package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestExhaustedDistinguishesUnknownFromZero 锁定 exhausted() 的核心口径：
// 「已确认耗尽」与「余额未知」必须分开。creditsTotal==0 表示从未成功读过余额
// （新号/刷新失败），此时 credits 恒 0 不代表没额度——若按耗尽处理，新号永远
// 选不上、池子空转 503。
func TestExhaustedDistinguishesUnknownFromZero(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "unknown"})
	if p.entryForTest("unknown").exhausted() {
		t.Fatal("creditsTotal==0 (余额未知) 不得判为耗尽")
	}
	p.SetCredits("unknown", 0, 350)
	if !p.entryForTest("unknown").exhausted() {
		t.Fatal("creditsTotal>0 且 credits<=0 应判为耗尽")
	}
	p.SetCredits("unknown", 1, 350)
	if p.entryForTest("unknown").exhausted() {
		t.Fatal("credits>0 不得判为耗尽")
	}
}

// TestPickSkipsExhaustedAccount 选号必须跳过已确认耗尽的号——这是 2026-09-19
// 生产循环的根因：三个 global 号 credits=0/credits_total=350 却只吃 10 分钟软冷却
// 就重新参选，选中又撞一次 14018。
func TestPickSkipsExhaustedAccount(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "empty"})
	p.Add(&auth.Auth{UID: "rich"})
	p.SetCredits("empty", 0, 350)
	p.SetCredits("rich", 100, 0)
	for i := 0; i < 200; i++ {
		got := p.Pick()
		if got == nil {
			t.Fatal("pick=nil，rich 应始终可选")
		}
		if got.UID == "empty" {
			t.Fatalf("耗尽号被选中（第 %d 次）", i)
		}
	}
}

// TestPickEarliestExpirySkipsExhausted 全冷却兜底同样不得选中已耗尽号：兜底语义是
// 「半开试探可能已恢复的号」，而已确认归零的号试探必失败（同一个循环的另一半）。
func TestPickEarliestExpirySkipsExhausted(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "empty"})
	p.SetCredits("empty", 0, 350)
	p.Cooldown("empty", CoolSoft, time.Minute, "test")
	if got := p.Pick(); got != nil {
		t.Fatalf("兜底不应选中已确认耗尽的号，got=%v", got.UID)
	}
}

// TestPickByUIDForModelSkipsExhausted 粘性命中不得放行已耗尽号，否则会话被钉死在
// 空号上反复撞 402（粘性优先于普通轮换，危害比随机选中更大）。
func TestPickByUIDForModelSkipsExhausted(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "sticky"})
	p.SetCredits("sticky", 0, 350)
	if got := p.PickByUIDForModel("sticky", "glm-5.2"); got != nil {
		t.Fatalf("粘性命中不应放行已耗尽号，got=%v", got.UID)
	}
	if got := p.PickByUID("sticky"); got != nil {
		t.Fatalf("PickByUID 不应放行已耗尽号，got=%v", got.UID)
	}
}

// TestExhaustedAccountReturnsWhenCreditsRestored 自愈性：余额刷回 >0 立即恢复可选，
// 无需等任何冷却到期。这是本闸门相对「只靠冷却」的关键优势。
func TestExhaustedAccountReturnsWhenCreditsRestored(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 0, 350)
	if got := p.Pick(); got != nil {
		t.Fatal("耗尽后不应可选")
	}
	p.SetCredits("u1", 500, 350)
	if got := p.Pick(); got == nil || got.UID != "u1" {
		t.Fatalf("余额恢复后应立即可选，got=%v", got)
	}
}

// TestAvailableUIDsAndServableSkipExhausted 可用集合与探活必须与选号同口径，
// 否则会出现「/healthz 报 200 可服务、chat 却全池跳过返 503」的矛盾。
func TestAvailableUIDsAndServableSkipExhausted(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 0, 350)
	if uids := p.AvailableUIDs(); len(uids) != 0 {
		t.Fatalf("AvailableUIDs=%v want empty", uids)
	}
	if p.ServableNow() {
		t.Fatal("仅剩耗尽号时 ServableNow 应为 false（与 chat 可达性同口径）")
	}
	if p.ServableForRealm("cn") {
		t.Fatal("ServableForRealm 同口径")
	}
	if _, healthy, _, _, _ := p.CountsDetailed(); healthy != 1 {
		t.Fatalf("healthy=%d want 1（healthy 只管状态机，不含余额维度）", healthy)
	}
}

// TestExhaustedNotCountedAsCooling 耗尽号不进 cooling 计数：它没有到期时间，
// 计成 cooling 会让运维看到错误的标签（以为在等冷却恢复）。
func TestExhaustedNotCountedAsCooling(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 0, 350)
	if _, _, cooling, _, _ := p.CountsDetailed(); cooling != 0 {
		t.Fatalf("cooling=%d want 0（耗尽不是冷却态）", cooling)
	}
	st, _ := p.Status("u1")
	if st.Cooling {
		t.Fatal("Status.Cooling 不应为 true")
	}
}

// TestAllExhaustedForRealm 锁定「仅因余额耗尽不可服务」的判定边界。
func TestAllExhaustedForRealm(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "g1"})
	p.Add(&auth.Auth{UID: "g2"})
	p.SetCredits("g1", 0, 350)
	p.SetCredits("g2", 0, 350)
	if !p.AllExhaustedForRealm("") {
		t.Fatal("两个号都已确认耗尽 → true")
	}
	// 存在非耗尽号 → false（真实原因不是余额，可能是冷却，不该报成余额问题）。
	p.SetCredits("g2", 10, 350)
	if p.AllExhaustedForRealm("") {
		t.Fatal("存在非耗尽号 → false")
	}
	// 余额未知（creditsTotal==0）不算耗尽 → false。
	p2 := New("")
	p2.Add(&auth.Auth{UID: "n1"})
	if p2.AllExhaustedForRealm("") {
		t.Fatal("余额未知不得判为耗尽 → false")
	}
	// 空池 → false（没有任何号，不是"余额耗尽"这个原因）。
	if New("").AllExhaustedForRealm("") {
		t.Fatal("空池 → false")
	}
	// 禁用号不参与判定：全部禁用时 seen=false → false（交由既有 disabled 口径处理）。
	p3 := New("")
	p3.Add(&auth.Auth{UID: "d1"})
	p3.SetCredits("d1", 0, 350)
	p3.Disable("d1", "session dead")
	if p3.AllExhaustedForRealm("") {
		t.Fatal("全禁用 → false（禁用是另一种不可用原因）")
	}
}
