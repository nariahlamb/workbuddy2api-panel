// accessors_mp.go 加锁读取访问器：mp 成长任务线（upstream/tasks.go、school.go）
// 移植自上游时统一改用 *Value() 取值，避免与 RefreshToken 的锁内改写构成数据竞争
// （go test -race 实证）。本 fork 原以裸字段访问，这里补齐同名方法保持一致。
package auth

// AccessTokenValue 加锁读取 AccessToken（RefreshToken 会在锁内改写它）。
func (a *Auth) AccessTokenValue() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.AccessToken
}

// DomainValue 加锁读取 Domain（同 AccessTokenValue）。
func (a *Auth) DomainValue() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Domain
}
