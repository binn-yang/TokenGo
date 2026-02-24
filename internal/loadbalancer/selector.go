package loadbalancer

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

var (
	ErrNoAvailableNodes = errors.New("没有可用的节点")
)

// NodeInfo 节点信息
type NodeInfo struct {
	PeerID  peer.ID
	Addrs   []string
	Weight  float64
	Healthy bool
}

// Selector 节点选择器接口
type Selector interface {
	// Select 从候选节点中选择一个
	Select(ctx context.Context, candidates []peer.AddrInfo) (*peer.AddrInfo, error)
	// ReportSuccess 报告成功
	ReportSuccess(peerID peer.ID)
	// ReportFailure 报告失败
	ReportFailure(peerID peer.ID)
}

// filterHealthy 过滤健康节点 (失败次数 < threshold 的节点)
// 如果全部不健康，返回原列表并清空 failures
func filterHealthy(candidates []peer.AddrInfo, failures map[peer.ID]int, threshold int) ([]peer.AddrInfo, bool) {
	var healthy []peer.AddrInfo
	for _, c := range candidates {
		if failures[c.ID] < threshold {
			healthy = append(healthy, c)
		}
	}
	if len(healthy) == 0 {
		return candidates, true // reset needed
	}
	return healthy, false
}

// WeightedSelector 加权选择器
type WeightedSelector struct {
	weights  map[peer.ID]float64
	failures map[peer.ID]int
	mu       sync.RWMutex
}

// NewWeightedSelector 创建加权选择器
func NewWeightedSelector() *WeightedSelector {
	return &WeightedSelector{
		weights:  make(map[peer.ID]float64),
		failures: make(map[peer.ID]int),
	}
}

// Select 加权随机选择
func (s *WeightedSelector) Select(ctx context.Context, candidates []peer.AddrInfo) (*peer.AddrInfo, error) {
	if len(candidates) == 0 {
		return nil, ErrNoAvailableNodes
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// 计算总权重
	var totalWeight float64
	weights := make([]float64, len(candidates))

	for i, c := range candidates {
		w := s.getWeight(c.ID)
		weights[i] = w
		totalWeight += w
	}

	if totalWeight <= 0 {
		// 所有节点权重为 0，随机选择
		idx := rand.IntN(len(candidates))
		return &candidates[idx], nil
	}

	// 加权随机选择
	r := rand.Float64() * totalWeight
	var cumulative float64

	for i, w := range weights {
		cumulative += w
		if r <= cumulative {
			return &candidates[i], nil
		}
	}

	// 兜底返回最后一个
	return &candidates[len(candidates)-1], nil
}

// getWeight 获取节点权重 (内部方法，调用者需持有读锁)
func (s *WeightedSelector) getWeight(id peer.ID) float64 {
	// 检查失败次数
	failures := s.failures[id]
	if failures >= 3 {
		return 0 // 连续失败 3 次，暂时排除
	}

	// 获取基础权重
	w, ok := s.weights[id]
	if !ok {
		w = 1.0 // 默认权重
	}

	// 根据失败次数降低权重
	if failures > 0 {
		w = w / float64(failures+1)
	}

	return w
}

// ReportSuccess 报告成功
func (s *WeightedSelector) ReportSuccess(peerID peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 清除失败计数
	delete(s.failures, peerID)

	// 增加权重
	if w, ok := s.weights[peerID]; ok {
		s.weights[peerID] = min(w*1.1, 10.0) // 最大权重 10
	} else {
		s.weights[peerID] = 1.0
	}
}

// ReportFailure 报告失败
func (s *WeightedSelector) ReportFailure(peerID peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failures[peerID]++

	// 降低权重
	if w, ok := s.weights[peerID]; ok {
		s.weights[peerID] = max(w*0.5, 0.1)
	}
}

// SetWeight 设置节点权重
func (s *WeightedSelector) SetWeight(peerID peer.ID, weight float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.weights[peerID] = weight
}

// ResetFailures 重置失败计数
func (s *WeightedSelector) ResetFailures(peerID peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, peerID)
}

// RoundRobinSelector 轮询选择器
type RoundRobinSelector struct {
	index    int
	failures map[peer.ID]int
	mu       sync.Mutex
}

// NewRoundRobinSelector 创建轮询选择器
func NewRoundRobinSelector() *RoundRobinSelector {
	return &RoundRobinSelector{
		failures: make(map[peer.ID]int),
	}
}

// Select 轮询选择
func (s *RoundRobinSelector) Select(ctx context.Context, candidates []peer.AddrInfo) (*peer.AddrInfo, error) {
	if len(candidates) == 0 {
		return nil, ErrNoAvailableNodes
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	healthy, resetNeeded := filterHealthy(candidates, s.failures, 3)
	if resetNeeded {
		s.failures = make(map[peer.ID]int)
	}

	// 轮询选择
	s.index = (s.index + 1) % len(healthy)
	return &healthy[s.index], nil
}

// ReportSuccess 报告成功
func (s *RoundRobinSelector) ReportSuccess(peerID peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, peerID)
}

// ReportFailure 报告失败
func (s *RoundRobinSelector) ReportFailure(peerID peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[peerID]++
}

// RandomSelector 随机选择器
type RandomSelector struct {
	failures map[peer.ID]int
	mu       sync.Mutex
}

// NewRandomSelector 创建随机选择器
func NewRandomSelector() *RandomSelector {
	return &RandomSelector{
		failures: make(map[peer.ID]int),
	}
}

// Select 随机选择
func (s *RandomSelector) Select(ctx context.Context, candidates []peer.AddrInfo) (*peer.AddrInfo, error) {
	if len(candidates) == 0 {
		return nil, ErrNoAvailableNodes
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	healthy, resetNeeded := filterHealthy(candidates, s.failures, 3)
	if resetNeeded {
		s.failures = make(map[peer.ID]int)
	}

	idx := rand.IntN(len(healthy))
	return &healthy[idx], nil
}

// ReportSuccess 报告成功
func (s *RandomSelector) ReportSuccess(peerID peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, peerID)
}

// ReportFailure 报告失败
func (s *RandomSelector) ReportFailure(peerID peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[peerID]++
}
