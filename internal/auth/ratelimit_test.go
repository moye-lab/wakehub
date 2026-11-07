package auth

import (
	"fmt"
	"testing"
	"time"
)

// TestMemoryFailureStore 覆盖 v0.2.9 问题 9 的存储后端：计数含过期丢弃、
// 删除、定期清理、容量上限淘汰最旧 key。
func TestMemoryFailureStore(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-loginFailureWindow)
	s := newMemoryFailureStore()

	// Append + Count：窗口内 3 次，窗口外 1 次不计。
	s.Append("ip:192.168.1.1", now.Add(-6*time.Minute))
	s.Append("ip:192.168.1.1", now.Add(-4*time.Minute))
	s.Append("ip:192.168.1.1", now.Add(-2*time.Minute))
	s.Append("ip:192.168.1.1", now)
	if got := s.Count("ip:192.168.1.1", cutoff); got != 3 {
		t.Fatalf("Count 应为 3（窗口外那次丢弃），got %d", got)
	}
	if got := s.Count("ip:9.9.9.9", cutoff); got != 0 {
		t.Fatalf("不存在的 key 应为 0，got %d", got)
	}

	// Delete：删后计数归零。
	s.Delete("ip:192.168.1.1")
	if got := s.Count("ip:192.168.1.1", cutoff); got != 0 {
		t.Fatalf("Delete 后应为 0，got %d", got)
	}

	// Prune：只剩过期记录的 key 整个删掉，有新近记录的 key 保留。
	s.Append("ip:192.168.1.2", now.Add(-6*time.Minute))
	s.Append("ip:192.168.1.3", now.Add(-1*time.Minute))
	s.Prune(cutoff)
	if got := s.Len(); got != 1 {
		t.Fatalf("Prune 后应只剩 1 个 key，got %d", got)
	}
	if got := s.Count("ip:192.168.1.3", cutoff); got != 1 {
		t.Fatalf("保留的 key 计数应为 1，got %d", got)
	}

	// 容量上限：写满后追加，淘汰最旧的 key，总数不增长且新 key 存在
	//（各 key 的 lastSeen 相同，淘汰哪一个旧 key 不确定，只断言不变量）。
	full := newMemoryFailureStore()
	for i := 0; i < maxRateLimitKeys; i++ {
		full.Append(fmt.Sprintf("k%d", i), now)
	}
	full.Append("k-new", now)
	if got := full.Len(); got != maxRateLimitKeys {
		t.Fatalf("超限追加后总数应仍为 %d，got %d", maxRateLimitKeys, got)
	}
	if got := full.Count("k-new", cutoff); got != 1 {
		t.Fatalf("新 key 应被记录，got %d", got)
	}
}
