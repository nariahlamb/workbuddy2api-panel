// 分池选号域：按 realm（cn/global）过滤选号与可用集合。realm=="" 退化为现状。
package pool

import (
	"sort"
	"time"
)

// AvailableUIDsForRealm 同 AvailableUIDs，但仅返回 Realm()==realm 的账号。
// realm=="" 退化为 AvailableUIDs（现状语义）。
func (p *Pool) AvailableUIDsForRealm(realm string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	uids := make([]string, 0, len(p.byUID))
	for uid, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if !e.usable(now) {
			continue // usable = healthy 且未确认余额耗尽（见 entry.usable）
		}
		if p.inFlightFull(e) {
			continue
		}
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	return uids
}

// AllExhaustedForRealm 报告该 realm 是否「仅因余额耗尽而不可服务」：域内存在至少
// 一个未禁用账号，且其中**每一个**都已确认余额耗尽（见 entry.exhausted）。
//
// 用途：选号返 nil 后区分「为什么没号」。若不分，chat 只能回笼统的
// no_healthy_account（"all accounts are temporarily unavailable"），而当域内号是
// 真没额度时这个措辞会掩盖真实原因——用户看到"暂时不可用"会以为网关坏了，实际是
// 需要充值/等签到。修复前这个信息靠"选中→撞 402→透传上游原文"间接暴露，选号层
// 排除耗尽号后必须显式补齐，否则是可见的信息回退。
//
// 刻意要求「全部耗尽」而非「存在耗尽」：若域内还有号只是临时冷却（会自愈），
// 则真实原因是冷却，不该报成余额问题。
func (p *Pool) AllExhaustedForRealm(realm string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	seen := false
	for _, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if e.disabled {
			continue // 禁用号不参与：它是另一种不可用原因，交由既有口径处理
		}
		seen = true
		if !e.exhausted() {
			return false
		}
	}
	return seen
}

// AvailableUIDsForModelRealm 同 AvailableUIDsForModel，但仅返回 Realm()==realm 的账号
// （6004 模型豁免照常生效）。realm=="" 退化为 AvailableUIDsForModel。
func (p *Pool) AvailableUIDsForModelRealm(model, realm string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	uids := make([]string, 0, len(p.byUID))
	for uid, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if !e.usableForModel(now, model) {
			continue // 同 AvailableUIDsForRealm：耗尽号不进可用集合
		}
		if p.inFlightFull(e) {
			continue
		}
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	return uids
}
