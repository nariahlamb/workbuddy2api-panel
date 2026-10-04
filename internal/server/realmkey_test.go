package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
)

// TestRealmAllows 范围判定：空 scope 是全通配（旧单 key 行为），否则精确匹配。
func TestRealmAllows(t *testing.T) {
	cases := []struct {
		scope, target string
		want          bool
	}{
		{"", "cn", true},
		{"", "global", true},
		{"cn", "cn", true},
		{"cn", "global", false},
		{"global", "global", true},
		{"global", "cn", false},
	}
	for _, c := range cases {
		if got := realmAllows(c.scope, c.target); got != c.want {
			t.Errorf("realmAllows(%q,%q)=%v want %v", c.scope, c.target, got, c.want)
		}
	}
}

// TestFilterModelsByRealm cn key 只看到 cn: 条目，global key 只看到 global:。
func TestFilterModelsByRealm(t *testing.T) {
	list := []map[string]any{
		{"id": "cn:deepseek-v4.1-flash"},
		{"id": "cn:glm-5.3"},
		{"id": "global:gpt-5.6-sol"},
		{"id": "global:deepseek-v4.1-flash"},
	}
	ids := func(in []map[string]any) []string {
		out := make([]string, 0, len(in))
		for _, m := range in {
			out = append(out, m["id"].(string))
		}
		return out
	}
	if got := ids(filterModelsByRealm(list, "")); len(got) != 4 {
		t.Errorf("全通配应返回全部 4 条，得到 %v", got)
	}
	got := ids(filterModelsByRealm(list, "cn"))
	if len(got) != 2 || got[0] != "cn:deepseek-v4.1-flash" || got[1] != "cn:glm-5.3" {
		t.Errorf("cn 过滤结果 = %v", got)
	}
	got = ids(filterModelsByRealm(list, "global"))
	if len(got) != 2 || got[0] != "global:gpt-5.6-sol" {
		t.Errorf("global 过滤结果 = %v", got)
	}
}

// TestMatchKeyWiring 密钥 → realm 的映射（含全通配向后兼容）。
func TestMatchKeyWiring(t *testing.T) {
	snap := livecfg.Snapshot{APIKey: "sk-all", APIKeyCN: "sk-cn", APIKeyGlobal: "sk-gl"}
	cases := []struct {
		tok       string
		wantRealm string
		wantOK    bool
	}{
		{"sk-all", "", true},
		{"sk-cn", "cn", true},
		{"sk-gl", "global", true},
		{"sk-wrong", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		realm, ok := snap.MatchKey(c.tok)
		if ok != c.wantOK || realm != c.wantRealm {
			t.Errorf("MatchKey(%q) = (%q,%v), want (%q,%v)", c.tok, realm, ok, c.wantRealm, c.wantOK)
		}
	}
	legacy := livecfg.Snapshot{APIKey: "sk-all"}
	if realm, ok := legacy.MatchKey("sk-all"); !ok || realm != "" {
		t.Errorf("旧配置单 key 应命中且不受限，得到 (%q,%v)", realm, ok)
	}
	if _, ok := legacy.MatchKey("sk-cn"); ok {
		t.Error("旧配置下未配置的 key 必须被拒绝")
	}
	open := livecfg.Snapshot{}
	if _, ok := open.MatchKey("anything"); !ok {
		t.Error("未启用鉴权时应恒通过")
	}
}

// TestRealmKeyEndToEnd 端到端：cn key 调 global 模型得 403，全通配不受限。
func TestRealmKeyEndToEnd(t *testing.T) {
	h := NewHandler(Config{
		Pool:     testPoolWith(),
		Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, "", true }),
		APIKey:   "sk-all",
	})
	h.cfg.Live = livecfg.New(livecfg.Snapshot{
		APIKey: "sk-all", APIKeyCN: "sk-cn", APIKeyGlobal: "sk-gl",
	})

	call := func(key, model string) *httptest.ResponseRecorder {
		body := `{"model":"` + model + `","messages":[]}`
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		// 经 withAuth 走一遍，确保 realm 真的从密钥注入 context。
		rec := httptest.NewRecorder()
		h.withAuth(h.chatCompletions)(rec, req)
		return rec
	}

	rec := call("sk-cn", "global:gpt-5.6-sol")
	if rec.Code != 403 {
		t.Fatalf("cn key 调 global 模型应 403，得到 %d body=%s", rec.Code, rec.Body.String())
	}
	var errResp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil || errResp.Error.Code != "realm_not_allowed" {
		t.Errorf("错误码应为 realm_not_allowed，得到 %s", rec.Body.String())
	}

	if rec := call("sk-gl", "cn:deepseek-v4.1-flash"); rec.Code != 403 {
		t.Fatalf("global key 调 cn 模型应 403，得到 %d", rec.Code)
	}

	if rec := call("sk-all", "global:gpt-5.6-sol"); rec.Code == 403 || rec.Code == 401 {
		t.Errorf("全通配 key 不应被拒，得到 %d body=%s", rec.Code, rec.Body.String())
	}

	if rec := call("sk-bad", "cn:deepseek-v4.1-flash"); rec.Code != 401 {
		t.Errorf("错误密钥应 401，得到 %d", rec.Code)
	}
}
