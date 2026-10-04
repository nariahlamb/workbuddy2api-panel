package panel

import (
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppJSSyntax app.js 必须能通过 JS 解析器语法校验。
//
// 为什么需要：app.js 是 go:embed 进二进制的静态资源，Go 编译器不检查其内容——
// 一次对象字面量键名未加引号（Model_chat_GLM5.2 被解析成属性访问 + 数字字面量）
// 就让整个面板白屏，而所有 Go 测试依然全绿。此测试把语法校验前移到 CI。
// 无 node 环境时跳过（不阻塞无 Node 的构建机）。
func TestAppJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping JS syntax check")
	}
	path, err := filepath.Abs("app.js")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "--check", path).CombinedOutput()
	if err != nil {
		t.Fatalf("app.js syntax error:\n%s", out)
	}
}

// TestIndexHTMLNoInlineScript index.html 不得含内联 <script> 块：
// 严格 CSP（script-src 'self'）会拦截内联脚本，页面将完全不可用。
// 外链形式 <script src="..."> 允许。
func TestIndexHTMLNoInlineScript(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	rest := body
	for {
		idx := strings.Index(rest, "<script")
		if idx < 0 {
			break
		}
		rest = rest[idx:]
		end := strings.Index(rest, ">")
		if end < 0 {
			break
		}
		tag := rest[:end+1]
		if !strings.Contains(tag, "src=") {
			t.Fatalf("index.html contains inline <script> (blocked by CSP): %s", tag)
		}
		rest = rest[end:]
	}
}

// TestModelsViewDualLists 「模型与档位」必须是国内/国际两个独立列表：
// 两个 tbody（#mdBodyCN / #mdBodyGL）与两个域小结节点必须都在，否则前端会在
// 渲染时分域失败（单表时代只能显示国内 16 个模型——本次修复的诉求之一）。
func TestModelsViewDualLists(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	for _, id := range []string{`id="mdBodyCN"`, `id="mdBodyGL"`, `id="mdCNSummary"`, `id="mdGLSummary"`} {
		if !strings.Contains(body, id) {
			t.Fatalf("index.html missing %s (双域模型列表未生效)", id)
		}
	}
	if strings.Contains(body, `id="mdBody"`) {
		t.Fatal("index.html still has legacy single-table #mdBody")
	}
}

// TestConfigViewRealmKeyFields 配置页必须提供国内/国际两把专用密钥输入框，
// 否则「显式设置分 key」在 UI 上无处落地（后端已支持但面板无入口）。
func TestConfigViewRealmKeyFields(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	for _, id := range []string{`name="api_keys_cn"`, `name="api_keys_global"`, `id="btnEyeCN"`, `id="btnEyeGL"`} {
		if !strings.Contains(body, id) {
			t.Fatalf("index.html missing %s (分 key 表单项未生效)", id)
		}
	}
}
