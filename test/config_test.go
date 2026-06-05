package test

import (
	"bakaWFS/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := config.EnsureConfig(cfgPath); err != nil {
		t.Fatalf("EnsureConfig: %v", err)
	}
	cfg, err := config.LoadYAML[config.Config](cfgPath)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if cfg.Address != "0.0.0.0" {
		t.Errorf("unexpected address: %s", cfg.Address)
	}
	if cfg.HttpPort != 8080 {
		t.Errorf("unexpected http_port: %d", cfg.HttpPort)
	}
}

func TestConfigValidateSecret(t *testing.T) {
	cfg := config.Config{Secret: "", HttpPort: 8080, HttpsPort: -1}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty secret")
	}
}

func TestConfigValidateSamePort(t *testing.T) {
	cfg := config.Config{Secret: "x", HttpPort: 80, HttpsPort: 80}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for same port")
	}
}

func TestConfigPortDisabled(t *testing.T) {
	cfg := config.Config{HttpsPort: -1, HttpPort: 8080, Secret: "x"}
	if cfg.HttpsEnabled() {
		t.Error("HttpsEnabled should be false when port is -1")
	}
	if !cfg.HttpEnabled() {
		t.Error("HttpEnabled should be true")
	}
}

func TestConfigHtmlEnabled(t *testing.T) {
	cfg := config.Config{HtmlDir: "built-in"}
	if !cfg.HtmlEnabled() {
		t.Error("HtmlEnabled should be true for built-in")
	}
	cfg.HtmlDir = ""
	if cfg.HtmlEnabled() {
		t.Error("HtmlEnabled should be false for empty string")
	}
}

func TestEnsureUsersConfig(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	adminPw, err := config.EnsureUsersConfig(usersPath)
	if err != nil {
		t.Fatalf("EnsureUsersConfig: %v", err)
	}
	if adminPw == "" || strings.Contains(adminPw, "%%") {
		t.Errorf("admin password should be randomly generated, got %q", adminPw)
	}
	cfg, err := config.LoadYAML[config.UsersConfig](usersPath)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	var baka, admin *config.User
	for i := range cfg.Users {
		switch cfg.Users[i].Username {
		case "baka":
			baka = &cfg.Users[i]
		case "admin":
			admin = &cfg.Users[i]
		}
	}
	if baka == nil || baka.Role != "guest" {
		t.Error("expected baka with role guest")
	}
	if admin == nil || admin.Role != "admin" {
		t.Error("expected admin with role admin")
	}
	if admin != nil && admin.Password != adminPw {
		t.Errorf("admin password in file (%q) should match returned (%q)", admin.Password, adminPw)
	}
	// 占位符必须已被替换
	if admin != nil && strings.Contains(admin.Password, "%%") {
		t.Error("admin password placeholder not replaced")
	}

	// 文件已存在时应跳过、返回空串
	pw2, err := config.EnsureUsersConfig(usersPath)
	if err != nil {
		t.Fatalf("EnsureUsersConfig (existing): %v", err)
	}
	if pw2 != "" {
		t.Errorf("expected empty password when file exists, got %q", pw2)
	}
}

func TestSecretGeneration(t *testing.T) {
	// EnsureConfig 内部调用 generateSecret，验证生成的是 32 位 hex
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	config.EnsureConfig(cfgPath)
	data, _ := os.ReadFile(cfgPath)
	content := string(data)
	if !strings.Contains(content, "secret: ") {
		t.Error("config should contain secret field")
	}
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "secret:") {
			parts := strings.SplitN(line, "\"", 3)
			if len(parts) >= 2 && len(parts[1]) == 32 {
				return // 32 位 hex secret，正确
			}
		}
	}
	t.Error("expected 32-char hex secret in config")
}
