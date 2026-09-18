package adapter

import (
	"encoding/json"
	"testing"
)

// TestResponsesToChatStringInput 覆盖最小请求：字符串 input。
func TestResponsesToChatStringInput(t *testing.T) {
	out, stream, err := ResponsesToChat([]byte(`{"model":"deepseek-v4.1-flash","input":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream {
		t.Fatal("Responses 默认非流式，stream 应为 false")
	}
	var chat map[string]any
	if err := json.Unmarshal(out, &chat); err != nil {
		t.Fatalf("out not json: %v", err)
	}
	if chat["model"] != "deepseek-v4.1-flash" {
		t.Fatalf("model mismatch: %v", chat["model"])
	}
	msgs, _ := chat["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "user" || m0["content"] != "hello" {
		t.Fatalf("bad message: %v", m0)
	}
}

// TestResponsesToChatInstructions 验证 instructions → system 消息。
func TestResponsesToChatInstructions(t *testing.T) {
	out, _, err := ResponsesToChat([]byte(`{
		"model":"m","instructions":"be brief","input":"hi"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var chat map[string]any
	json.Unmarshal(out, &chat)
	msgs, _ := chat["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "be brief" {
		t.Fatalf("bad system message: %v", m0)
	}
}

// TestResponsesToChatToolFlow 验证 function_call / function_call_output 双向翻译，
// 这是工具调用多轮回灌的关键路径。
func TestResponsesToChatToolFlow(t *testing.T) {
	body := `{
		"model":"m",
		"input":[
			{"role":"user","content":"weather?"},
			{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"SH\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"sunny"}
		],
		"tools":[{"type":"function","name":"get_weather","description":"d",
		          "parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"tool_choice":"auto"
	}`
	out, _, err := ResponsesToChat([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var chat map[string]any
	json.Unmarshal(out, &chat)
	msgs, _ := chat["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user, assistant.tool_calls, tool), got %d", len(msgs))
	}
	// 第二条必须是带 tool_calls 的 assistant（chat 协议的硬性结构要求）。
	a, _ := msgs[1].(map[string]any)
	if a["role"] != "assistant" {
		t.Fatalf("msg[1] should be assistant, got %v", a["role"])
	}
	tcs, _ := a["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tcs))
	}
	tc0, _ := tcs[0].(map[string]any)
	if tc0["id"] != "call_1" {
		t.Fatalf("tool call id mismatch: %v", tc0["id"])
	}
	fn, _ := tc0["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("tool name mismatch: %v", fn["name"])
	}
	// 第三条是 tool 结果
	tr, _ := msgs[2].(map[string]any)
	if tr["role"] != "tool" || tr["tool_call_id"] != "call_1" || tr["content"] != "sunny" {
		t.Fatalf("bad tool result: %v", tr)
	}
	// tools 必须翻译为 chat 嵌套结构
	tools, _ := chat["tools"].([]any)
	t0, _ := tools[0].(map[string]any)
	if _, ok := t0["function"]; !ok {
		t.Fatalf("tools must be nested for chat: %v", t0)
	}
}

// TestChatToResponses 覆盖非流式响应翻译，含正文 / 思考 / 工具调用。
func TestChatToResponses(t *testing.T) {
	chat := map[string]any{
		"id":      "chatcmpl-1",
		"model":   "deepseek-v4.1-flash",
		"created": float64(1700000000),
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{
				"role":              "assistant",
				"content":           "let me check",
				"reasoning_content": "thinking hard",
				"tool_calls": []any{map[string]any{
					"id":   "call_9",
					"type": "function",
					"function": map[string]any{
						"name": "get_weather", "arguments": `{"city":"SH"}`,
					},
				}},
			},
		}},
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(20),
			"total_tokens":      float64(30),
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": float64(5),
			},
		},
	}

	resp := ChatToResponses(chat)
	if resp["object"] != "response" {
		t.Fatalf("object should be response: %v", resp["object"])
	}
	if resp["status"] != "completed" {
		t.Fatalf("status should be completed: %v", resp["status"])
	}
	if resp["output_text"] != "let me check" {
		t.Fatalf("output_text mismatch: %v", resp["output_text"])
	}
	out, _ := resp["output"].([]any)
	if len(out) != 3 {
		t.Fatalf("expected 3 output items (reasoning, message, function_call), got %d", len(out))
	}
	r0, _ := out[0].(map[string]any)
	if r0["type"] != "reasoning" {
		t.Fatalf("output[0] should be reasoning: %v", r0["type"])
	}
	m1, _ := out[1].(map[string]any)
	if m1["type"] != "message" {
		t.Fatalf("output[1] should be message: %v", m1["type"])
	}
	f2, _ := out[2].(map[string]any)
	if f2["type"] != "function_call" || f2["call_id"] != "call_9" || f2["name"] != "get_weather" {
		t.Fatalf("bad function_call item: %v", f2)
	}
	if f2["arguments"] != `{"city":"SH"}` {
		t.Fatalf("arguments must stay string: %v", f2["arguments"])
	}
	// usage 字段名必须换成 Responses 口径
	u, _ := resp["usage"].(map[string]any)
	if u["input_tokens"] != 10 || u["output_tokens"] != 20 || u["total_tokens"] != 30 {
		t.Fatalf("bad usage: %v", u)
	}
}

// TestChatToResponsesIncomplete 验证 length → incomplete。
func TestChatToResponsesIncomplete(t *testing.T) {
	chat := map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": "length",
			"message":       map[string]any{"content": "truncated"},
		}},
	}
	resp := ChatToResponses(chat)
	if resp["status"] != "incomplete" {
		t.Fatalf("length should map to incomplete, got %v", resp["status"])
	}
}

// TestResponsesStreamConverterText 验证纯文本流的事件序列与增量正确性。
func TestResponsesStreamConverterText(t *testing.T) {
	c := NewResponsesStreamConverter("deepseek-v4.1-flash")

	first := c.Feed(`{"id":"c1","created":1700000000,"choices":[{"delta":{"content":"Hel"}}]}`)
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
	} {
		if !contains(first, want) {
			t.Fatalf("first frame missing %q\n%s", want, first)
		}
	}
	if !contains(first, `"delta":"Hel"`) {
		t.Fatalf("first frame missing delta payload:\n%s", first)
	}

	second := c.Feed(`{"choices":[{"delta":{"content":"lo"}}]}`)
	if contains(second, "response.output_item.added") {
		t.Fatalf("second frame should not re-add item:\n%s", second)
	}
	if !contains(second, `"delta":"lo"`) {
		t.Fatalf("second frame missing delta:\n%s", second)
	}

	fin := c.Feed("[DONE]")
	for _, want := range []string{
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.completed",
	} {
		if !contains(fin, want) {
			t.Fatalf("finish missing %q\n%s", want, fin)
		}
	}
	if !contains(fin, `"text":"Hello"`) {
		t.Fatalf("finish should carry full text:\n%s", fin)
	}
	if got := c.Text(); got != "Hello" {
		t.Fatalf("accumulated text mismatch: %q", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestResponsesStreamConverterToolCalls 验证工具调用增量参数拼接。
func TestResponsesStreamConverterToolCalls(t *testing.T) {
	c := NewResponsesStreamConverter("m")

	c.Feed(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":""}}]}}]}`)
	f2 := c.Feed(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci"}}]}}]}`)
	f3 := c.Feed(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"SH\"}"}}]}}]}`)

	if !contains(f2, "event: response.function_call_arguments.delta") {
		t.Fatalf("frame2 should carry arguments delta:\n%s", f2)
	}

	fin := c.Feed("[DONE]")
	if !contains(fin, "event: response.function_call_arguments.done") {
		t.Fatalf("finish missing arguments.done:\n%s", fin)
	}
	tcs := c.ToolCalls()
	if len(tcs) != 1 || tcs[0]["name"] != "get_weather" {
		t.Fatalf("bad tool calls: %v", tcs)
	}
	if tcs[0]["arguments"] != `{"city":"SH"}` {
		t.Fatalf("arguments not reassembled: %v", tcs[0]["arguments"])
	}
	_ = f3
}

// TestResponsesStreamConverterReasoning 验证思考走 reasoning_summary 事件，
// 且正文 output_index 顺延为 1（不与 reasoning 抢 0）。
func TestResponsesStreamConverterReasoning(t *testing.T) {
	c := NewResponsesStreamConverter("m")
	f1 := c.Feed(`{"choices":[{"delta":{"reasoning_content":"hmm"}}]}`)
	if !contains(f1, "event: response.reasoning_summary_text.delta") {
		t.Fatalf("reasoning delta missing:\n%s", f1)
	}
	f2 := c.Feed(`{"choices":[{"delta":{"content":"answer"}}]}`)
	if !contains(f2, `"output_index":1`) {
		t.Fatalf("text output_index should be 1 after reasoning:\n%s", f2)
	}
}

// TestResponsesStreamConverterFinishIdempotent 收尾幂等。
func TestResponsesStreamConverterFinishIdempotent(t *testing.T) {
	c := NewResponsesStreamConverter("m")
	c.Feed(`{"choices":[{"delta":{"content":"x"}}]}`)
	if c.Finish() == "" {
		t.Fatal("first Finish should emit events")
	}
	if again := c.Finish(); again != "" {
		t.Fatalf("second Finish must be empty, got:\n%s", again)
	}
}

// TestResponsesStreamConverterEmptyStream 空流也要有 created + completed。
func TestResponsesStreamConverterEmptyStream(t *testing.T) {
	c := NewResponsesStreamConverter("m")
	out := c.Finish()
	if !contains(out, "event: response.created") || !contains(out, "event: response.completed") {
		t.Fatalf("empty stream must still bracket the stream:\n%s", out)
	}
}
