package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// responsesSSE 是带思考与工具调用的上游 chat SSE fixture。
const responsesSSE = "data: {\"id\":\"c1\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你好\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"世界\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n" +
	"data: [DONE]\n\n"

// TestResponsesEndpointNonStream 非流式：请求体被翻译、响应被翻回 Response 对象。
func TestResponsesEndpointNonStream(t *testing.T) {
	var gotPath, gotBody string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, responsesSSE, true
	})
	// 记录上游收到的路径与 body，确认走的是 chat 端点、且 model/messages 已翻译。
	up.HTTP.Transport = capturingTransport(t, &gotPath, &gotBody, responsesSSE)

	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	reqBody := `{"model":"glm-5.2","input":"hi","instructions":"be nice"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(reqBody)))

	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not json: %v\n%s", err, rec.Body.String())
	}
	if resp["object"] != "response" {
		t.Fatalf("object should be response: %v", resp["object"])
	}
	if resp["output_text"] != "你好世界" {
		t.Fatalf("output_text mismatch: %v", resp["output_text"])
	}
	if !strings.Contains(gotPath, "/chat/completions") {
		t.Fatalf("upstream should be called on chat completions, got %s", gotPath)
	}
	// 上游必须收到翻译后的 chat 请求体（messages + system）
	if !strings.Contains(gotBody, `"messages"`) || !strings.Contains(gotBody, "be nice") {
		t.Fatalf("translated body not forwarded: %s", gotBody)
	}
}

// capturingTransport 返回一个记录请求路径与 body、并回放给定 SSE 的 transport。
func capturingTransport(t *testing.T, pathOut, bodyOut *string, sse string) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*pathOut = r.URL.Path
		if r.Body != nil {
			buf := make([]byte, 1<<16)
			n, _ := r.Body.Read(buf)
			*bodyOut = string(buf[:n])
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       &readCloser{strings.NewReader(sse)},
		}, nil
	})
}

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }

// TestResponsesEndpointStream 流式：返回 Responses 事件流，含正确的事件类型与完整文本。
func TestResponsesEndpointStream(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, responsesSSE, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses",
		strings.NewReader(`{"model":"glm-5.2","input":"hi","stream":true}`)))

	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type should be SSE, got %q", ct)
	}
	out := rec.Body.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.completed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "你好") || !strings.Contains(out, "世界") {
		t.Fatalf("stream missing text deltas:\n%s", out)
	}
}

// TestResponsesEndpointInvalidBody 畸形 JSON → 400，不触上游。
func TestResponsesEndpointInvalidBody(t *testing.T) {
	called := false
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		called = true
		return 200, responsesSSE, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{bad json`)))

	if rec.Code != 400 {
		t.Fatalf("malformed body should be 400, got %d", rec.Code)
	}
	if called {
		t.Fatal("malformed request must not hit upstream")
	}
}

// TestResponsesEndpointMissingInput 无 input/instructions → 400。
func TestResponsesEndpointMissingInput(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, responsesSSE, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m"}`)))

	if rec.Code != 400 {
		t.Fatalf("missing input should be 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
