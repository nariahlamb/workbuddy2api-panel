package livecfg

import "testing"

// realm 专用密钥必须真正参与鉴权判定：MatchKey 对 cn/global 两把 key 分别
// 返回对应 realm，全通配 key 返回空 realm（不限制），未命中返回 ok=false。
// 这是「国内/国外分 key」功能的认证层契约——此前 main 未把配置里的
// api_keys 注入 Snapshot，导致该功能在真实部署下完全失效（本次修复点）。
func TestMatchKeyRealmWire(t *testing.T) {
	s := Snapshot{APIKey: "wild", APIKeyCN: "key-cn", APIKeyGlobal: "key-gl"}

	cases := []struct {
		tok     string
		realm   string
		ok      bool
		comment string
	}{
		{"wild", "", true, "全通配：不受 realm 限制"},
		{"key-cn", "cn", true, "国内专用 key → cn"},
		{"key-gl", "global", true, "国际专用 key → global"},
		{"nope", "", false, "未命中任何 key"},
	}
	for _, c := range cases {
		realm, ok := s.MatchKey(c.tok)
		if ok != c.ok || realm != c.realm {
			t.Fatalf("%s: MatchKey(%q)=(%q,%v) want (%q,%v)", c.comment, c.tok, realm, ok, c.realm, c.ok)
		}
	}
}

// 仅配 realm key（无全通配）时也必须生效；三把全空 = 不鉴权，恒放行。
func TestMatchKeyRealmOnlyAndDisabled(t *testing.T) {
	only := Snapshot{APIKeyCN: "c", APIKeyGlobal: "g"}
	if realm, ok := only.MatchKey("c"); !ok || realm != "cn" {
		t.Fatalf("cn-only: (%q,%v)", realm, ok)
	}
	if realm, ok := only.MatchKey("g"); !ok || realm != "global" {
		t.Fatalf("global-only: (%q,%v)", realm, ok)
	}
	if _, ok := only.MatchKey("wrong"); ok {
		t.Fatal("wrong key must be rejected")
	}

	off := Snapshot{}
	if realm, ok := off.MatchKey("anything"); !ok || realm != "" {
		t.Fatalf("no-key mode must allow all: (%q,%v)", realm, ok)
	}
}
