package upstream

import (
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestFetchModelsCNPrefersV3RealNames 锁定 CN 侧目录合并语义：
// /v3/config 的真名目录是唯一权威源，企业端点的功能代号不得混入。
//
// 背景（实测）：CN 的 /v3/config 下发 30 条真名（deepseek-v4.1-flash、
// glm-5.3、kimi-k3-1 …，含 name/credits/输入输出上限），企业端点
// /console/enterprises/personal/models 只下发 8 个功能代号（deep-model、
// fast-model、balanced-model、primary-model、auto-chat、enhance-1.0、
// o4-mini、default-model）。两组 id 不同名，旧实现 mergeModelInfos(v3,
// enterprise) 会把代号当作「v3 缺失项」补进列表，面板因此出现
// cn:deep-model 与 cn:deepseek-v4.1-flash 并列的混杂结果。
func TestFetchModelsCNPrefersV3RealNames(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v3/config"):
			return jsonResp(200, `{"code":0,"data":{"models":[
				{"id":"deepseek-v4.1-flash","name":"Deepseek-V4.1-Flash","maxInputTokens":1000000,"maxOutputTokens":131072,"vendor":"f"},
				{"id":"glm-5.3","name":"GLM-5.3","maxInputTokens":1000000,"maxOutputTokens":64000,"vendor":"e"},
				{"id":"kimi-k3-1","name":"Kimi-K3","maxInputTokens":1000000,"maxOutputTokens":32000,"vendor":"f"}
			]}}`), nil
		case strings.HasSuffix(r.URL.Path, "/console/enterprises/personal/models"):
			return jsonResp(200, `{"code":0,"data":{"models":[
				{"id":"deep-model","name":"Deep","maxInputTokens":176000,"maxOutputTokens":24000},
				{"id":"fast-model","name":"Fast","maxInputTokens":200000,"maxOutputTokens":32000},
				{"id":"default-model","name":"Auto","maxInputTokens":176000,"maxOutputTokens":24000}
			],"agents":[{"name":"cli","models":["deep-model","fast-model","default-model"]}]}}`), nil
		}
		return jsonResp(404, `{}`), nil
	})

	a := &auth.Auth{AccessToken: "at", UID: "u1", EnterpriseID: "e1"}
	infos, err := c.FetchModels(a)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	got := map[string]ModelInfo{}
	for _, mi := range infos {
		got[mi.ID] = mi
	}
	for _, want := range []string{"deepseek-v4.1-flash", "glm-5.3", "kimi-k3-1"} {
		if _, ok := got[want]; !ok {
			t.Errorf("真名 %q 应出现在 CN 目录中", want)
		}
	}
	for _, bad := range []string{"deep-model", "fast-model", "default-model"} {
		if _, ok := got[bad]; ok {
			t.Errorf("功能代号 %q 不得混入 CN 真名目录（v3 成功即只用 v3）", bad)
		}
	}
	if mi, ok := got["deepseek-v4.1-flash"]; ok {
		if mi.Name != "Deepseek-V4.1-Flash" {
			t.Errorf("name = %q, want Deepseek-V4.1-Flash", mi.Name)
		}
		if mi.MaxTokens != 131072 {
			t.Errorf("maxOutputTokens = %d, want 131072", mi.MaxTokens)
		}
	}
}
