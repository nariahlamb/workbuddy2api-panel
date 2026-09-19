package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestChatWafEntryFailFastSkipsUpstream 锁定入口 fail-fast：本 realm 闸门激活时，
// 请求**不打上游**即被拒（503 + Retry-After）。依据是 2026-09-19 实测——闸门
// 01:33:21 激活后，01:33:24/30/39 三个新请求仍各撞一次上游才被拦，是纯浪费且继续
// 向已判定为风控目标的出口 IP 加量。
func TestChatWafEntryFailFastSkipsUpstream(t *testing.T) {
	var calls int
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return 200, sseOK, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})

	// 手工把 global 域闸门打成激活态（两个不同号命中）。
	h.wafIP.note("global", "a1")
	h.wafIP.note("global", "a2")

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"global:deepseek-v4.1-flash","messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if calls != 0 {
		t.Fatalf("入口 fail-fast 必须不打上游，实际 calls=%d", calls)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503 body=%s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("应回 Retry-After 头告知等待时长")
	}
	if !strings.Contains(rec.Body.String(), "waf_ip_blocked") {
		t.Fatalf("body 应带 waf_ip_blocked，got=%s", rec.Body)
	}
}

// TestChatWafEntryFailFastRealmScoped 入口拒绝按 realm 分档：global 被封不影响 CN
// 请求正常打到上游（实测两域不对称）。
func TestChatWafEntryFailFastRealmScoped(t *testing.T) {
	var calls int
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return 200, sseOK, true
	})
	p := testPoolWith(&auth.Auth{UID: "c1", AccessToken: "at-cn", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})

	h.wafIP.note("global", "a1")
	h.wafIP.note("global", "a2")

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4.1-flash","messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if calls == 0 {
		t.Fatal("CN 请求不应被 global 的闸门拦住（必须打到上游）")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 body=%s", rec.Code, rec.Body)
	}
}

// TestChatWafRotateStopsOnIPLevelBlock 轮转内命中 WAF 后，闸门激活即终止轮转
// （不再换号把请求放大 MaxRotate 倍打同一出口 IP）。
func TestChatWafRotateStopsOnIPLevelBlock(t *testing.T) {
	const wafPage = `<!DOCTYPE html><html><head><title>WAF Block Page</title></head><body>x</body></html>`
	var calls int
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return http.StatusForbidden, wafPage, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u3", AccessToken: "at3", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// 两个不同号命中即激活闸门 → 第二个号后立即 break（不试满三个号）。
	if calls != 2 {
		t.Fatalf("闸门激活后应停止轮转，calls=%d want 2", calls)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503 body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "waf_ip_blocked") {
		t.Fatalf("末端应给 IP 级拦截措辞，got=%s", rec.Body)
	}
}

// TestChatAllExhaustedGivesActionableError 域内号全因余额耗尽时，末端必须给可操作
// 措辞（充值/等签到）而非笼统的 "temporarily unavailable"。选号层排除耗尽号后，
// 修复前"选中→撞 402→透传原文"的间接信息必须显式补齐，否则是可见的信息回退。
func TestChatAllExhaustedGivesActionableError(t *testing.T) {
	var calls int
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return 200, sseOK, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	p.SetCredits("u1", 0, 350) // 已确认耗尽
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if calls != 0 {
		t.Fatalf("耗尽号不应被打上游，calls=%d", calls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "insufficient_credits") {
		t.Fatalf("应给 insufficient_credits 而非笼统 no_healthy_account，got=%s", body)
	}
	if strings.Contains(body, "no_healthy_account") {
		t.Fatalf("不应落到笼统措辞，got=%s", body)
	}
}
