// Package adapter 实现 OpenAI Responses 协议与网关内部 Chat Completions 协议之间的
// 双向翻译。
//
// 设计要点：网关上游只认 Chat Completions（/v1/chat/completions 的 SSE 流），
// 因此 Responses 端点走「翻译请求 → 复用 chat 执行链 → 翻译响应」三段式，
// 使选号、轮转、粘性、错误策略、用量记账等既有能力对 Responses 客户端零成本复用。
package adapter

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type responsesRequest struct {
	Model             string          `json:"model"`
	Instructions      json.RawMessage `json:"instructions"`
	Input             json.RawMessage `json:"input"`
	Stream            bool            `json:"stream"`
	MaxOutputTokens   *int            `json:"max_output_tokens"`
	Temperature       *float64        `json:"temperature"`
	TopP              *float64        `json:"top_p"`
	Tools             json.RawMessage `json:"tools"`
	ToolChoice        json.RawMessage `json:"tool_choice"`
	Reasoning         json.RawMessage `json:"reasoning"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls"`
	TextFormat        json.RawMessage `json:"text"`
}

// ResponsesModel 提取 Responses 请求体里的 model 字段（供 realm 判定使用）。
func ResponsesModel(body []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Model
}

// ResponsesToChat 把 Responses 请求体翻译为 Chat Completions 请求体。
// 返回值第二项为「是否流式」（Responses 默认非流式）。
func ResponsesToChat(body []byte) (out []byte, stream bool, err error) {
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false, fmt.Errorf("invalid responses request: %w", err)
	}

	chat := map[string]any{}
	messages := []any{}

	if len(req.Instructions) > 0 && string(req.Instructions) != "null" {
		if text := extractText(req.Instructions); text != "" {
			messages = append(messages, map[string]any{"role": "system", "content": text})
		}
	}

	inputMsgs, err := convertInput(req.Input)
	if err != nil {
		return nil, false, err
	}
	messages = append(messages, inputMsgs...)

	if len(messages) == 0 {
		return nil, false, fmt.Errorf("input is required")
	}

	chat["messages"] = messages
	chat["model"] = req.Model
	// 出站恒定流式：网关的上游链只产出 SSE（ChatStreamContext），非流式 Responses
	// 响应用 Aggregate 消费同一份流后一次性写出。若此处不透传 stream=true，
	// chatCompletionsBody 的 peek.Stream 会为 false，成功路径就会去 Aggregate
	// 一个本该逐帧翻译的流，导致流式请求被当成非流式处理。
	chat["stream"] = true

	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		chat["max_tokens"] = *req.MaxOutputTokens
	}
	if req.Temperature != nil {
		chat["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		chat["top_p"] = *req.TopP
	}

	if tools, err := convertTools(req.Tools); err != nil {
		return nil, false, err
	} else if tools != nil {
		chat["tools"] = tools
		if tc, err := convertToolChoice(req.ToolChoice); err != nil {
			return nil, false, err
		} else if tc != nil {
			chat["tool_choice"] = tc
		}
		if req.ParallelToolCalls != nil {
			chat["parallel_tool_calls"] = *req.ParallelToolCalls
		}
	}

	if effort := reasoningEffort(req.Reasoning); effort != "" {
		chat["reasoning_effort"] = effort
	}

	if rf := convertTextFormat(req.TextFormat); rf != nil {
		chat["response_format"] = rf
	}

	raw, err := json.Marshal(chat)
	if err != nil {
		return nil, false, err
	}
	return raw, req.Stream, nil
}

func extractText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		out := ""
		for _, p := range parts {
			if p.Text != "" {
				out += p.Text
			}
		}
		return out
	}
	return ""
}

// NewID 生成带前缀的随机 id（resp_ / msg_ / fc_ 等）。
func NewID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + "fallback"
	}
	return prefix + hex.EncodeToString(b)
}

// convertInput 把 Responses 的 input 翻译成 chat 的 messages 列表。
// 支持：纯字符串 / 消息数组（带 role）/ item 数组（function_call、function_call_output）。
func convertInput(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []any{map[string]any{"role": "user", "content": s}}, nil
	}

	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("input must be a string or an array: %w", err)
	}

	var out []any
	var pendingToolCalls []any

	flushToolCalls := func() {
		if len(pendingToolCalls) > 0 {
			out = append(out, map[string]any{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": pendingToolCalls,
			})
			pendingToolCalls = nil
		}
	}

	for _, item := range items {
		typ, _ := item["type"].(string)

		if typ == "function_call" {
			name, _ := item["name"].(string)
			callID, _ := item["call_id"].(string)
			args, _ := item["arguments"].(string)
			if args == "" {
				args = "{}"
			}
			pendingToolCalls = append(pendingToolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			})
			continue
		}

		if typ == "function_call_output" {
			flushToolCalls()
			callID, _ := item["call_id"].(string)
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": callID,
				"content":      stringifyToolOutput(item["output"]),
			})
			continue
		}

		// 历史思考不回灌上游，丢弃以免上游报错。
		if typ == "reasoning" {
			continue
		}

		role, _ := item["role"].(string)
		if role == "" {
			continue
		}
		flushToolCalls()

		content, err := convertContent(item["content"])
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"role": role, "content": content})
	}
	flushToolCalls()

	return out, nil
}

func convertContent(v any) (any, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return "", nil
	}

	parts := make([]any, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		switch typ {
		case "input_text", "output_text", "text":
			text, _ := m["text"].(string)
			parts = append(parts, map[string]any{"type": "text", "text": text})
		case "input_image", "image_url":
			url := ""
			switch u := m["image_url"].(type) {
			case string:
				url = u
			case map[string]any:
				url, _ = u["url"].(string)
			}
			if url == "" {
				if u, ok := m["url"].(string); ok {
					url = u
				}
			}
			if url != "" {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
			}
		}
	}

	allText := true
	for _, p := range parts {
		if m, ok := p.(map[string]any); !ok || m["type"] != "text" {
			allText = false
			break
		}
	}
	if allText {
		buf := ""
		for _, p := range parts {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					buf += t
				}
			}
		}
		return buf, nil
	}
	return parts, nil
}

func stringifyToolOutput(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		buf := ""
		for _, it := range t {
			if m, ok := it.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					buf += s
				}
			}
		}
		return buf
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

// convertTools 把 Responses 的平铺 function 工具定义翻译为 chat 的嵌套结构。
// Responses: {type:"function", name, description, parameters, strict}
// Chat:      {type:"function", function:{name, description, parameters, strict}}
func convertTools(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var tools []map[string]any
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("tools must be an array: %w", err)
	}
	var out []any
	for _, t := range tools {
		typ, _ := t["type"].(string)
		if _, ok := t["function"]; ok {
			out = append(out, t)
			continue
		}
		// Responses 内置工具（web_search / file_search 等）上游不支持，跳过。
		if typ != "function" && typ != "" {
			continue
		}
		fn := map[string]any{"name": t["name"]}
		if d, ok := t["description"]; ok {
			fn["description"] = d
		}
		if p, ok := t["parameters"]; ok {
			fn["parameters"] = p
		} else if p, ok := t["input_schema"]; ok {
			fn["parameters"] = p
		}
		if s, ok := t["strict"]; ok {
			fn["strict"] = s
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out, nil
}

func convertToolChoice(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("tool_choice must be a string or object: %w", err)
	}
	if name, ok := m["name"].(string); ok {
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": name},
		}, nil
	}
	return m, nil
}

func reasoningEffort(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	s, _ := m["effort"].(string)
	return s
}

// convertTextFormat 把 text.format 翻译为 chat 的 response_format。
func convertTextFormat(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	format, _ := m["format"].(map[string]any)
	if format == nil {
		format = m
	}
	typ, _ := format["type"].(string)
	switch typ {
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		js := map[string]any{"name": format["name"]}
		if s, ok := format["schema"]; ok {
			js["schema"] = s
		}
		if st, ok := format["strict"]; ok {
			js["strict"] = st
		}
		return map[string]any{"type": "json_schema", "json_schema": js}
	}
	return nil
}
