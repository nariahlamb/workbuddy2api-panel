// Package httpauth 网关与面板共用的 Bearer 鉴权原语。
//
// 单独成包的原因：server（/v1/*、/status）与 panel（/panel/api/*）两处鉴权
// 必须完全同口径——此前各自复制了一份"字符串直接比较"的实现，既容易漂移，
// 又都带计时侧信道。统一到这里后，口径只有一份，且天然常量时间比较。
package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerPrefix 认证方案前缀（大小写敏感，与 HTTP 规范及既有实现一致）。
const bearerPrefix = "Bearer "

// VerifyBearer 校验请求头是否携带正确的 Bearer 密钥。
//
// key 为空表示"未启用鉴权"，恒返回 true（调用方据此放行）。
// 比较用 SHA-256 摘要 + subtle.ConstantTimeCompare：
//   - 常量时间，不因前缀匹配长度而泄露信息；
//   - 先摘要再比较，长度差异被吸收进摘要（不会因长度不同提前返回）；
//   - 摘要本身不可逆，即便有侧信道也拿不到密钥原文。
func VerifyBearer(r *http.Request, key string) bool {
	if key == "" {
		return true
	}
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, bearerPrefix) {
		// 缺头/方案不对：仍走一次摘要比较，保持耗时形状一致。
		subtle.ConstantTimeCompare(digest(""), digest(key))
		return false
	}
	tok := authz[len(bearerPrefix):]
	return subtle.ConstantTimeCompare(digest(tok), digest(key)) == 1
}

// digest 返回 s 的 SHA-256（定长 32 字节，供常量时间比较）。
func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// realmKeyCN / realmKeyGlobal 与 livecfg.RealmCN / RealmGlobal 同值。
// 定义在此处避免 httpauth 反向依赖 livecfg（依赖方向：livecfg → httpauth）。
const (
	realmKeyCN     = "cn"
	realmKeyGlobal = "global"
)

// MatchRealmKeys 判定 token 命中哪把密钥，返回 (realm, ok)。
//
// 入参语义：
//   - wildcard 全通配密钥（旧 api_key）。命中时返回 realm="", ok=true——
//     调用方据此不做 realm 限制（向后兼容：老配置只有一个 key，行为不变）。
//   - cn / global realm 专用密钥。命中时返回对应 realm，调用方据此收窄范围。
//
// 三把都为空 = 未启用鉴权，恒 (…)→("", true)。
//
// 常量时间与不泄露命中信息：三个比较全部执行（不用短路），且所有分支耗时
// 形状一致——否则"用的是哪把 key"可由响应时间推断。比较实现复用 digest +
// subtle.ConstantTimeCompare，与 VerifyBearer 同口径。
func MatchRealmKeys(tok, wildcard, cn, global string) (string, bool) {
	if wildcard == "" && cn == "" && global == "" {
		return "", true
	}
	dt := digest(tok)
	hitW := wildcard != "" && subtle.ConstantTimeCompare(dt, digest(wildcard)) == 1
	hitC := cn != "" && subtle.ConstantTimeCompare(dt, digest(cn)) == 1
	hitG := global != "" && subtle.ConstantTimeCompare(dt, digest(global)) == 1

	// 顺序即优先级：全通配 > cn > global。互斥由配置保证（同一个 key 不应
	// 同时配到两个域），但即使误配也只会按此固定顺序取一个，不产生歧义。
	switch {
	case hitW:
		return "", true
	case hitC:
		return realmKeyCN, true
	case hitG:
		return realmKeyGlobal, true
	default:
		return "", false
	}
}

// BearerToken 从请求头取出 Bearer token（无头或方案不符时返回空串）。
// 供调用方在 VerifyBearer 之外自行做 realm 判定时复用。
func BearerToken(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, bearerPrefix) {
		if t := authz[len(bearerPrefix):]; t != "" {
			return t
		}
	}
	// x-api-key 回退：Anthropic 系客户端（Claude Code 等）用该头传密钥，
	// 不走 Authorization: Bearer。空值不参与回退，避免空头覆盖有效 Bearer。
	return r.Header.Get("x-api-key")
}
