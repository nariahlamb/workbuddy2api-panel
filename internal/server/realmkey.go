// realmkey.go realm 专用密钥的鉴权与范围收窄。
//
// 场景：把"国内一套 key、国外一套 key"分发给不同客户端。持 api_keys.cn 的
// 客户端只看得到 cn: 模型、也只能用 cn 前缀调补全；api_keys.global 同理。
// api_key 保持全通配（面板登录与既有客户端零影响）。
//
// 实现要点：鉴权在 withAuth 里完成一次，命中的 realm 经 context 传给后续
// handler，避免在每个端点重复解析密钥。全通配命中时 realm 为空串，代表
// "不限制"——这条路径就是改造前的行为，保证向后兼容。
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// ctxKeyRealm context 键：本请求被授权的 realm（"" = 全通配，不限制）。
type ctxKeyRealm struct{}

// realmFrom 读取请求被授权的 realm。ok=false 表示上下文缺失（不该发生，
// 说明端点没走 withAuth），调用方按"不限制"处理以免误伤。
func realmFrom(r *http.Request) (string, bool) {
	v, ok := r.Context().Value(ctxKeyRealm{}).(string)
	return v, ok
}

// withRealm 用授权 realm 标注请求上下文。
func withRealm(r *http.Request, realm string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyRealm{}, realm))
}

// realmAllows 判定被授权的 scope 是否允许访问 target realm。
//
// scope 为空 = 全通配，恒允许（旧配置单 key 行为不变）。否则必须精确匹配：
// cn key 不能碰 global 资源，反之亦然。
func realmAllows(scope, target string) bool {
	if scope == "" {
		return true
	}
	return scope == target
}

// modelRealm 从请求体里取 model 字段并解析其 realm 前缀。
// 读取失败（body 非 JSON / 无 model）时返回 ("", false)，调用方据此跳过
// realm 校验——真正的 body 校验由 chatCompletions 自己给出更准确的错误。
func modelRealm(body []byte) (string, bool) {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.Model == "" {
		return "", false
	}
	realm, _ := resolveModel(probe.Model)
	return realm, true
}

// readBodyPeek 读取并复原 body，供需要在读体前做 realm 判定的路径使用。
// 上限与 chatCompletions 一致，避免这里成为绕过 body 限制的后门。
func readBodyPeek(r *http.Request, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	// 复原 body 供下游正常读取（ContentLength 保持不变，下游 LimitReader 仍生效）。
	r.Body = io.NopCloser(&bytesReader{data: body})
	return body, nil
}

// bytesReader 极简可复用 reader（避免为一次复原引入 bytes 包的额外分配路径）。
type bytesReader struct {
	data []byte
	pos  int
}

func (b *bytesReader) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}

// filterModelsByRealm 按被授权的 realm 过滤 /v1/models 输出。
//
// scope 为空（全通配）时原样返回。否则只保留 "cn:" / "global:" 前缀匹配的
// 条目——客户端因此拿不到另一个域的模型名，也就不会选中它。
func filterModelsByRealm(list []map[string]any, scope string) []map[string]any {
	if scope == "" || len(list) == 0 {
		return list
	}
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		id, _ := m["id"].(string)
		realm, _ := resolveModel(id)
		if realm == scope {
			out = append(out, m)
		}
	}
	return out
}
