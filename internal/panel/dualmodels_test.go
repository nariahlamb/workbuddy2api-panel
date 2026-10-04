package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// realm 标记契约：modelEntry 必须把 realm 与 _id（带域前缀）写入条目，
// 面板前端据此把结果拆成国内 / 国际两个列表；id 保持裸名（无前缀）。
func TestModelEntryRealmTagging(t *testing.T) {
	p := New(Config{Version: "test"})

	cn := p.modelEntry(upstream.ModelInfo{ID: "glm-5.2"}, "cn", "cn:", nil, "")
	if cn["realm"] != "cn" {
		t.Fatalf("realm=%v want cn", cn["realm"])
	}
	if cn["_id"] != "cn:glm-5.2" {
		t.Fatalf("_id=%v want cn:glm-5.2", cn["_id"])
	}
	if cn["id"] != "glm-5.2" {
		t.Fatalf("id=%v want bare glm-5.2", cn["id"])
	}

	// global 域同理，前缀为 global:
	g := p.modelEntry(upstream.ModelInfo{ID: "gpt-5.3-codex"}, "global", "global:", nil, "")
	if g["realm"] != "global" || g["_id"] != "global:gpt-5.3-codex" {
		t.Fatalf("global entry=%v", g)
	}
	if g["id"] != "gpt-5.3-codex" {
		t.Fatalf("global id=%v want bare", g["id"])
	}
}

// 双域端点契约：未配置池时端点必须给出明确错误码而不是 panic。
// 真实双域内容（cn/global 两段）由真机集成验证覆盖，此处只锁「先建后判」的安全边界。
func TestModelsEndpointNoPoolSafe(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key"})
	req := httptest.NewRequest("GET", "/panel/api/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusOK {
		t.Fatalf("unexpected code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusOK {
		var got struct {
			OK      bool             `json:"ok"`
			Models  []map[string]any `json:"models"`
			Global  []map[string]any `json:"global"`
			CNCount int              `json:"cn_count"`
			GLCount int              `json:"global_count"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
		if got.CNCount != len(got.Models) || got.GLCount != len(got.Global) {
			t.Fatalf("counts mismatch: %+v", got)
		}
	}
}
