// Package livecfg 运行期可变配置的并发安全持有者。
//
// 背景：进程启动时读入的配置是普通字段（读多写零），但管理面板允许在线改配置，
// 于是少量"可热生效"的字段需要有并发安全的读写点。此处用不可变快照 + atomic 指针：
// 读方 Load 拿到一致视图，写方 Store 整体替换，无锁无数据竞争。
//
// 只承载**读路径深、热改需求强**的少数字段；池参数/排程参数等各有既有 setter
// （pool.SetBreaker、scheduler.Reconfigure 等），不重复收编到这里。
package livecfg

import (
	"sync/atomic"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

// Snapshot 一次读取的不可变配置视图。
type Snapshot struct {
	APIKey               string        // 网关/面板共同鉴权密钥；空 = 不鉴权。全通配。
	SoftCooldown         time.Duration // 429 软冷却基数（<=0 时调用方回退内置默认）
	SanitizeFingerprints bool          // 出站请求体指纹脱敏

	// APIKeyCN / APIKeyGlobal realm 专用密钥（config.api_keys.cn / .global，可空）。
	//
	// 语义：持该密钥的客户端只能访问对应 realm 的模型与补全端点。用于"国内一套
	// key、国外一套 key"的分发场景——两个 key 各自只看到本域模型列表，且无法用
	// 另一个域的 model 前缀调补全（见 handler.realmFor）。
	//
	// 与 APIKey 的关系：APIKey 仍是全通配（向后兼容，也是面板登录用的那把）；
	// realm 密钥是额外收窄，不取代它。三者都为空的组合即"完全不鉴权"。
	APIKeyCN     string
	APIKeyGlobal string
}

// Realm 常量：与 auth.Realm()、模型 id 前缀（cn: / global:）同一套取值。
const (
	RealmCN     = "cn"
	RealmGlobal = "global"
)

// MatchKey 判定请求密钥属于哪个 realm，返回 (realm, 是否命中)。
//
// realm 返回空串表示该密钥是全通配的 APIKey（不受 realm 限制）；返回值
// ok=false 表示密钥不正确，调用方应回 401。
//
// 比较仍走 SHA-256 + 常量时间（复用 httpauth 的口径），且**始终把所有候选
// 都比较一遍**再决定结果，避免"命中哪个 key"通过耗时泄露。
func (s Snapshot) MatchKey(tok string) (string, bool) {
	if s.APIKey == "" && s.APIKeyCN == "" && s.APIKeyGlobal == "" {
		return "", true // 未启用鉴权
	}
	return httpauth.MatchRealmKeys(tok, s.APIKey, s.APIKeyCN, s.APIKeyGlobal)
}

// Holder 原子持有当前快照。
type Holder struct {
	p atomic.Pointer[Snapshot]
}

// New 以初始快照构建。
func New(s Snapshot) *Holder {
	h := &Holder{}
	h.Store(s)
	return h
}

// Load 返回当前快照（Holder 为 nil 或从未 Store 时返回零值快照，调用方无需判空）。
func (h *Holder) Load() Snapshot {
	if h == nil {
		return Snapshot{}
	}
	if s := h.p.Load(); s != nil {
		return *s
	}
	return Snapshot{}
}

// Store 整体替换快照。
func (h *Holder) Store(s Snapshot) { h.p.Store(&s) }
