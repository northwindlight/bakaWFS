package test

import (
	"testing"

	"bakaWFS/internal/task"
)

// TestValidateRemoteURL 覆盖 SSRF 防护：协议白名单 + 内网/环回/元数据 IP 拦截。
// 用 IP 字面量做用例，net.LookupIP 对字面量直接返回不走真实 DNS，测试不依赖网络。
func TestValidateRemoteURL(t *testing.T) {
	blocked := []struct {
		name string
		url  string
	}{
		{"loopback v4", "http://127.0.0.1/x"},
		{"loopback v4 high", "http://127.7.7.7:8443/x"},
		{"loopback v6", "http://[::1]/x"},
		{"private 10", "http://10.0.0.5/x"},
		{"private 172", "http://172.16.3.4/x"},
		{"private 192", "http://192.168.1.1/x"},
		{"link-local metadata", "http://169.254.169.254/latest/meta-data/"},
		{"unspecified", "http://0.0.0.0/x"},
		{"scheme file", "file:///etc/passwd"},
		{"scheme gopher", "gopher://127.0.0.1:6379/x"},
		{"scheme ftp", "ftp://example.com/x"},
		{"no host", "http:///x"},
	}
	for _, c := range blocked {
		t.Run("blocked/"+c.name, func(t *testing.T) {
			if err := task.ValidateRemoteURL(c.url); err == nil {
				t.Errorf("expected %q to be rejected, but it passed", c.url)
			}
		})
	}

	allowed := []struct {
		name string
		url  string
	}{
		{"public v4", "http://1.1.1.1/x"},
		{"public v4 https", "https://8.8.8.8/file.zip"},
		{"public v6", "http://[2606:4700:4700::1111]/x"},
	}
	for _, c := range allowed {
		t.Run("allowed/"+c.name, func(t *testing.T) {
			if err := task.ValidateRemoteURL(c.url); err != nil {
				t.Errorf("expected %q to pass, got error: %v", c.url, err)
			}
		})
	}
}
