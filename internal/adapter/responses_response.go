package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChatToResponses 把聚合后的 Chat Completions 响应翻译为 Responses 协议对象。
func ChatToResponses(chat map[string]any) map[string]any {
	content, reasoning, toolCalls := extractChatMessage(chat)

	respID := NewID("resp_")
	model, _ := chat["model"].(string)

	output := []any{}
	var outputText strings.Builder

	if reasoning != "" {
		output = append(output, map[string]any{
			"type": "reasoning",
			"id":   NewID("rs_"),
			"summary": []any{
				map[string]any{"type": "summary_text", "text": reasoning},
			},
		})
	}

	// 有工具调用且无正文时省略空 message（对齐官方行为）。
	if content != "" || len(toolCalls) == 0 {
		outputText.WriteString(content)
		output = append(output, map[string]any{
			"type":   "message",
			"id":     NewID("msg_"),
			"status": "completed",
			"role":   "assistant",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        content,
					"annotations": []any{},
				},
			},
		})
	}

	for _, tc := range toolCalls {
		args, _ := tc["arguments"].(string)
		output = append(output, map[string]any{
			"type":      "function_call",
			"id":        NewID("fc_"),
			"call_id":   tc["id"],
			"name":      tc["name"],
			"arguments": args,
			"status":    "completed",
		})
	}

	return map[string]any{
		"id":                   respID,
		"object":               "response",
		"created_at":           createdAt(chat),
		"status":               responsesStatus(chat),
		"model":                model,
		"output":               output,
		"output_text":          outputText.String(),
		"usage":                convertUsage(chat["usage"]),
		"error":                nil,
		"incomplete_details":   nil,
		"instructions":         nil,
		"metadata":             map[string]any{},
		"parallel_tool_calls":  true,
		"temperature":          nil,
		"tool_choice":          "auto",
		"tools":                []any{},
		"top_p":                nil,
		"max_output_tokens":    nil,
		"previous_response_id": nil,
		"reasoning":            map[string]any{"effort": nil, "summary": nil},
		"store":                false,
		"truncation":           "disabled",
		"user":                 nil,
	}
}

// extractChatMessage 从 chat 响应里取出正文、思考文本与工具调用列表。
func extractChatMessage(chat map[string]any) (content, reasoning string, toolCalls []map[string]any) {
	choices, _ := chat["choices"].([]any)
	if len(choices) == 0 {
		return "", "", nil
	}
	c0, _ := choices[0].(map[string]any)
	if c0 == nil {
		return "", "", nil
	}
	msg, _ := c0["message"].(map[string]any)
	if msg == nil {
		return "", "", nil
	}
	if s, ok := msg["content"].(string); ok {
		content = s
	}
	if s, ok := msg["reasoning_content"].(string); ok && s != "" {
		reasoning = s
	} else if s, ok := msg["reasoning"].(string); ok {
		reasoning = s
	}

	if arr, ok := msg["tool_calls"].([]any); ok {
		for _, it := range arr {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			fn, _ := m["function"].(map[string]any)
			name, args := "", ""
			if fn != nil {
				name, _ = fn["name"].(string)
				args, _ = fn["arguments"].(string)
			}
			id, _ := m["id"].(string)
			if id == "" {
				id = NewID("call_")
			}
			toolCalls = append(toolCalls, map[string]any{
				"id": id, "name": name, "arguments": args,
			})
		}
	}
	return content, reasoning, toolCalls
}

// convertUsage 把 chat 的 usage 映射为 Responses 的 usage 结构。
func convertUsage(v any) map[string]any {
	u, _ := v.(map[string]any)
	in, out, total := 0, 0, 0
	cached, reasoningTokens := 0, 0
	if u != nil {
		in = intOf(u["prompt_tokens"])
		out = intOf(u["completion_tokens"])
		total = intOf(u["total_tokens"])
		if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
			cached = intOf(d["cached_tokens"])
		}
		if d, ok := u["completion_tokens_details"].(map[string]any); ok {
			reasoningTokens = intOf(d["reasoning_tokens"])
		}
	}
	if total == 0 {
		total = in + out
	}
	return map[string]any{
		"input_tokens":          in,
		"input_tokens_details":  map[string]any{"cached_tokens": cached},
		"output_tokens":         out,
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoningTokens},
		"total_tokens":          total,
	}
}

func intOf(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	}
	return 0
}

func createdAt(chat map[string]any) int64 {
	if v, ok := chat["created"].(float64); ok {
		return int64(v)
	}
	return 0
}

// responsesStatus 由 finish_reason 映射响应状态。
func responsesStatus(chat map[string]any) string {
	switch finishReason(chat) {
	case "length", "max_tokens":
		return "incomplete"
	}
	return "completed"
}

func finishReason(chat map[string]any) string {
	choices, _ := chat["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	c0, _ := choices[0].(map[string]any)
	if c0 == nil {
		return ""
	}
	s, _ := c0["finish_reason"].(string)
	return s
}

var _ = fmt.Sprintf
