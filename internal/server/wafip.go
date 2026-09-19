// wafip.go WAF IP 级拦截 fail-fast 状态机（任务书 waf-ip-failfast）。
//
// 背景（fork-scan-absorb T-1 / BulidH 实测 + 2026-09-19 生产日志）：WAF 403 拦的是
// 网关出口 IP 而非账号——2026-09-19 日志实证：同一出口 IP（新加坡机房，ip-api 标
// proxy=true）上 global 域 60s 内 4 个不同账号接连 403（14 次 waf_block），而**同期
// CN 域连续 12 次 200**。两个国际站域名的 WAF 对机房/代理 IP 严，腾讯 CN 站不严。
// 故这是 IP/指纹维风控，不是账号问题（被拦的号几分钟后仍能成功）。
//
// 这两条证据决定了两处设计：
//  1. 闸门**按 realm 隔离**（wafIPGates）：IP 级状态只约束命中的那个域。单闸门版本下
//     global 被拦会把健康的 CN 一起打死（反之亦然），与实测的分域不对称矛盾。
//  2. **入口 fail-fast**，而非只在轮转循环内 break：实测 01:33:21 激活后，
//     01:33:24/30/39 三个**新请求**仍各撞一次上游才被拦——已在封禁窗口内的请求连
//     "撞一次"都是白费，还向已判定为风控目标的出口 IP 继续加量。故入口直接拒绝，
//     窗口到期由闸门自然解除（首个请求即恢复探测，无需主动探活）。
//
// 判定：ErrWafBlock 基础上的短窗多号计数——wafIPWindow（60s 滑动窗）内
// ≥ wafIPThreshold 个**不同** UID 接连命中 WAF 403 → 判定 IP 级拦截，激活至
// now+wafIPWindow。单号反复 403（账号级偶发）永不触发：只数不同号。
// 激活期内新命中不续期（保守：不做主动探测，窗口自然解除）。
//
// 归属层评估：放 server（Handler 局部）而非 pool——IP 级状态唯一消费者是
// chatCompletions（是否继续轮转 / 入口是否直接拒绝），pool 是账号级记账层，跨 UID
// 语义不属于任何账号；server 已有进程级状态先例 degradeGate（mu+until 同风格）。
// 账号级软冷却照常记账（applyErrorPolicy 不变），IP 级状态只改变「是否继续轮转」与
// 「入口是否拒绝」——协同不叠加。进程内状态、重启清零（窗口 60s，重建成本极低）。
package server

import (
	"log"
	"sync"
	"time"
)

// wafIPWindow IP 级判定滑动窗 + 激活时长：窗内不同账号命中 WAF 403 达阈值即
// 判 IP 级拦截，激活同样长（到期自然解除）。var 仅供测试注入短窗（生产恒 60s）。
var wafIPWindow = 60 * time.Second

// wafIPThreshold 判定阈值：窗内不同 UID 数达到该值激活。取 2——「多号」的最小
// 定义：单号反复 403 永不触发（账号级偶发归软冷却管），两个不同号在 60s 内接连
// 被拦（同一出口 IP）已是 IP 级证据。
const wafIPThreshold = 2

// wafIPGate 单个 realm 的 WAF IP 级拦截状态机（零值可用）。
type wafIPGate struct {
	mu    sync.Mutex
	hits  map[string]time.Time // uid → 最近一次 WAF 403 时刻（判定窗内，惰性剪枝）
	until time.Time            // IP 级拦截激活截止；零值 = 未激活
}

// noteWaf 记一次某账号的 WAF 403，返回记账后本域 IP 级拦截是否激活（调用方据此
// fail-fast 终止轮转，优先于 rotateBackoff 退避）。
//   - 已激活（now < until）：不续期、不记账（窗口期内不重置——保守自然解除）→ true；
//   - 未激活：记 hits[uid]=now（同号重复命中覆盖不累计，判定口径是「不同号数」），
//     剪掉窗外的过期命中；不同 UID 数达 wafIPThreshold → 激活到 now+wafIPWindow
//     （打一条 WARN 供观测），清空判定窗（解除后需全新命中重新判定，不叠旧账）。
func (g *wafIPGate) noteWaf(uid string) bool {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.until) {
		return true // 激活期内新命中：不续期（自然解除语义）
	}
	if g.hits == nil {
		g.hits = map[string]time.Time{}
	}
	g.hits[uid] = now
	for u, t := range g.hits {
		if now.Sub(t) > wafIPWindow {
			delete(g.hits, u)
		}
	}
	if len(g.hits) >= wafIPThreshold {
		g.until = now.Add(wafIPWindow)
		log.Printf("WARN: [server] waf ip-level block: %d accounts hit waf 403 within %s, rotate fail-fast until %s", len(g.hits), wafIPWindow, g.until.Format(time.RFC3339))
		g.hits = map[string]time.Time{}
		return true
	}
	return false
}

// active 报告本域 IP 级拦截是否激活。
func (g *wafIPGate) active() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.until)
}

// retryAfter 返回距本域窗口解除的剩余时长（未激活返回 0）。供入口 fail-fast 回
// Retry-After 头，让客户端知道该等多久而不是盲目重试。
func (g *wafIPGate) retryAfter() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if !now.Before(g.until) {
		return 0
	}
	return g.until.Sub(now)
}

// wafIPGates 按 realm 分档的闸门集合（见文件头「按 realm 隔离」说明）。
// realm 取 resolveModel 的产物（恒 "cn" / "global"）；空串兜底归 "cn" 档（不带
// 前缀的模型即 CN，与 resolveModel 的缺省口径一致）。懒建：只有真出现 WAF 403 的域
// 才会分配闸门，未命中的域零开销。
type wafIPGates struct {
	mu sync.Mutex
	m  map[string]*wafIPGate
}

// gate 取（惰性建）指定 realm 的闸门。
func (gs *wafIPGates) gate(realm string) *wafIPGate {
	if realm == "" {
		realm = "cn"
	}
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if gs.m == nil {
		gs.m = map[string]*wafIPGate{}
	}
	g, ok := gs.m[realm]
	if !ok {
		g = &wafIPGate{}
		gs.m[realm] = g
	}
	return g
}

// note 记一次某 realm 内某账号的 WAF 403，返回该 realm 的 IP 级拦截是否激活。
func (gs *wafIPGates) note(realm, uid string) bool { return gs.gate(realm).noteWaf(uid) }

// active 报告该 realm 的 IP 级拦截是否激活。
func (gs *wafIPGates) active(realm string) bool { return gs.gate(realm).active() }

// retryAfter 返回该 realm 距窗口解除的剩余时长（未激活返回 0）。
func (gs *wafIPGates) retryAfter(realm string) time.Duration { return gs.gate(realm).retryAfter() }
