package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

// parseEvents 从事件流文本里解析出事件对象列表。
func parseEvents(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, blk := range strings.Split(s, "\n\n") {
		blk = strings.TrimSpace(blk)
		if blk == "" {
			continue
		}
		var payload string
		for _, line := range strings.Split(blk, "\n") {
			if strings.HasPrefix(line, "data: ") {
				payload = strings.TrimPrefix(line, "data: ")
			}
		}
		if payload == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("bad event json: %v\n%s", err, payload)
		}
		out = append(out, m)
	}
	return out
}

// TestResponsesStreamItemLifecycle 锁定三条结构不变量：
//  1. 每个 output_item.added 都有同 id、同 output_index 的 output_item.done；
//  2. output_index 全局唯一递增，reasoning / message / function_call 不撞车；
//  3. completed 里各 item 的 id 与流中一致。
//
// 违反任一条，客户端会在工具调用后判定流异常并主动断开（「对话没结束就断」）。
func TestResponsesStreamItemLifecycle(t *testing.T) {
	c := NewResponsesStreamConverter("m")
	var sb strings.Builder

	sb.WriteString(c.Feed(`{"created":1700000000,"choices":[{"delta":{"reasoning_content":"想一想"}}]}`))
	sb.WriteString(c.Feed(`{"choices":[{"delta":{"content":"我来查"}}]}`))
	sb.WriteString(c.Feed(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`))
	sb.WriteString(c.Feed(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"上海\"}"}}]}}]}`))
	sb.WriteString(c.Finish())

	evs := parseEvents(t, sb.String())

	addedByIndex := map[float64]string{}
	doneByIndex := map[float64]string{}
	for _, e := range evs {
		typ, _ := e["type"].(string)
		if typ != "response.output_item.added" && typ != "response.output_item.done" {
			continue
		}
		oi, ok := e["output_index"].(float64)
		if !ok {
			t.Fatalf("%s missing output_index: %v", typ, e)
		}
		item, _ := e["item"].(map[string]any)
		if item == nil {
			t.Fatalf("%s missing item: %v", typ, e)
		}
		id, _ := item["id"].(string)
		if id == "" {
			t.Fatalf("%s item missing id: %v", typ, e)
		}
		if typ == "response.output_item.added" {
			addedByIndex[oi] = id
		} else {
			doneByIndex[oi] = id
		}
	}
	if len(addedByIndex) == 0 {
		t.Fatal("no output_item.added emitted")
	}
	for oi, id := range addedByIndex {
		doneID, ok := doneByIndex[oi]
		if !ok {
			t.Fatalf("output_index %v added (id=%s) has no matching output_item.done", oi, id)
		}
		if doneID != id {
			t.Fatalf("output_index %v: added id=%s but done id=%s", oi, id, doneID)
		}
	}

	itemTypes := map[float64]string{}
	for _, e := range evs {
		if e["type"] != "response.output_item.added" {
			continue
		}
		oi, _ := e["output_index"].(float64)
		item, _ := e["item"].(map[string]any)
		it, _ := item["type"].(string)
		if prev, dup := itemTypes[oi]; dup {
			t.Fatalf("output_index %v reused by %s and %s", oi, prev, it)
		}
		itemTypes[oi] = it
	}
	// 对齐参考实现：reasoning 不建独立 item，故只应有 message 与 function_call 两种。
	if len(itemTypes) != 2 {
		t.Fatalf("expected 2 distinct items (message/function_call), got %v", itemTypes)
	}
	seen := map[string]bool{}
	for _, it := range itemTypes {
		seen[it] = true
	}
	for _, want := range []string{"message", "function_call"} {
		if !seen[want] {
			t.Fatalf("missing item type %q in %v", want, itemTypes)
		}
	}
	if seen["reasoning"] {
		t.Fatal("reasoning must not emit its own output_item")
	}

	var completed map[string]any
	for _, e := range evs {
		if e["type"] == "response.completed" {
			completed, _ = e["response"].(map[string]any)
		}
	}
	if completed == nil {
		t.Fatal("no response.completed")
	}
	if completed["parallel_tool_calls"] != true {
		t.Fatalf("completed should set parallel_tool_calls=true: %v", completed["parallel_tool_calls"])
	}
	outList, _ := completed["output"].([]any)
	if len(outList) == 0 {
		t.Fatal("completed output is empty")
	}
	for _, it := range outList {
		im, _ := it.(map[string]any)
		id, _ := im["id"].(string)
		found := false
		for _, aid := range addedByIndex {
			if aid == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("completed output item id=%s not seen in stream: %v", id, addedByIndex)
		}
	}
}

// TestResponsesStreamUsageNullAtCreated created/in_progress 的 usage 应为 null。
func TestResponsesStreamUsageNullAtCreated(t *testing.T) {
	c := NewResponsesStreamConverter("m")
	out := c.Feed(`{"choices":[{"delta":{"content":"x"}}]}`)
	checked := 0
	for _, e := range parseEvents(t, out) {
		typ, _ := e["type"].(string)
		if typ != "response.created" && typ != "response.in_progress" {
			continue
		}
		resp, _ := e["response"].(map[string]any)
		if resp == nil {
			t.Fatalf("%s missing response object", typ)
		}
		if v, present := resp["usage"]; !present || v != nil {
			t.Fatalf("%s usage should be null, got %v", typ, v)
		}
		checked++
	}
	if checked != 2 {
		t.Fatalf("expected created+in_progress, checked %d", checked)
	}
}

// TestResponsesStreamEveryDataHasType 每条 data 载荷都必须自带 type 字段，
// 不能只靠 event: 行表达类型（客户端按 data 解析）。
func TestResponsesStreamEveryDataHasType(t *testing.T) {
	c := NewResponsesStreamConverter("m")
	var sb strings.Builder
	sb.WriteString(c.Feed(`{"choices":[{"delta":{"content":"hi"}}]}`))
	sb.WriteString(c.Finish())
	for _, e := range parseEvents(t, sb.String()) {
		if _, ok := e["type"].(string); !ok {
			t.Fatalf("event payload missing type: %v", e)
		}
	}
}
