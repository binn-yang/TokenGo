package relay

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// mockConn 实现 quic.Connection 接口用于测试
type mockConn struct {
	id         int
	closeCalls atomic.Int32
	ctx        context.Context
	cancel     context.CancelFunc
}

func newMockConn(id int) *mockConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &mockConn{id: id, ctx: ctx, cancel: cancel}
}

func (m *mockConn) AcceptStream(_ context.Context) (quic.Stream, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockConn) AcceptUniStream(_ context.Context) (quic.ReceiveStream, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockConn) OpenStream() (quic.Stream, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockConn) OpenStreamSync(_ context.Context) (quic.Stream, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockConn) OpenUniStream() (quic.SendStream, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockConn) OpenUniStreamSync(_ context.Context) (quic.SendStream, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockConn) LocalAddr() net.Addr                        { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4433} }
func (m *mockConn) RemoteAddr() net.Addr                       { return &net.UDPAddr{IP: net.IPv4(10, 0, 0, byte(m.id)), Port: 5000 + m.id} }
func (m *mockConn) CloseWithError(_ quic.ApplicationErrorCode, _ string) error {
	m.closeCalls.Add(1)
	m.cancel()
	return nil
}
func (m *mockConn) Context() context.Context                   { return m.ctx }
func (m *mockConn) ConnectionState() quic.ConnectionState      { return quic.ConnectionState{} }
func (m *mockConn) SendDatagram(_ []byte) error                { return fmt.Errorf("not implemented") }
func (m *mockConn) ReceiveDatagram(_ context.Context) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestRegistry_ConcurrentRegisterLookup(t *testing.T) {
	r := NewRegistry()
	const n = 100
	var wg sync.WaitGroup

	// 并发注册 n 个不同的 Exit
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			hash := fmt.Sprintf("hash-%d", i)
			conn := newMockConn(i)
			r.Register(hash, conn, []byte(fmt.Sprintf("keyconfig-%d", i)))
		}(i)
	}
	wg.Wait()

	if r.Count() != n {
		t.Fatalf("期望 %d 个注册，实际 %d", n, r.Count())
	}

	// 并发查找
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			hash := fmt.Sprintf("hash-%d", i)
			conn, ok := r.Lookup(hash)
			if !ok {
				t.Errorf("Lookup(%s) 应该找到注册", hash)
				return
			}
			if conn == nil {
				t.Errorf("Lookup(%s) 返回了 nil conn", hash)
			}
		}(i)
	}
	wg.Wait()

	// 并发注册 + 查找混合
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			hash := fmt.Sprintf("mixed-%d", i)
			conn := newMockConn(i + n)
			r.Register(hash, conn, []byte("kc"))
		}(i)
		go func(i int) {
			defer wg.Done()
			hash := fmt.Sprintf("hash-%d", i)
			r.Lookup(hash)
		}(i)
	}
	wg.Wait()
}

func TestRegistry_ConcurrentRemoveIfMatch(t *testing.T) {
	r := NewRegistry()
	const hash = "exit-1"

	conn1 := newMockConn(1)
	r.Register(hash, conn1, []byte("kc1"))

	// 并发: 一个 goroutine 尝试 RemoveIfMatch(conn1)，另一个注册新连接
	conn2 := newMockConn(2)
	var wg sync.WaitGroup
	var removed atomic.Bool

	wg.Add(2)
	go func() {
		defer wg.Done()
		if r.RemoveIfMatch(hash, conn1) {
			removed.Store(true)
		}
	}()
	go func() {
		defer wg.Done()
		r.Register(hash, conn2, []byte("kc2"))
	}()
	wg.Wait()

	// 无论谁先执行，最终 Registry 中应该有一个有效条目
	conn, ok := r.Lookup(hash)
	if removed.Load() {
		// 如果 Remove 先执行，Register 之后会重新注册
		// 如果 Register 先执行，RemoveIfMatch 因 conn 不匹配而跳过
		// 两种情况下，conn2 都应在 Registry 中（或 Registry 为空后被 Register 填充）
		if ok && conn != conn2 {
			// 如果 RemoveIfMatch 在 Register 之前执行，Registry 先清空再被 conn2 填充
			// conn 应该是 conn2
			t.Logf("Remove 先执行，Register 后执行: conn=%v", conn)
		}
	} else {
		// RemoveIfMatch 未删除: 说明 Register 先更新了连接
		if !ok {
			t.Fatal("Registry 不应为空")
		}
		if conn != conn2 {
			t.Fatal("Registry 中应该是 conn2")
		}
	}

	// 大规模并发 RemoveIfMatch 测试
	const m = 50
	conns := make([]*mockConn, m)
	for i := 0; i < m; i++ {
		conns[i] = newMockConn(100 + i)
		r.Register(fmt.Sprintf("h-%d", i), conns[i], []byte("kc"))
	}

	wg.Add(m * 2)
	for i := 0; i < m; i++ {
		go func(i int) {
			defer wg.Done()
			r.RemoveIfMatch(fmt.Sprintf("h-%d", i), conns[i])
		}(i)
		go func(i int) {
			defer wg.Done()
			newConn := newMockConn(200 + i)
			r.Register(fmt.Sprintf("h-%d", i), newConn, []byte("kc-new"))
		}(i)
	}
	wg.Wait()
}

func TestRegistry_HeartbeatCleanup(t *testing.T) {
	r := NewRegistry()

	fresh := newMockConn(1)
	stale := newMockConn(2)

	r.Register("fresh", fresh, []byte("kc1"))
	r.Register("stale", stale, []byte("kc2"))

	// 更新 fresh 的心跳，stale 保持旧时间
	r.UpdateHeartbeat("fresh")

	// 手动修改 stale 的 LastHeartbeat 使其超时
	r.mu.Lock()
	if entry, ok := r.entries["stale"]; ok {
		entry.LastHeartbeat = time.Now().Add(-2 * time.Minute)
	}
	r.mu.Unlock()

	// 执行清理（超时 60 秒）
	r.cleanup(60 * time.Second)

	// stale 应被清理
	if _, ok := r.Lookup("stale"); ok {
		t.Fatal("stale 应已被清理")
	}

	// fresh 应该保留
	if _, ok := r.Lookup("fresh"); !ok {
		t.Fatal("fresh 不应被清理")
	}

	// stale 的连接应被关闭
	if stale.closeCalls.Load() == 0 {
		t.Fatal("stale 连接应调用 CloseWithError")
	}

	// fresh 的连接不应被关闭
	if fresh.closeCalls.Load() != 0 {
		t.Fatal("fresh 连接不应调用 CloseWithError")
	}

	if r.Count() != 1 {
		t.Fatalf("清理后应剩 1 个注册，实际 %d", r.Count())
	}
}

func TestRegistry_ReRegister(t *testing.T) {
	r := NewRegistry()
	const hash = "exit-reregister"

	conn1 := newMockConn(1)
	r.Register(hash, conn1, []byte("kc1"))

	// 确认 conn1 已注册
	got, ok := r.Lookup(hash)
	if !ok || got != conn1 {
		t.Fatal("conn1 应已注册")
	}

	// 重新注册同一 pubKeyHash 的新连接
	conn2 := newMockConn(2)
	r.Register(hash, conn2, []byte("kc2"))

	// conn1 应被关闭
	if conn1.closeCalls.Load() == 0 {
		t.Fatal("旧连接 conn1 应调用 CloseWithError")
	}

	// Registry 中应该是 conn2
	got, ok = r.Lookup(hash)
	if !ok {
		t.Fatal("重新注册后应该能找到注册")
	}
	if got != conn2 {
		t.Fatal("重新注册后 Registry 中应该是 conn2")
	}

	// conn2 不应被关闭
	if conn2.closeCalls.Load() != 0 {
		t.Fatal("新连接 conn2 不应调用 CloseWithError")
	}

	// 注册数应该仍为 1
	if r.Count() != 1 {
		t.Fatalf("重新注册后应有 1 个注册，实际 %d", r.Count())
	}
}

func TestRegistry_ListExitKeys(t *testing.T) {
	r := NewRegistry()

	r.Register("h1", newMockConn(1), []byte("kc1"))
	r.Register("h2", newMockConn(2), []byte("kc2"))
	r.Register("h3", newMockConn(3), nil) // 无 KeyConfig

	keys := r.ListExitKeys()
	if len(keys) != 2 {
		t.Fatalf("期望 2 个 ExitKeyEntry（排除空 KeyConfig），实际 %d", len(keys))
	}
}

func TestRegistry_StartCleanup(t *testing.T) {
	r := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stale := newMockConn(1)
	r.Register("stale", stale, []byte("kc"))

	// 将心跳时间设置为过去
	r.mu.Lock()
	if entry, ok := r.entries["stale"]; ok {
		entry.LastHeartbeat = time.Now().Add(-5 * time.Second)
	}
	r.mu.Unlock()

	// 启动清理，超时2秒，清理间隔1秒
	r.StartCleanup(ctx, 2*time.Second)

	// 等待清理执行
	time.Sleep(2 * time.Second)

	if _, ok := r.Lookup("stale"); ok {
		t.Fatal("stale 应已被自动清理")
	}

	if stale.closeCalls.Load() == 0 {
		t.Fatal("stale 连接应被关闭")
	}
}
