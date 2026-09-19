package pool

// entryForTest 取池内 entry（同包测试用，只读语义）。
func (p *Pool) entryForTest(uid string) *entry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.byUID[uid]
}
