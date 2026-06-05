package task

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 远程下载（/remote-upload）的 SSRF 防护：用户给一个 URL，服务器以自身身份去请求。
// 若不校验，攻击者（需 admin）可让服务器请求内网地址、云元数据端点（169.254.169.254），
// 把本服务当成穿透内网的跳板。这里做三层防护，且重定向时对每一跳重做（见 newSafeClient）：
//
//	1. 协议白名单：只允许 http/https，挡掉 file:// gopher:// 等
//	2. 私网 IP 拦截：解析 host 的所有 IP，命中环回/私有/链路本地等一律拒绝
//	3. 逐跳校验：自定义 http.Client 的 CheckRedirect 对每个跳转目标再过一遍本函数
//
// 已知残留：DNS rebinding（校验时解析到公网 IP、实际拨号时又解析到内网 IP 的 TOCTOU）
// 未防——彻底防需在 DialContext 拨号那刻校验。当前场景下前三层已挡住实战利用的绝大多数。

// ValidateRemoteURL 校验远程下载 URL：协议必须是 http/https，且 host 解析出的
// 任一 IP 不得落在内网/环回/链路本地等敏感网段。任一不满足返回错误。
// 导出供 handler 在入队前预校验；execute 的重定向逐跳也复用本函数。
func ValidateRemoteURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL 解析失败: %w", err)
	}

	// 第 1 层：协议白名单
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("不允许的协议: %q（仅支持 http/https）", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL 缺少主机名")
	}

	// 第 2 层：解析 host 的所有 IP 并逐个校验。
	// host 本身就是 IP 字面量时 LookupIP 直接返回它；是域名则做一次 DNS 解析。
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("无法解析主机 %q: %w", host, err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("目标地址 %s 指向受限网段，拒绝请求", ip)
		}
	}
	return nil
}

// isBlockedIP 判断 IP 是否落在禁止访问的网段（内网/环回/链路本地等）。
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || // 127.0.0.0/8, ::1
		ip.IsPrivate() || // 10/8, 172.16/12, 192.168/16, fc00::/7
		ip.IsLinkLocalUnicast() || // 169.254/16（云元数据）, fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified() { // 0.0.0.0, ::
		return true
	}
	// IPv4 广播
	if ip4 := ip.To4(); ip4 != nil && ip4.Equal(net.IPv4bcast) {
		return true
	}
	return false
}

// safeDownloadClient 是远程下载专用 http.Client：
//   - CheckRedirect 对每个重定向目标重做 ValidateRemoteURL（第 3 层：防先给公网
//     URL、再 302 跳内网的绕过），并限制最多 10 跳。
//   - 不设 Timeout：远程下载可能是数小时的大文件，整体超时会误杀；取消/截断
//     由 execute 侧的 ctx 与 LimitReader 负责。空闲与建连超时由 Transport 控制。
var safeDownloadClient = &http.Client{
	Transport: &http.Transport{
		// 建连/TLS 握手超时，防慢速对端拖死 worker（不影响已建立连接的大文件传输）
		DialContext: (&net.Dialer{
			Timeout: 15 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("重定向次数过多")
		}
		return ValidateRemoteURL(req.URL.String())
	},
}
