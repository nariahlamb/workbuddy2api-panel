// credx.go 外部凭证的「规范化解析 + 导出形状」：供面板导入/导出用。
//
// 与 auth.Parse 的分工：
//   - auth.Parse 只认网关卡自己写盘的两种形态（嵌套形/扁平形，camelCase），
//     面向「服务启动加载 auths/」这条路径，语义要窄、要稳（改它会波及 refresh 写回）。
//   - 本文件面向「用户从别处搬来的凭证」：多得是历史样本（扁平 snake_case、
//     expiresAt 用毫秒、realm 写 domestic/cn/global、account 里 enterprise_id 与
//     enterpriseId 混用）。这里做单一入口归一，输出仍是 *Auth，落盘一律走
//     SaveAtomic（嵌套形），导出则统一给出两段式 camelCase。
//
// 归一规则（宁可显式拒绝，也不猜出错误身份）：
//   - accessToken 必填（access_token / accessToken 任一）；
//   - realm：显式字段优先，取值 domestic/cn/中国大陆 → cn，global/国际 → global；
//     缺省按 domain 后缀回落（www.workbuddy.ai → global，其余 cn）；
//   - expiresAt：> 1e12 视为毫秒（面板/网页版习惯），否则视为秒；0 表示未知。
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// CredDoc 是导入解析的中间形态：字段全部按「扁平 camelCase」口径归一，
// 便于同时吃嵌套形与扁平形（含 snake_case 历史样本）。
type CredDoc struct {
	Auth    CredFields     `json:"auth"`
	Account CredAccount    `json:"account"`
	Extra   map[string]any `json:"-"`
}

// CredFields 凭证段。
type CredFields struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
	Domain       string `json:"domain"`
	Realm        string `json:"realm"`
}

// CredAccount 账号段。
type CredAccount struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

// pick 从候选键中取第一个非空字符串（大小写/snake 混用的容错取键）。
func pick(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// pickAny 取任意类型原始值（数字类型统一转数字，其它转字符串），用于 expiresAt 之类。
func pickAny(m map[string]json.RawMessage, keys ...string) any {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err == nil && v != nil {
			return v
		}
	}
	return nil
}

// asUnixSeconds 把 expiresAt 的数字/字符串统一成 Unix 秒；
// 超过 1e12 的按毫秒处理（网页版/面板导出的写法）。
func asUnixSeconds(v any) int64 {
	var n float64
	switch t := v.(type) {
	case float64:
		n = t
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		var f float64
		if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
			return 0
		}
		n = f
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0
		}
		n = f
	default:
		return 0
	}
	if n <= 0 {
		return 0
	}
	if n > 1e12 { // 毫秒
		n /= 1000
	}
	return int64(n)
}

// normRealm 把各处写法的 realm 归一到 "cn"/"global"；无法识别返回空（交给 domain 回落）。
func normRealm(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "global", "intl", "international", "overseas":
		return "global"
	case "cn", "domestic", "china", "mainland", "国内", "中国大陆":
		return "cn"
	}
	return ""
}

// normDomain 归一 domain：去掉协议/路径，只留主机名（对齐 auth 文件里的写法）。
func normDomain(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			s = u.Host
		}
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// ParseImported 解析一份外部凭证 JSON（两种示例形态 + snake_case 历史形态都吃）。
//
// 返回的 *Auth 未设置 FilePath（调用方决定落盘文件名），不含任何未识别键。
func ParseImported(raw []byte) (*Auth, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("空文件")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("不是合法 JSON: %w", err)
	}
	// 凭证/账号两段：优先取嵌套段，缺省把顶层当扁平面。
	authPart, acctPart := top, top
	if v, ok := top["auth"]; ok {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(v, &m); err == nil && m != nil {
			authPart = m
		}
	}
	if v, ok := top["account"]; ok {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(v, &m); err == nil && m != nil {
			acctPart = m
		}
	}

	doc := CredDoc{Extra: map[string]any{}}
	doc.Auth.AccessToken = pick(authPart, "accessToken", "access_token", "token")
	doc.Auth.RefreshToken = pick(authPart, "refreshToken", "refresh_token")
	doc.Auth.Domain = normDomain(pick(authPart, "domain"))
	doc.Auth.Realm = normRealm(pick(authPart, "realm"))
	doc.Auth.ExpiresAt = asUnixSeconds(pickAny(authPart, "expiresAt", "expires_at", "expire_at"))

	doc.Account.UID = pick(acctPart, "uid", "user_id", "userId", "sub")
	doc.Account.EnterpriseID = pick(acctPart, "enterpriseId", "enterprise_id")
	doc.Account.Nickname = pick(acctPart, "nickname", "name", "username", "preferred_username", "email")

	// 顶层兜底：扁平历史样本把账号字段直接放在根上。
	if doc.Account.UID == "" {
		doc.Account.UID = pick(top, "uid", "user_id", "userId")
	}
	if doc.Account.Nickname == "" {
		doc.Account.Nickname = pick(top, "nickname", "username", "email")
	}
	if doc.Auth.Domain == "" {
		doc.Auth.Domain = normDomain(pick(top, "domain"))
	}
	if doc.Auth.Realm == "" {
		doc.Auth.Realm = normRealm(pick(top, "realm"))
	}

	if doc.Auth.AccessToken == "" {
		return nil, fmt.Errorf("缺少 accessToken / access_token")
	}
	// UID 是池的去重键：缺失时用 token 哈希构造安全且稳定的标识，
	// 不能把 token 尾部直接拼入文件名（JWT/opaque token 可能含路径分隔符）。
	if doc.Account.UID == "" {
		sum := sha256.Sum256([]byte(doc.Auth.AccessToken))
		doc.Account.UID = "imported-" + hex.EncodeToString(sum[:8])
	}
	if !validUID(doc.Account.UID) {
		return nil, fmt.Errorf("uid 含有不支持的字符或长度超限")
	}

	// realm 缺省按 domain 回落；domain 也缺省时按 UID 形态兜底 cn（老 CN 凭证零回归）。
	if doc.Auth.Realm == "" {
		doc.Auth.Realm = ResolveRealm("", doc.Auth.Domain)
	}
	if doc.Auth.Domain == "" {
		if doc.Auth.Realm == "global" {
			doc.Auth.Domain = "www.workbuddy.ai"
		} else {
			doc.Auth.Domain = "www.codebuddy.cn"
		}
	}

	a := &Auth{
		AccessToken:  doc.Auth.AccessToken,
		RefreshToken: doc.Auth.RefreshToken,
		ExpiresAt:    doc.Auth.ExpiresAt,
		Domain:       doc.Auth.Domain,
		realm:        doc.Auth.Realm,
		UID:          doc.Account.UID,
		EnterpriseID: doc.Account.EnterpriseID,
		Nickname:     doc.Account.Nickname,
	}
	// 顶层 device_token（风控头）原样带上：它在两台设备间搬运时不能丢。
	if dt := pick(top, "device_token", "deviceToken"); dt != "" {
		a.DeviceToken = dt
	}
	return a, nil
}

// validUID 限定池 UID 及基于它生成的 auth 文件名字符集，防路径穿越/文件覆盖。
// WorkBuddy UID 为 UUID/数字串；字母数字、点、下划线、短横足够，且拒绝 . 与 ..。
func validUID(s string) bool {
	if len(s) == 0 || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// ExportShape 返回导出用的两段式 camelCase 文档（与插件 OAuth 输出同形，
// 与 SaveAtomic 落盘形状一致，便于再导入）。
//
// 含完整 token——调用方必须自行决定落点与权限（面板导出写 /sdcard 前已 chmod）。
func (a *Auth) ExportShape() map[string]any {
	// RefreshToken 会在同一把锁里更新 access/refresh token 与过期时间。
	// 快照导出须与 RefreshToken / SaveAtomic 同步，避免写出一新一旧的 token 对。
	a.Lock()
	defer a.Unlock()
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken":  a.AccessToken,
			"refreshToken": a.RefreshToken,
			"expiresAt":    a.ExpiresAt,
			"domain":       a.Domain,
			"realm":        a.realm,
		},
		"account": map[string]any{
			"uid":          a.UID,
			"enterpriseId": a.EnterpriseID,
			"nickname":     a.Nickname,
		},
	}
	if a.DeviceToken != "" {
		doc["device_token"] = a.DeviceToken
	}
	return doc
}

// MaskToken 把 token 中间打码，仅用于面板上的「当前值」预览（不用于导出落盘）。
func MaskToken(s string) string {
	if len(s) <= 16 {
		if s == "" {
			return ""
		}
		return "***"
	}
	return s[:6] + "…" + s[len(s)-6:]
}
