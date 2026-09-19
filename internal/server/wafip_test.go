package server

import (
	"testing"
	"time"
)

// TestWafIPGateRealmIsolation 锁定闸门必须按 realm 隔离——这是 2026-09-19 日志的
// 直接结论：同一出口 IP 上 global 域 60s 内 4 个不同账号接连 403，而**同期 CN 域
// 连续 12 次 200**。单闸门版本下 global 被拦会把健康的 CN 一起打死。
func TestWafIPGateRealmIsolation(t *testing.T) {
	var gs wafIPGates
	// global 域两个不同号命中 → 仅 global 激活。
	if gs.note("global", "u1") {
		t.Fatal("首个号不应激活")
	}
	if !gs.note("global", "u2") {
		t.Fatal("global 域两个不同号应激活")
	}
	if !gs.active("global") {
		t.Fatal("global 应处于激活期")
	}
	if gs.active("cn") {
		t.Fatal("cn 域不得被 global 的拦截影响（实测两域不对称）")
	}
	if gs.retryAfter("cn") != 0 {
		t.Fatal("cn 的 retryAfter 应为 0")
	}
}

// TestWafIPGateSameAccountNeverActivates 单号反复 403 永不触发：判定口径是
// 「不同 UID 数」，账号级偶发归软冷却管。
func TestWafIPGateSameAccountNeverActivates(t *testing.T) {
	var gs wafIPGates
	for i := 0; i < 10; i++ {
		if gs.note("global", "same") {
			t.Fatalf("单号反复命中不应激活（第 %d 次）", i)
		}
	}
	if gs.active("global") {
		t.Fatal("不应激活")
	}
}

// TestWafIPGateActivatesAndExpires 激活后到期自然解除（不做主动探测）。
func TestWafIPGateActivatesAndExpires(t *testing.T) {
	old := wafIPWindow
	wafIPWindow = 40 * time.Millisecond
	defer func() { wafIPWindow = old }()

	var gs wafIPGates
	gs.note("global", "u1")
	gs.note("global", "u2")
	if !gs.active("global") {
		t.Fatal("应激活")
	}
	if d := gs.retryAfter("global"); d <= 0 {
		t.Fatal("激活期 retryAfter 应 > 0")
	}
	time.Sleep(60 * time.Millisecond)
	if gs.active("global") {
		t.Fatal("窗口到期应自然解除")
	}
	if gs.retryAfter("global") != 0 {
		t.Fatal("解除后 retryAfter 应为 0")
	}
}

// TestWafIPGateNoRenewOnHit 激活期内新命中不续期（保守：窗口自然解除，不叠旧账）。
func TestWafIPGateNoRenewOnHit(t *testing.T) {
	old := wafIPWindow
	wafIPWindow = 80 * time.Millisecond
	defer func() { wafIPWindow = old }()

	var gs wafIPGates
	gs.note("global", "u1")
	gs.note("global", "u2")
	first := gs.gate("global").retryAfter()
	time.Sleep(30 * time.Millisecond)
	gs.note("global", "u3") // 激活期内命中
	second := gs.gate("global").retryAfter()
	if second >= first {
		t.Fatalf("激活期内命中不得续期：first=%v second=%v", first, second)
	}
}

// TestWafIPGateEmptyRealmDefaultsToCN 空 realm 归 cn 档（与 resolveModel 缺省一致）。
func TestWafIPGateEmptyRealmDefaultsToCN(t *testing.T) {
	var gs wafIPGates
	gs.note("", "u1")
	gs.note("", "u2")
	if !gs.active("cn") {
		t.Fatal("空 realm 应归 cn 档")
	}
	if gs.active("global") {
		t.Fatal("不应影响 global")
	}
}
