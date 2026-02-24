package relay

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// ExitEntry 已注册的 Exit 节点条目
type ExitEntry struct {
	PubKeyHash    string
	Conn          quic.Connection
	RegisteredAt  time.Time
	LastHeartbeat time.Time
}

// Registry Exit 节点注册表
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*ExitEntry
}

// NewRegistry 创建注册表
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[string]*ExitEntry),
	}
}

// Register 注册 Exit 节点，如果已有旧连接则关闭旧的
func (r *Registry) Register(pubKeyHash string, conn quic.Connection) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 如果已有旧连接，关闭旧的
	if old, ok := r.entries[pubKeyHash]; ok {
		log.Printf("Exit %s 重新注册，关闭旧连接 %s", pubKeyHash, old.Conn.RemoteAddr())
		old.Conn.CloseWithError(0, "replaced by new connection")
	}

	now := time.Now()
	r.entries[pubKeyHash] = &ExitEntry{
		PubKeyHash:    pubKeyHash,
		Conn:          conn,
		RegisteredAt:  now,
		LastHeartbeat: now,
	}
	log.Printf("Exit 注册成功: %s (来自 %s), 当前注册数: %d", pubKeyHash, conn.RemoteAddr(), len(r.entries))
}

// Lookup 查找 Exit 节点连接
func (r *Registry) Lookup(pubKeyHash string) (quic.Connection, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.entries[pubKeyHash]
	if !ok {
		return nil, false
	}
	return entry.Conn, true
}

// Remove 移除 Exit 节点
func (r *Registry) Remove(pubKeyHash string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.entries[pubKeyHash]; ok {
		delete(r.entries, pubKeyHash)
		log.Printf("Exit 已移除: %s, 当前注册数: %d", pubKeyHash, len(r.entries))
	}
}

// RemoveIfMatch 移除 Exit 节点，但只有在连接匹配时才移除（避免 TOCTOU 竞争）
func (r *Registry) RemoveIfMatch(pubKeyHash string, conn quic.Connection) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.entries[pubKeyHash]; ok {
		if entry.Conn == conn { // 比较连接是否相同
			delete(r.entries, pubKeyHash)
			log.Printf("Exit 已移除 (匹配): %s, 当前注册数: %d", pubKeyHash, len(r.entries))
			return true
		}
		log.Printf("Exit %s 连接已更新，跳过移除", pubKeyHash)
		return false
	}
	return false
}

// UpdateHeartbeat 更新心跳时间
func (r *Registry) UpdateHeartbeat(pubKeyHash string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.entries[pubKeyHash]; ok {
		entry.LastHeartbeat = time.Now()
	}
}

// StartCleanup 启动后台清理 goroutine，清理超时的 Entry
func (r *Registry) StartCleanup(ctx context.Context, timeout time.Duration) {
	go func() {
		ticker := time.NewTicker(timeout / 2)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.cleanup(timeout)
			}
		}
	}()
	log.Printf("Registry 清理任务已启动，超时时间: %v", timeout)
}

// cleanup 清理超时的 Exit 条目
func (r *Registry) cleanup(timeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for hash, entry := range r.entries {
		if now.Sub(entry.LastHeartbeat) > timeout {
			log.Printf("Exit %s 心跳超时 (%v)，移除", hash, now.Sub(entry.LastHeartbeat))
			entry.Conn.CloseWithError(0, "heartbeat timeout")
			delete(r.entries, hash)
		}
	}
}

// Count 返回已注册的 Exit 数量
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}
