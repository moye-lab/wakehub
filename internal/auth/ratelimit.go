package auth

import "time"

// --- 登录限流（双维度：IP + 账号，契约 §2.5）---
//
// 同一 IP 或同一账号 5 分钟内失败 ≥ 8 次即锁定（429）；成功登录清除
// 该 IP 与该账号的失败记录；5 分钟清理 ticker 定期删过期记录；
// 存储硬上限 10000 个 key，超限时淘汰最旧的 key（防内存无限增长）。

// failureStore 是登录失败记录的存储后端（v0.2.9 问题 9）：
// 目前只有内存实现（memoryFailureStore）。需要跨重启/多实例持久化时，
// 可替换为 SQLite/Redis 实现，接口与限流语义不变——调用方（Service）
// 在方法内持 s.mu 调用后端，后端本身不加锁。
type failureStore interface {
	// Count 返回 key 在 cutoff 之后的新近失败次数（并丢弃过期时间戳）。
	Count(key string, cutoff time.Time) int
	// Append 追加一次失败记录；容量满时淘汰最旧的 key。
	Append(key string, now time.Time)
	// Delete 删除 key 的全部记录。
	Delete(key string)
	// Prune 删除全部过期记录（无新近失败的 key 整个删掉）。
	Prune(cutoff time.Time)
	// Len 返回当前 key 数（容量检查用）。
	Len() int
}

// failureRecord 是一条 key（"ip:"+IP 或 "acct:"+用户名）的失败记录。
type failureRecord struct {
	times    []time.Time
	lastSeen time.Time
}

// memoryFailureStore 是 failureStore 的内存实现：重启后记录清空
// （单机部署可接受，见 security.md）；容量与淘汰逻辑见 Append。
type memoryFailureStore struct {
	records map[string]*failureRecord
}

func newMemoryFailureStore() *memoryFailureStore {
	return &memoryFailureStore{records: make(map[string]*failureRecord)}
}

func (m *memoryFailureStore) Count(key string, cutoff time.Time) int {
	rec, ok := m.records[key]
	if !ok {
		return 0
	}
	var recent []time.Time
	for _, t := range rec.times {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	rec.times = recent
	return len(recent)
}

func (m *memoryFailureStore) Append(key string, now time.Time) {
	rec, ok := m.records[key]
	if !ok {
		if len(m.records) >= maxRateLimitKeys {
			m.evictOldest()
		}
		rec = &failureRecord{}
		m.records[key] = rec
	}
	rec.times = append(rec.times, now)
	rec.lastSeen = now
}

// evictOldest 在达到容量上限时淘汰最旧的 key（lastSeen 最早）。
func (m *memoryFailureStore) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for k, rec := range m.records {
		if oldestKey == "" || rec.lastSeen.Before(oldest) {
			oldestKey, oldest = k, rec.lastSeen
		}
	}
	if oldestKey != "" {
		delete(m.records, oldestKey)
	}
}

func (m *memoryFailureStore) Delete(key string) {
	delete(m.records, key)
}

func (m *memoryFailureStore) Prune(cutoff time.Time) {
	for key, rec := range m.records {
		var recent []time.Time
		for _, t := range rec.times {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}
		if len(recent) == 0 {
			delete(m.records, key)
		} else {
			rec.times = recent
		}
	}
}

func (m *memoryFailureStore) Len() int {
	return len(m.records)
}

// isRateLimited 判断 IP 或账号是否处于锁定状态。
func (s *Service) isRateLimited(ip, username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-loginFailureWindow)
	return s.failures.Count("ip:"+ip, cutoff) >= loginFailureThreshold ||
		s.failures.Count("acct:"+username, cutoff) >= loginFailureThreshold
}

// recordFailure 记一次登录失败（IP 与账号双维度）。
func (s *Service) recordFailure(ip, username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.failures.Append("ip:"+ip, now)
	s.failures.Append("acct:"+username, now)
}

// recordSuccess 登录成功后清除该 IP 与该账号的失败记录。
func (s *Service) recordSuccess(ip, username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures.Delete("ip:" + ip)
	s.failures.Delete("acct:" + username)
}

// pruneFailures 删除全部过期的失败记录（定期清理，5 分钟 ticker 驱动）。
func (s *Service) pruneFailures(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures.Prune(now.Add(-loginFailureWindow))
}
