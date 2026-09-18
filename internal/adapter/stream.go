package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResponsesStreamConverter 把上游 Chat Completions 的 SSE 增量翻译为 Responses 事件流。
//
// 事件序列、字段名与 output_index 规则逐条对齐参考实现
// （com.joy4fire.workbuddy2api，端口 :8788，已确认能驱动该客户端完成多轮工具调用）：
//
//	response.created / response.in_progress（二者为同一 response 对象，usage=null）
//	[reasoning]     reasoning_summary_part.added → reasoning_summary_text.delta ...
//	[message]       output_item.added(idx=x) → content_part.added → output_text.delta ...
//	[function_call] output_item.added → function_call_arguments.delta ...
//	收尾            output_text.done / content_part.done
//	                reasoning_summary_text.done
//	                output_item.done（message）
//	                function_call_arguments.done + output_item.done（每个 function_call）
//	                response.completed
//
// 与参考实现的三条一致约定（此前偏差正是「对话没结束就断」的原因）：
//
//  1. 每个 output_item.added 必须配一个同 id、同 output_index 的 output_item.done；
//     message 的 id 在 added / done / completed 三处必须一致（不得重新生成）；
//  2. reasoning 不产生独立 output item（不发 output_item.added/done），
//     其 summary 事件固定用 output_index 0、summary_index 0；
//     message 也用 output_index 0；function_call 从 baseIndex() 起按出现顺序递增；
//  3. created / in_progress 的 usage 必须是 JSON null，有真实用量时才展开。
type ResponsesStreamConverter struct {
	model     string
	respID    string
	msgID     string
	createdAt int64

	text      strings.Builder
	reasoning strings.Builder

	toolOrder []int
	tools     map[int]*responsesToolState

	created        bool
	messageAdded   bool
	contentAdded   bool
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
		msgID:  NewID("msg_"),
		tools:  map[int]*responsesToolState{},
	}
}

// baseIndex 是 function_call 的 output_index 起点：已有 message 时为 1，否则为 0。
// 与参考实现一致——message 固定占 0，工具项顺延。
func (c *ResponsesStreamConverter) baseIndex() int {
	if c.messageAdded || c.text.Len() > 0 {
		return 1
	}
	return 0
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
		if m, ok := chunk["model"].(string); ok && m != "" {
			c.model = m
		}
		// 官方（及参考实现）把 response 对象嵌套在 "response" 键下，
		// 客户端按 data.response.output 取最终结果——平铺会让它取不到而判流异常。
		// created 与 in_progress 复用同一个对象（参考实现如此）。
		wrapped := map[string]any{"response": c.responseObject("in_progress", nil)}
		out.WriteString(c.event("response.created", wrapped))
		out.WriteString(c.event("response.in_progress", wrapped))
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
		// 顺序与参考实现一致：正文 → reasoning → tool_calls。
		c.feedText(&out, delta)
		c.feedReasoning(&out, delta)
		c.feedToolCalls(&out, delta)
	}
	return out.String()
}

// ensureMessage 在首个正文增量前补发 message item 与 content_part.added。
// message 的 id 在此生成一次，done / completed 复用，绝不重新生成。
func (c *ResponsesStreamConverter) ensureMessage(out *strings.Builder) {
	if c.messageAdded {
		return
	}
	c.messageAdded = true
	out.WriteString(c.event("response.output_item.added", map[string]any{
		"output_index": 0,
		"item":         c.messageItem("in_progress"),
	}))
}

// feedText 处理正文增量 → message item + output_text.delta。
func (c *ResponsesStreamConverter) feedText(out *strings.Builder, delta map[string]any) {
	txt, ok := delta["content"].(string)
	if !ok || txt == "" {
		return
	}
	c.ensureMessage(out)
	if !c.contentAdded {
		c.contentAdded = true
		out.WriteString(c.event("response.content_part.added", map[string]any{
			"output_index":  0,
			"content_index": 0,
			"part": map[string]any{
				"type": "output_text", "text": "", "annotations": []any{},
			},
		}))
	}
	c.text.WriteString(txt)
	out.WriteString(c.event("response.output_text.delta", map[string]any{
		"output_index":  0,
		"content_index": 0,
		"delta":         txt,
	}))
}

// feedReasoning 处理思考增量 → reasoning_summary 事件。
//
// 参考实现不为 reasoning 建独立 output item（不发 output_item.added/done），
// summary 事件固定 output_index=0、summary_index=0。
func (c *ResponsesStreamConverter) feedReasoning(out *strings.Builder, delta map[string]any) {
	rc, ok := delta["reasoning_content"].(string)
	if !ok || rc == "" {
		rc, ok = delta["reasoning"].(string)
		if !ok || rc == "" {
			return
		}
	}
	c.reasoning.WriteString(rc)
	if !c.reasoningAdded {
		c.reasoningAdded = true
		out.WriteString(c.event("response.reasoning_summary_part.added", map[string]any{
			"output_index":  0,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": ""},
		}))
	}
	out.WriteString(c.event("response.reasoning_summary_text.delta", map[string]any{
		"output_index":  0,
		"summary_index": 0,
		"delta":         rc,
	}))
}

// feedToolCalls 处理工具调用增量 → function_call item + arguments.delta。
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
		key := intOf(m["index"])
		st := c.tools[key]
		if st == nil {
			// output_index 由「起点 + 已出现的工具数」决定，与参考实现一致：
			// 绝不复用上游的 tool index，否则会与 message 的 0 撞车。
			st = &responsesToolState{
				outputIndex: c.baseIndex() + len(c.tools),
				itemID:      NewID("fc_"),
			}
			c.tools[key] = st
			c.toolOrder = append(c.toolOrder, key)
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
			if st.callID == "" {
				st.callID = NewID("call_")
			}
			out.WriteString(c.event("response.output_item.added", map[string]any{
				"output_index": st.outputIndex,
				"item":         c.functionItem(st, "in_progress"),
			}))
		}
		if args != "" {
			st.args.WriteString(args)
			if st.added {
				out.WriteString(c.event("response.function_call_arguments.delta", map[string]any{
					"output_index": st.outputIndex,
					"delta":        args,
				}))
			}
		}
	}
}

// Finish 收尾：下发 done 类事件与 response.completed。幂等。
// 事件顺序与参考实现逐条一致。
func (c *ResponsesStreamConverter) Finish() string {
	if c.completed {
		return ""
	}
	c.completed = true

	var out strings.Builder
	if !c.created {
		// 空流兜底：至少给出 created，客户端不会卡死等首帧。
		c.created = true
		out.WriteString(c.event("response.created",
			map[string]any{"response": c.responseObject("in_progress", nil)}))
	}

	if c.contentAdded {
		out.WriteString(c.event("response.output_text.done", map[string]any{
			"output_index":  0,
			"content_index": 0,
			"text":          c.text.String(),
		}))
		out.WriteString(c.event("response.content_part.done", map[string]any{
			"output_index":  0,
			"content_index": 0,
			"part": map[string]any{
				"type": "output_text", "text": c.text.String(), "annotations": []any{},
			},
		}))
	}

	// reasoning_summary_text.done：缺了它部分客户端会认为 reasoning 段落没有正常结束。
	if c.reasoningAdded {
		out.WriteString(c.event("response.reasoning_summary_text.done", map[string]any{
			"output_index":  0,
			"summary_index": 0,
			"text":          c.reasoning.String(),
		}))
	}

	if c.messageAdded {
		out.WriteString(c.event("response.output_item.done", map[string]any{
			"output_index": 0,
			"item":         c.messageItem("completed"),
		}))
	}

	for _, key := range c.toolOrder {
		st := c.tools[key]
		if st == nil || !st.added {
			continue
		}
		out.WriteString(c.event("response.function_call_arguments.done", map[string]any{
			"output_index": st.outputIndex,
			"arguments":    st.args.String(),
		}))
		out.WriteString(c.event("response.output_item.done", map[string]any{
			"output_index": st.outputIndex,
			"item":         c.functionItem(st, "completed"),
		}))
	}

	status := "completed"
	if c.finishReason == "length" || c.finishReason == "max_tokens" {
		status = "incomplete"
	}
	obj := c.responseObject(status, c.usage)
	obj["output"] = c.finalOutput()
	out.WriteString(c.event("response.completed", map[string]any{"response": obj}))

	return out.String()
}

// messageItem 构造 assistant message item。id 固定，三处复用。
// empty=false 时带完整正文（done / completed 用）。
func (c *ResponsesStreamConverter) messageItem(status string) map[string]any {
	content := []any{}
	if status == "completed" {
		content = append(content, map[string]any{
			"type": "output_text", "text": c.text.String(), "annotations": []any{},
		})
	}
	return map[string]any{
		"type": "message", "id": c.msgID, "status": status,
		"role": "assistant", "content": content,
	}
}

// functionItem 构造 function_call item（added / done / completed 三处同 id / call_id）。
func (c *ResponsesStreamConverter) functionItem(st *responsesToolState, status string) map[string]any {
	return map[string]any{
		"type": "function_call", "id": st.itemID, "call_id": st.callID,
		"name": st.name, "arguments": st.args.String(), "status": status,
	}
}

// finalOutput 组装 completed 事件里的完整 output 数组，顺序与 output_index 一致。
func (c *ResponsesStreamConverter) finalOutput() []any {
	output := []any{}
	if c.messageAdded || c.text.Len() > 0 {
		output = append(output, c.messageItem("completed"))
	}
	for _, key := range c.toolOrder {
		st := c.tools[key]
		if st == nil || !st.added {
			continue
		}
		output = append(output, c.functionItem(st, "completed"))
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
	for _, key := range c.toolOrder {
		st := c.tools[key]
		if st == nil {
			continue
		}
		out = append(out, map[string]any{
			"id": st.callID, "name": st.name, "arguments": st.args.String(),
		})
	}
	return out
}

// responseObject 构造 response 骨架对象。
// parallel_tool_calls 与 usage:null 与参考实现一致。
func (c *ResponsesStreamConverter) responseObject(status string, usage any) map[string]any {
	return map[string]any{
		"id":                  c.respID,
		"object":              "response",
		"created_at":          c.createdAt,
		"status":              status,
		"model":               c.model,
		"output":              []any{},
		"parallel_tool_calls": true,
		"usage":               usageOrNull(usage),
	}
}

// usageOrNull 让 created / in_progress 的 usage 为 JSON null（对齐参考实现），
// 有真实用量时才展开成完整 usage 结构。
func usageOrNull(v any) any {
	if v == nil {
		return nil
	}
	return convertUsage(v)
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
