package upstream

import (
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestFetchGlobalModelsPrefersCLICatalog 锁定 global 侧目录合并语义：
// /v3/config **CLI UA** 形态的真名目录为主路，企业端点家族（/v2）只补缺。
//
// 背景（实测）：global 的 /v3/config 目录内容随 UA 分叉——
//   - IDE UA（codeBuddyIDEUA）：只给 13 条功能代号（default-model /
//     auto-chat / o4-mini / enhance-1.0 / nes-* / codewise-*），真名型号整体缺失；
//   - CLI UA（codeBuddyCLIUA）：给 22 条真名目录（deepseek-v4.1-flash /
//     gpt-6-astra / glm-5.3 / kimi-k2.8-preview / gpt-5.6-* 等），字段齐全。
//
// 企业端点家族 /v2 给 18 条，与 CLI 目录差异仅 gpt-5.3-codex 为家族独有，
// 故主路取 CLI 目录、家族补其缺失 id。若沿用「IDE UA 为主」或「家族为主」，
// 代号会占据主位、真名型号缺失，面板因此只见代号。
func TestFetchGlobalModelsPrefersCLICatalog(t *testing.T) {
	var sawCLIUA bool
	c := testClient(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v3/config"):
			// 主路必须用 CLI UA——IDE UA 在此端点只下发功能代号。
			if ua := r.Header.Get("User-Agent"); ua == codeBuddyCLIUA {
				sawCLIUA = true
			} else {
				t.Errorf("/v3/config User-Agent = %q, want %q", ua, codeBuddyCLIUA)
			}
			return jsonResp(200, `{"code":0,"data":{"models":[
				{"id":"deepseek-v4.1-flash","name":"Deepseek-V4.1-Flash","maxInputTokens":1000000,"maxOutputTokens":128000,
				 "reasoning":{"defaultEffort":"high","summary":"auto"},"credits":"x0.00","supportsReasoning":true},
				{"id":"gpt-6-astra","name":"GPT-6-Astra","maxInputTokens":1000000,"maxOutputTokens":128000,
				 "reasoning":{"defaultEffort":"high","supportedEfforts":["low","high","max"]}},
				{"id":"glm-5.3","name":"GLM-5.3","maxInputTokens":1000000,"maxOutputTokens":48000,
				 "reasoning":{"defaultEffort":"high","supportedEfforts":["low","high","max"]}}
			]}}`), nil
		case strings.HasSuffix(r.URL.Path, "/v2/enterprises/personal/models"):
			// 家族端点只补 CLI 目录缺失的 id（gpt-5.3-codex 家族独有）。
			return jsonResp(200, `{"code":0,"data":{"models":[
				{"id":"gpt-5.3-codex","name":"GPT-5.3-Codex","maxInputTokens":272000,"maxOutputTokens":72000,
				 "reasoning":{"effort":"medium","summary":"auto"}}
			]}}`), nil
		}
		return jsonResp(404, `{}`), nil
	})
	c.ChatBaseGlobal = "https://global.example"
	c.BillingBaseGlobal = "https://global.example"
	c.GlobalEnabled = true

	// Domain 后缀 workbuddy.ai → Realm()=="global"（包外无法直接设私有 realm 字段）。
	a := &auth.Auth{AccessToken: "at", UID: "g1", Domain: "www.workbuddy.ai"}

	infos := c.FetchGlobalModelInfos(a)
	if !sawCLIUA {
		t.Fatal("/v3/config 未按 CLI UA 探测")
	}
	got := map[string]ModelInfo{}
	for _, mi := range infos {
		got[mi.ID] = mi
	}
	// CLI 目录真名全在 + 家族独有条目经补缺进来（不丢）。
	for _, want := range []string{"deepseek-v4.1-flash", "gpt-6-astra", "glm-5.3", "gpt-5.3-codex"} {
		if _, ok := got[want]; !ok {
			t.Errorf("global 目录缺少 %q", want)
		}
	}
	if len(infos) == 0 {
		t.Fatal("global 目录为空")
	}
	// CLI 目录为主路：首位应为 CLI 条目（升序排序后 deepseek-v4.1-flash 在前），
	// 家族补充项（gpt-5.3-codex）在其后。
	if infos[0].ID != "deepseek-v4.1-flash" {
		t.Errorf("列表首位 = %q, want deepseek-v4.1-flash（CLI 目录应为主路）", infos[0].ID)
	}
	if last := infos[len(infos)-1].ID; last != "gpt-5.3-codex" {
		t.Errorf("列表末位 = %q, want gpt-5.3-codex（家族补充项应在主路之后）", last)
	}
	// 主路字段权威：name / credits 取自 CLI 目录条目。
	if mi, ok := got["deepseek-v4.1-flash"]; ok {
		if mi.Name != "Deepseek-V4.1-Flash" {
			t.Errorf("name = %q, want Deepseek-V4.1-Flash", mi.Name)
		}
		if mi.Credits != "x0.00" {
			t.Errorf("credits = %q, want x0.00", mi.Credits)
		}
		if mi.DefaultEffort != "high" {
			t.Errorf("defaultEffort = %q, want high", mi.DefaultEffort)
		}
	}
	// 家族条目的 effort 也应进桶（补缺项的 supportedEfforts 不能丢）。
	if mi, ok := got["gpt-5.3-codex"]; ok && mi.MaxTokens != 72000 {
		t.Errorf("gpt-5.3-codex maxOutputTokens = %d, want 72000", mi.MaxTokens)
	}
}
