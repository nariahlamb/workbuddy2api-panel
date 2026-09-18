package upstream

import (
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestFetchGlobalModelsPrefersFamilyRealNames 锁定 global 侧目录合并语义：
// 企业端点家族（/v2 优先）的真名目录为主，/v3/config 的功能代号只补缺。
//
// 背景（实测）：global 的 /v3/config 下发 14 条功能代号（default-model、
// fast-model、balanced-model、primary-model、deep-model、auto-chat、
// enhance-1.0、nes-1.2、Tencent-Cloud.genie-ide …），真名目录在企业端点
// 家族 /v2/enterprises/personal/models——gpt-5.6-sol / gpt-5.6-terra /
// gemini-3.5-flash / deepseek-v4.1-flash / glm-5.3 / kimi-k3 等，与 APK
// 本地缓存 models:v3:international 的 24 条一致。沿用「v3 为主」会让代号
// 占据主位，面板因此只见代号。此处与 CN 侧镜像。
func TestFetchGlobalModelsPrefersFamilyRealNames(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/enterprises/personal/models"):
			return jsonResp(200, `{"code":0,"data":{"models":[
				{"id":"gpt-5.6-sol","name":"GPT-5.6-Sol","maxInputTokens":1000000,"maxOutputTokens":128000},
				{"id":"deepseek-v4.1-flash","name":"DeepSeek-V4.1-Flash","maxInputTokens":96000,"maxOutputTokens":32000},
				{"id":"gemini-3.5-flash","name":"Gemini-3.5-Flash","maxInputTokens":1000000,"maxOutputTokens":65536}
			],"agents":[{"name":"cli","models":["gpt-5.6-sol","deepseek-v4.1-flash","gemini-3.5-flash"]}]}}`), nil
		case strings.HasSuffix(r.URL.Path, "/v3/config"):
			return jsonResp(200, `{"code":0,"data":{"models":[
				{"id":"deep-model","name":"Deep","maxInputTokens":176000,"maxOutputTokens":24000},
				{"id":"fast-model","name":"Fast","maxInputTokens":200000,"maxOutputTokens":32000}
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
	got := map[string]ModelInfo{}
	for _, mi := range infos {
		got[mi.ID] = mi
	}
	for _, want := range []string{"gpt-5.6-sol", "deepseek-v4.1-flash", "gemini-3.5-flash"} {
		if _, ok := got[want]; !ok {
			t.Errorf("global 真名 %q 应出现在目录中", want)
		}
	}
	// 真名必须排在代号之前：家族为主、v3 补缺。旧「v3 为主」实现会把
	// deep-model / fast-model 顶到列表前面，客户端默认选中代号模型。
	if len(infos) == 0 {
		t.Fatal("global 目录为空")
	}
	if infos[0].ID == "deep-model" || infos[0].ID == "fast-model" {
		t.Errorf("列表首位是功能代号 %q，真名目录应为主路", infos[0].ID)
	}
	if mi, ok := got["deepseek-v4.1-flash"]; ok && mi.Name != "DeepSeek-V4.1-Flash" {
		t.Errorf("name = %q, want DeepSeek-V4.1-Flash", mi.Name)
	}
}
