package netutil

import (
	"fmt"
	"strings"

	ma "github.com/multiformats/go-multiaddr"
)

// ExtractQUICAddress 从 multiaddr 列表提取 host:port 地址
// 优先返回 UDP 地址（QUIC 运行在 UDP 上），TCP 地址仅作为回退
func ExtractQUICAddress(addrs []ma.Multiaddr) string {
	var fallbackAddr string

	for _, addr := range addrs {
		addrStr := addr.String()
		parts := strings.Split(addrStr, "/")
		var ip, port string
		var isUDP bool
		for i := 0; i < len(parts)-1; i++ {
			if parts[i] == "ip4" || parts[i] == "ip6" {
				ip = parts[i+1]
			}
			if parts[i] == "udp" {
				port = parts[i+1]
				isUDP = true
			} else if parts[i] == "tcp" {
				port = parts[i+1]
			}
		}
		if ip != "" && port != "" {
			result := fmt.Sprintf("%s:%s", ip, port)
			if isUDP {
				return result // UDP 优先，直接返回
			}
			if fallbackAddr == "" {
				fallbackAddr = result
			}
		}
	}
	return fallbackAddr
}
