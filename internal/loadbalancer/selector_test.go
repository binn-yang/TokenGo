package loadbalancer

import (
	"context"
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func makeCandidates(n int) []peer.AddrInfo {
	candidates := make([]peer.AddrInfo, n)
	for i := 0; i < n; i++ {
		candidates[i] = peer.AddrInfo{ID: peer.ID(string(rune('A' + i)))}
	}
	return candidates
}

// --- WeightedSelector ---

func TestWeightedSelector_SelectEmpty(t *testing.T) {
	s := NewWeightedSelector()
	_, err := s.Select(context.Background(), nil)
	if err != ErrNoAvailableNodes {
		t.Fatalf("expected ErrNoAvailableNodes, got %v", err)
	}
}

func TestWeightedSelector_SelectSingle(t *testing.T) {
	s := NewWeightedSelector()
	candidates := makeCandidates(1)

	for i := 0; i < 10; i++ {
		got, err := s.Select(context.Background(), candidates)
		if err != nil {
			t.Fatalf("Select failed: %v", err)
		}
		if got.ID != candidates[0].ID {
			t.Fatalf("expected %v, got %v", candidates[0].ID, got.ID)
		}
	}
}

func TestWeightedSelector_ReportFailureReducesWeight(t *testing.T) {
	s := NewWeightedSelector()
	id := peer.ID("node-A")

	// 设置初始权重
	s.SetWeight(id, 5.0)

	// 3次失败后权重归零
	s.ReportFailure(id)
	s.ReportFailure(id)
	s.ReportFailure(id)

	s.mu.RLock()
	w := s.getWeight(id)
	s.mu.RUnlock()

	if w != 0 {
		t.Fatalf("3 次失败后权重应为 0, got %v", w)
	}
}

func TestWeightedSelector_ReportSuccessClearsFailures(t *testing.T) {
	s := NewWeightedSelector()
	id := peer.ID("node-A")

	s.SetWeight(id, 2.0)
	s.ReportFailure(id)
	s.ReportFailure(id)

	// 成功应清除失败计数并增加权重
	s.ReportSuccess(id)

	s.mu.RLock()
	failures := s.failures[id]
	w := s.getWeight(id)
	s.mu.RUnlock()

	if failures != 0 {
		t.Fatalf("ReportSuccess 后失败计数应为 0, got %d", failures)
	}
	if w <= 0 {
		t.Fatalf("ReportSuccess 后权重应大于 0, got %v", w)
	}
}

func TestWeightedSelector_WeightCap(t *testing.T) {
	s := NewWeightedSelector()
	id := peer.ID("node-A")

	s.SetWeight(id, 9.5)

	// 多次成功，权重不应超过 10.0
	for i := 0; i < 20; i++ {
		s.ReportSuccess(id)
	}

	s.mu.RLock()
	w := s.weights[id]
	s.mu.RUnlock()

	if w > 10.0 {
		t.Fatalf("权重不应超过 10.0, got %v", w)
	}
}

func TestWeightedSelector_AllZeroWeightFallback(t *testing.T) {
	s := NewWeightedSelector()
	candidates := makeCandidates(3)

	// 所有节点 3 次失败 → 权重全为 0
	for _, c := range candidates {
		s.ReportFailure(c.ID)
		s.ReportFailure(c.ID)
		s.ReportFailure(c.ID)
	}

	// 权重全为 0 时应随机选择而非报错
	got, err := s.Select(context.Background(), candidates)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}
	if got == nil {
		t.Fatal("Select returned nil")
	}
}

// --- RoundRobinSelector ---

func TestRoundRobinSelector_CycleThroughCandidates(t *testing.T) {
	s := NewRoundRobinSelector()
	candidates := makeCandidates(3)

	seen := make(map[peer.ID]int)
	for i := 0; i < 6; i++ {
		got, err := s.Select(context.Background(), candidates)
		if err != nil {
			t.Fatalf("Select failed: %v", err)
		}
		seen[got.ID]++
	}

	// 6 次选择，3 个候选，每个应被选2次
	for _, c := range candidates {
		if seen[c.ID] != 2 {
			t.Errorf("candidate %v selected %d times, want 2", c.ID, seen[c.ID])
		}
	}
}

func TestRoundRobinSelector_SkipsUnhealthyCandidates(t *testing.T) {
	s := NewRoundRobinSelector()
	candidates := makeCandidates(3)

	// 让候选 0 不健康
	s.ReportFailure(candidates[0].ID)
	s.ReportFailure(candidates[0].ID)
	s.ReportFailure(candidates[0].ID)

	// 选择 10 次，候选 0 不应出现
	for i := 0; i < 10; i++ {
		got, err := s.Select(context.Background(), candidates)
		if err != nil {
			t.Fatalf("Select failed: %v", err)
		}
		if got.ID == candidates[0].ID {
			t.Fatal("不健康的节点不应被选中")
		}
	}
}

func TestRoundRobinSelector_AllUnhealthyResets(t *testing.T) {
	s := NewRoundRobinSelector()
	candidates := makeCandidates(2)

	// 所有节点不健康
	for _, c := range candidates {
		s.ReportFailure(c.ID)
		s.ReportFailure(c.ID)
		s.ReportFailure(c.ID)
	}

	// 全部不健康时应重置 failures 并从全列表选取
	got, err := s.Select(context.Background(), candidates)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}

	found := false
	for _, c := range candidates {
		if got.ID == c.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Select 返回了不在候选列表中的节点")
	}
}

func TestRoundRobinSelector_Empty(t *testing.T) {
	s := NewRoundRobinSelector()
	_, err := s.Select(context.Background(), nil)
	if err != ErrNoAvailableNodes {
		t.Fatalf("expected ErrNoAvailableNodes, got %v", err)
	}
}

// --- RandomSelector ---

func TestRandomSelector_SelectEmpty(t *testing.T) {
	s := NewRandomSelector()
	_, err := s.Select(context.Background(), nil)
	if err != ErrNoAvailableNodes {
		t.Fatalf("expected ErrNoAvailableNodes, got %v", err)
	}
}

func TestRandomSelector_SelectReturnsFromCandidates(t *testing.T) {
	s := NewRandomSelector()
	candidates := makeCandidates(5)

	idSet := make(map[peer.ID]bool)
	for _, c := range candidates {
		idSet[c.ID] = true
	}

	for i := 0; i < 20; i++ {
		got, err := s.Select(context.Background(), candidates)
		if err != nil {
			t.Fatalf("Select failed: %v", err)
		}
		if !idSet[got.ID] {
			t.Fatalf("Selected ID %v not in candidates", got.ID)
		}
	}
}

func TestRandomSelector_AllUnhealthyResets(t *testing.T) {
	s := NewRandomSelector()
	candidates := makeCandidates(2)

	for _, c := range candidates {
		s.ReportFailure(c.ID)
		s.ReportFailure(c.ID)
		s.ReportFailure(c.ID)
	}

	got, err := s.Select(context.Background(), candidates)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}
	if got == nil {
		t.Fatal("Select returned nil")
	}
}

// --- filterHealthy ---

func TestFilterHealthy(t *testing.T) {
	candidates := makeCandidates(3)
	failures := map[peer.ID]int{
		candidates[0].ID: 5, // 不健康
		candidates[1].ID: 1, // 健康
	}

	healthy, resetNeeded := filterHealthy(candidates, failures, 3)
	if resetNeeded {
		t.Fatal("不应 reset，还有健康节点")
	}
	if len(healthy) != 2 {
		t.Fatalf("expected 2 healthy, got %d", len(healthy))
	}

	// 全部不健康
	for _, c := range candidates {
		failures[c.ID] = 5
	}
	healthy2, resetNeeded2 := filterHealthy(candidates, failures, 3)
	if !resetNeeded2 {
		t.Fatal("全部不健康时应返回 resetNeeded=true")
	}
	if len(healthy2) != len(candidates) {
		t.Fatalf("全部不健康时应返回原列表，len=%d", len(healthy2))
	}
}

// --- 并发安全 ---

func TestConcurrentSelectorAccess(t *testing.T) {
	selectors := []Selector{
		NewWeightedSelector(),
		NewRoundRobinSelector(),
		NewRandomSelector(),
	}

	candidates := makeCandidates(5)

	for _, sel := range selectors {
		s := sel
		var wg sync.WaitGroup
		wg.Add(100)
		for i := 0; i < 100; i++ {
			go func(i int) {
				defer wg.Done()
				_, _ = s.Select(context.Background(), candidates)
				if i%3 == 0 {
					s.ReportSuccess(candidates[i%len(candidates)].ID)
				} else {
					s.ReportFailure(candidates[i%len(candidates)].ID)
				}
			}(i)
		}
		wg.Wait()
	}
}
