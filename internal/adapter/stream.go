package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResponsesStreamConverter 把上游 Chat Completions 的 SSE 增量翻译为 Responses 事件流。
// 事件序列与官方对齐：
//
//	response.created / response.in_progress
//	response.output_item.added (reasoning) + response.reasoning_summary_part.added
//	response.reasoning_summary_text.delta ...
//	response.output_item.added (message) + response.content_part.added
//	response.output_text.delta ...
//	response.output_text.done + response.content_part.done + response.output_item.done
//	response.output_item.added (function_call) + response.function_call_arguments.delta ...
//	response.function_call_arguments.done + response.output_item.done
//	response.completed
type ResponsesStreamConverter struct {
	model     string
	respID    string
	createdAt int64

	text      strings.Builder
	reasoning strings.Builder

	toolOrder []int
	tools     map[int]*responsesToolState

	created        bool
	messageAdded   bool
	reasoningAdded bool
	completed      bool
	usage          map[string]any
	finishReason   string
}

type responsesToolState struct {
	callID      string
	name        string
	args        strings.Builder
	itemID      string
	outputIndex int
	added       bool
}

// NewResponsesStreamConverter 构造流式翻译器。
func NewResponsesStreamConverter(model string) *ResponsesStreamConverter {
	return &ResponsesStreamConverter{
		model:  model,
		respID: NewID("resp_"),
		tools:  map[int]*responsesToolState{},
	}
}

// Feed 喂入一帧 chat SSE 的 data 载荷（已去掉 "data: " 前缀）。
// payload 为 "[DONE]" 时表示流结束，返回收尾事件。
func (c *ResponsesStreamConverter) Feed(payload string) string {
	if payload == "[DONE]" {
		return c.Finish()
	}
	var chunk map[string]any
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return ""
	}

	var out strings.Builder
	if !c.created {
		c.created = true
		if v, ok := chunk["created"].(float64); ok {
			c.createdAt = int64(v)
		}
		out.WriteString(c.event("response.created", c.responseObject("in_progress", nil)))
		out.WriteString(c.event("response.in_progress", c.responseObject("in_progress", nil)))
	}
	if u, ok := chunk["usage"].(map[string]any); ok {
		c.usage = u
	}

	choices, _ := chunk["choices"].([]any)
	for _, ci := range choices {
		ch, _ := ci.(map[string]any)
		if ch == nil {
			continue
		}
		if fr, ok := ch["finish_reason"].(string); ok && fr != "" {
			c.finishReason = fr
		}
		delta, _ := ch["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		c.feedReasoning(&out, delta)
		c.feedText(&out, delta)
		c.feedToolCalls(&out, delta)
	}
	return out.String()
}

// feedReasoning 处理思考增量 → reasoning_summary_text.delta。
func (c *ResponsesStreamConverter) feedReasoning(out *strings.Builder, delta map[string]any) {
	rc, ok := delta["reasoning_content"].(string)
	if !ok || rc == "" {
		return
	}
	if !c.reasoningAdded {
		c.reasoningAdded = true
		out.WriteString(c.event("response.output_item.added", map[string]any{
			"output_index": 0,
			"item": map[string]any{
				"type": "reasoning", "id": NewID("rs_"), "summary": []any{},
			},
		}))
		out.WriteString(c.event("response.reasoning_summary_part.added", map[string]any{
			"output_index":  0,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": ""},
		}))
	}
	c.reasoning.WriteString(rc)
	out.WriteString(c.event("response.reasoning_summary_text.delta", map[string]any{
		"output_index":  0,
		"summary_index": 0,
		"delta":         rc,
	}))
}

// feedText 处理正文增量 → output_text.delta。
func (c *ResponsesStreamConverter) feedText(out *strings.Builder, delta map[string]any) {
	txt, ok := delta["content"].(string)
	if !ok || txt == "" {
		return
	}
	idx := c.messageOutputIndex()
	if !c.messageAdded {
		c.messageAdded = true
		out.WriteString(c.event("response.output_item.added", map[string]any{
			"output_index": idx,
			"item": map[string]any{
				"type": "message", "id": NewID("msg_"), "status": "in_progress",
				"role": "assistant", "content": []any{},
			},
		}))
		out.WriteString(c.event("response.content_part.added", map[string]any{
			"output_index":  idx,
			"content_index": 0,
			"part": map[string]any{
				"type": "output_text", "text": "", "annotations": []any{},
			},
		}))
	}
	c.text.WriteString(txt)
	out.WriteString(c.event("response.output_text.delta", map[string]any{
		"output_index":  idx,
		"content_index": 0,
		"delta":         txt,
	}))
}

// messageOutputIndex 正文消息的 output_index（reasoning 占用 0 时顺延）。
func (c *ResponsesStreamConverter) messageOutputIndex() int {
	if c.reasoningAdded {
		return 1
	}
	return 0
}

// feedToolCalls 处理工具调用增量 → function_call_arguments.delta。
func (c *ResponsesStreamConverter) feedToolCalls(out *strings.Builder, delta map[string]any) {
	// 同 responses_response.go：聚合器与 JSON 反序列化产出的切片静态类型不同，
	// 只认 []any 会让流式工具调用被静默丢弃。
	arr := toAnySlice(delta["tool_calls"])
	if arr == nil {
		return
	}
	for _, it := range arr {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		idx := intOf(m["index"])
		st := c.tools[idx]
		if st == nil {
			st = &responsesToolState{outputIndex: idx, itemID: NewID("fc_")}
			c.tools[idx] = st
			c.toolOrder = append(c.toolOrder, idx)
		}
		if id, ok := m["id"].(string); ok && id != "" && st.callID == "" {
			st.callID = id
		}

		name, args := "", ""
		if fn, ok := m["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				name = n
			}
			if a, ok := fn["arguments"].(string); ok {
				args = a
			}
		}
		if name != "" && st.name == "" {
			st.name = name
		}
		// 首个非空 name 出现时才下发 output_item.added（Responses 要求 item 先于 delta）。
		if !st.added && st.name != "" {
			st.added = true
			out.WriteString(c.event("response.output_item.added", map[string]any{
				"output_index": st.outputIndex,
				"item": map[string]any{
					"type": "function_call", "id": st.itemID,
					"call_id": st.callID, "name": st.name,
					"arguments": "", "status": "in_progress",
				},
			}))
		}
		if args != "" {
			st.args.WriteString(args)
			if st.added {
				out.WriteString(c.event("response.function_call_arguments.delta", map[string]any{
					"item_id":      st.itemID,
					"output_index": st.outputIndex,
					"delta":        args,
				}))
			}
		}
	}
}

// Finish 收尾：下发 done 类事件与 response.completed。幂等。
func (c *ResponsesStreamConverter) Finish() string {
	if c.completed {
		return ""
	}
	c.completed = true

	var out strings.Builder
	if !c.created {
		// 空流兜底：至少给出 created，客户端不会卡死等首帧。
		c.created = true
		out.WriteString(c.event("response.created", c.responseObject("in_progress", nil)))
	}

	idx := c.messageOutputIndex()
	if c.messageAdded {
		out.WriteString(c.event("response.output_text.done", map[string]any{
			"output_index":  idx,
			"content_index": 0,
			"text":          c.text.String(),
		}))
		out.WriteString(c.event("response.content_part.done", map[string]any{
			"output_index":  idx,
			"content_index": 0,
			"part": map[string]any{
				"type": "output_text", "text": c.text.String(), "annotations": []any{},
			},
		}))
	}

	for _, i := range c.toolOrder {
		st := c.tools[i]
		if st == nil || !st.added {
			continue
		}
		out.WriteString(c.event("response.function_call_arguments.done", map[string]any{
			"item_id":      st.itemID,
			"output_index": st.outputIndex,
			"arguments":    st.args.String(),
		}))
	}

	status := "completed"
	if c.finishReason == "length" || c.finishReason == "max_tokens" {
		status = "incomplete"
	}
	obj := c.responseObject(status, c.usage)
	obj["output"] = c.finalOutput()
	out.WriteString(c.event("response.completed", obj))

	return out.String()
}

// finalOutput 组装 completed 事件里的完整 output 数组。
func (c *ResponsesStreamConverter) finalOutput() []any {
	output := []any{}
	if c.reasoning.Len() > 0 {
		output = append(output, map[string]any{
			"type": "reasoning", "id": NewID("rs_"),
			"summary": []any{
				map[string]any{"type": "summary_text", "text": c.reasoning.String()},
			},
		})
	}
	if c.text.Len() > 0 || len(c.toolOrder) == 0 {
		output = append(output, map[string]any{
			"type": "message", "id": NewID("msg_"), "status": "completed",
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "output_text", "text": c.text.String(), "annotations": []any{},
				},
			},
		})
	}
	for _, i := range c.toolOrder {
		st := c.tools[i]
		if st == nil {
			continue
		}
		output = append(output, map[string]any{
			"type": "function_call", "id": st.itemID,
			"call_id": st.callID, "name": st.name,
			"arguments": st.args.String(), "status": "completed",
		})
	}
	return output
}

// Text 返回累计正文（供网关记账）。
func (c *ResponsesStreamConverter) Text() string { return c.text.String() }

// Reasoning 返回累计思考内容。
func (c *ResponsesStreamConverter) Reasoning() string { return c.reasoning.String() }

// ToolCalls 返回聚合后的工具调用。
func (c *ResponsesStreamConverter) ToolCalls() []map[string]any {
	var out []map[string]any
	for _, i := range c.toolOrder {
		st := c.tools[i]
		if st == nil {
			continue
		}
		out = append(out, map[string]any{
			"id": st.callID, "name": st.name, "arguments": st.args.String(),
		})
	}
	return out
}

// responseObject 构造 response 骨架对象（created / completed 事件共用）。
func (c *ResponsesStreamConverter) responseObject(status string, usage any) map[string]any {
	return map[string]any{
		"id":         c.respID,
		"object":     "response",
		"created_at": c.createdAt,
		"status":     status,
		"model":      c.model,
		"output":     []any{},
		"usage":      convertUsage(usage),
		"error":      nil,
		"metadata":   map[string]any{},
	}
}

// event 组装一条 SSE 事件：event: <type>\ndata: <json>\n\n
func (c *ResponsesStreamConverter) event(typ string, fields map[string]any) string {
	fields["type"] = typ
	raw, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", typ, raw)
}
