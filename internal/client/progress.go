package client

import (
	"fmt"
	"time"
)

// ProgressReporter 启动进度报告接口
type ProgressReporter interface {
	OnBootstrapConnecting()
	OnBootstrapConnected(connected, total int)
	OnDiscoveringRelays()
	OnRelaysDiscovered(count int)
	OnRelayProbed(addr string, latency time.Duration, selected bool)
	OnRelaySelected(addr string, latency time.Duration)
	OnFetchingExitKeys()
	OnExitKeyFetched(pubKeyHash string)
	OnReady(listenAddr string)
}

// consoleProgress 控制台进度报告器
type consoleProgress struct{}

// NewConsoleProgress 创建控制台进度报告器
func NewConsoleProgress() ProgressReporter {
	return &consoleProgress{}
}

func (p *consoleProgress) OnBootstrapConnecting() {
	fmt.Println("正在连接 DHT 网络...")
}

func (p *consoleProgress) OnBootstrapConnected(connected, total int) {
	fmt.Printf("已连接到 %d 个引导节点\n", connected)
}

func (p *consoleProgress) OnDiscoveringRelays() {
	fmt.Println("正在发现 Relay 节点...")
}

func (p *consoleProgress) OnRelaysDiscovered(count int) {
	if count > 1 {
		fmt.Printf("发现 %d 个 Relay 节点，正在测量延迟...\n", count)
	} else if count == 1 {
		fmt.Println("发现 1 个 Relay 节点")
	} else {
		fmt.Println("未发现 Relay 节点")
	}
}

func (p *consoleProgress) OnRelayProbed(addr string, latency time.Duration, selected bool) {
	if selected {
		fmt.Printf("  %s: %dms [已选择]\n", addr, latency.Milliseconds())
	} else {
		fmt.Printf("  %s: %dms\n", addr, latency.Milliseconds())
	}
}

func (p *consoleProgress) OnRelaySelected(addr string, latency time.Duration) {
	fmt.Printf("已选择最佳 Relay (%dms)\n", latency.Milliseconds())
}

func (p *consoleProgress) OnFetchingExitKeys() {
	fmt.Println("正在获取 Exit 公钥...")
}

func (p *consoleProgress) OnExitKeyFetched(pubKeyHash string) {
	if len(pubKeyHash) > 12 {
		pubKeyHash = pubKeyHash[:12] + "..."
	}
	fmt.Printf("已获取 Exit 公钥 (Hash: %s)\n", pubKeyHash)
}

func (p *consoleProgress) OnReady(listenAddr string) {
	fmt.Printf("就绪! 监听 %s\n", listenAddr)
}

// silentProgress 静默进度报告器（用于静态模式）
type silentProgress struct{}

// NewSilentProgress 创建静默进度报告器
func NewSilentProgress() ProgressReporter {
	return &silentProgress{}
}

func (p *silentProgress) OnBootstrapConnecting()                        {}
func (p *silentProgress) OnBootstrapConnected(connected, total int)     {}
func (p *silentProgress) OnDiscoveringRelays()                          {}
func (p *silentProgress) OnRelaysDiscovered(count int)                  {}
func (p *silentProgress) OnRelayProbed(addr string, latency time.Duration, selected bool) {
}
func (p *silentProgress) OnRelaySelected(addr string, latency time.Duration) {}
func (p *silentProgress) OnFetchingExitKeys()                            {}
func (p *silentProgress) OnExitKeyFetched(pubKeyHash string)             {}
func (p *silentProgress) OnReady(listenAddr string)                      {}
