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
	if err := config.EnsureUsersConfig(usersPath); err != nil {
		t.Fatalf("EnsureUsersConfig: %v", err)
	}
	cfg, err := config.LoadYAML[config.UsersConfig](usersPath)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Username != "baka" {
		t.Error("unexpected default users config")
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
