package server

import (
	"io"
	"log"
	"net/http"

	"github.com/linguo2625469/workbuddy2api-panel/internal/adapter"
)

// responses POST /v1/responses 入口（OpenAI Responses 协议）。
//
// 实现方式：把 Responses 请求体翻译为 Chat Completions 请求体，再复用
// chatCompletionsBody 的完整执行链（选号 / 轮转 / 粘性 / 降级 / 错误策略 /
// 用量记账），成功路径由 responsesSink 把上游 Chat SSE 翻译回 Responses 事件流
// 或 Response 对象。这样两个协议共享同一套账号治理逻辑，不会出现行为漂移。
func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
	limit := h.maxBodyBytes.Load()
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	if int64(len(body)) > limit {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_body_too_large",
			"请求体超过上限，请压缩图片或调大 server.max_body_mb 后重试")
		return
	}

	chatBody, stream, err := adapter.ResponsesToChat(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// 把翻译后的 chat body 装回请求，复用 chatCompletionsBody 的读体逻辑。
	// 用 replaceBody 而非直接赋值 r.Body：chatCompletionsBody 会重新 LimitReader 读取。
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(&bytesReader{data: chatBody})
	// stream 标记以 Responses 侧为准：chat body 里不含 stream 字段（Responses 的
	// 流式语义由本端点的事件流承担，非流式则由 sink 聚合后一次性写出）。
	if stream {
		r2.Header.Set("X-WB2API-Responses-Stream", "1")
	}

	model := adapter.ResponsesModel(body)
	sink := &responsesSink{model: model, stream: stream}
	h.chatCompletionsBody(w, r2, sink)
}

// responsesSink 把上游 Chat Completions 字节流翻译为 Responses 协议输出。
type responsesSink struct {
	model  string
	stream bool
}

// ClientStreams 报告客户端是否要求流式（Responses 的 stream 字段）。
func (s *responsesSink) ClientStreams() bool { return s.stream }

// Stream 流式：逐帧读取上游 SSE，翻译为 Responses 事件流并 flush。
func (s *responsesSink) Stream(w http.ResponseWriter, rc io.Reader) error {
	fl, _ := w.(http.Flusher)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	conv := adapter.NewResponsesStreamConverter(s.model)
	// done 标记：收到上游 [DONE] 并下发 response.completed 后置位。
	// 用于区分「结束前的真上游故障」与「结束后的客户端断连回声」。
	done := false

	// 复用 upstream 的 SSE 帧读取器语义：按行读，取 "data: " 载荷。
	// 这里自行解析而不复用 upstream.StreamHint，因为输出形态完全不同
	// （Responses 需要事件级翻译，不是帧透传）。
	br := newSSEReader(rc)
	for {
		payload, err := br.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// 上游读取错误：已开流无法改状态码。
			//
			// 若已经下发过 response.completed，说明本轮协议层已完整结束，
			// 客户端拿到结果后会自行关闭连接——此时上游读到的
			// "context canceled" 是客户端断开的回声，不是网关故障。
			// 这种收尾后的读取错误必须静默（既不记 error 日志，也不再补发
			// error 事件），否则会在 completed 之后多出一个 error 帧，
			// 让客户端把一次成功的对话判成失败。
			if !done {
				writeResponsesErrorEvent(w, fl, err)
				return err
			}
			return nil
		}
		var out string
		if payload == "[DONE]" {
			// 上游显式结束：本次翻译收尾，且必须立即停止读取。
			// 继续读会让「结束后」的 socket 状态（连接被对端回收/取消）
			// 变成一个假的上游错误。
			out = conv.Finish()
			done = true
		} else {
			out = conv.Feed(payload)
		}
		if out != "" {
			if _, werr := io.WriteString(w, out); werr != nil {
				return werr
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if done {
			break
		}
	}
	// 收尾事件（上游未发 [DONE] 就已 EOF 的情况）。
	if out := conv.Finish(); out != "" {
		io.WriteString(w, out)
	}
	if fl != nil {
		fl.Flush()
	}
	return nil
}

// Aggregate 非流式：聚合上游 SSE 为完整 chat 响应，翻译为 Response 对象一次性写出。
func (s *responsesSink) Aggregate(w http.ResponseWriter, rc io.Reader) error {
	chat, err := aggregateChat(rc)
	if err != nil {
		// 尚无任何输出写出 → 可安全回 502。
		writeOpenAIError(w, http.StatusBadGateway, "upstream_parse", err.Error())
		return err
	}
	writeJSON(w, http.StatusOK, adapter.ChatToResponses(chat))
	return nil
}

// writeResponsesErrorEvent 在上游读流出错时下发一个 error 事件（已开流场景）。
func writeResponsesErrorEvent(w http.ResponseWriter, fl http.Flusher, err error) {
	log.Printf("responses stream: upstream read error: %v", err)
	msg := `{"type":"error","error":{"type":"upstream_error","message":"upstream stream interrupted"}}`
	io.WriteString(w, "event: error\ndata: "+msg+"\n\n")
	if fl != nil {
		fl.Flush()
	}
}
