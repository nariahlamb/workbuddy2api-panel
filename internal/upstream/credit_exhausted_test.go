package upstream

import (
	"net/http"
	"testing"
)

// TestClassifyCreditExhaustedCode14018 锁定 14018 精确判定。
//
// 实测原文（2026-09-19 日志）：
//
//	{"error":{"data":{"code":14018,"msg":"Credits exhausted. Please visit the link
//	 below to purchase add-on packs and get more credits: https://www.codebuddy.ai/profile/usage "}}}
//
// 该响应带 status=429。修复前落 status==429 层 → ErrSoftRate（10 分钟软冷却），
// 而号是真没额度，冷却到期回来必然再撞，形成循环。
func TestClassifyCreditExhaustedCode14018(t *testing.T) {
	body := `{"error":{"data":{"code":14018,"msg":"Credits exhausted. Please visit the link below to purchase add-on packs and get more credits: https://www.codebuddy.ai/profile/usage ","requestId":"263f1e5639092e"}}}`
	if got := Classify(http.StatusTooManyRequests, body); got != ErrHardCredit {
		t.Fatalf("429+14018 应判 ErrHardCredit（精确码优先于状态码），got=%v", got)
	}
}

// TestClassifyCreditExhaustedCode14018Variants code 字段 JSON 空格/引号容差
// （与 IsModelRateLimit/IsModelBlocked 同口径）。
func TestClassifyCreditExhaustedCode14018Variants(t *testing.T) {
	cases := []string{
		`{"code":14018}`,
		`{"code": 14018}`,
		`{"code":"14018"}`,
		`{"code": "14018"}`,
	}
	for _, b := range cases {
		if got := Classify(http.StatusTooManyRequests, b); got != ErrHardCredit {
			t.Errorf("Classify(429, %q)=%v want ErrHardCredit", b, got)
		}
	}
}

// TestClassifyPlain429StillSoftRate 回归守卫：**不带动**既有 429 语义。
// 普通 429（无 14018）仍必须走 ErrSoftRate——429 body 高频携带 "quota exceeded"/
// "额度不足" 这类跨计费与限流两界的措辞，若被硬冷却会白扔号约 12h。
func TestClassifyPlain429StillSoftRate(t *testing.T) {
	cases := []string{
		``,
		`{"code":1,"msg":"too many requests"}`,
		`{"code":11140,"msg":"The model provider is rate-limiting requests."}`,
		`{"code":6004,"msg":"将在 2026-09-11 18:33:27 UTC+8 重置"}`,
		`{"code":1,"msg":"quota exceeded"}`, // 429 + quota 措辞：仍按限流（状态码更权威）
		`{"code":1,"msg":"额度不足"}`,           // 同上，中文计费措辞
	}
	for _, b := range cases {
		if got := Classify(http.StatusTooManyRequests, b); got != ErrSoftRate {
			t.Errorf("Classify(429, %q)=%v want ErrSoftRate（429 层语义不得被 14018 改动波及）", b, got)
		}
	}
}

// TestIsCreditExhausted 谓词本身的边界。
func TestIsCreditExhausted(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"code":14018}`, true},
		{`{"code": 14018}`, true},
		{`{"code":"14018"}`, true},
		{`{"code":1401}`, false},
		{`{"code":140180}`, false}, // 不做朴素子串命中的守卫：140180 不应命中
		{``, false},
		{`{"code":1,"msg":"ok"}`, false},
	}
	for _, c := range cases {
		if got := IsCreditExhausted(c.body); got != c.want {
			t.Errorf("IsCreditExhausted(%q)=%v want %v", c.body, got, c.want)
		}
	}
}
