package test

import (
	"bakaWFS/internal/auth"
	"bakaWFS/internal/config"
	"testing"
)

func newTestAuth() *auth.Auth {
	cfg := config.Config{Secret: "test-secret"}
	users := config.UsersConfig{
		Users: []config.User{
			{Username: "baka", Password: "bakabaka"},
		},
	}
	return auth.NewAuth(cfg, users)
}

func TestLoginSuccess(t *testing.T) {
	a := newTestAuth()
	token, err := a.Login(config.User{Username: "baka", Password: "bakabaka"}, "test-ua")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if token == "" {
		t.Error("token should not be empty")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	a := newTestAuth()
	_, err := a.Login(config.User{Username: "baka", Password: "wrong"}, "test-ua")
	if err == nil {
		t.Error("expected error for wrong password")
	}
}

func TestLoginUnknownUser(t *testing.T) {
	a := newTestAuth()
	_, err := a.Login(config.User{Username: "nobody", Password: "x"}, "test-ua")
	if err == nil {
		t.Error("expected error for unknown user")
	}
}

func TestVerifyToken(t *testing.T) {
	a := newTestAuth()
	token, _ := a.Login(config.User{Username: "baka", Password: "bakabaka"}, "mozilla")

	username, err := a.VerifyToken(token, "mozilla")
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if username != "baka" {
		t.Errorf("expected baka, got %s", username)
	}
}

func TestVerifyTokenWrongUA(t *testing.T) {
	a := newTestAuth()
	token, _ := a.Login(config.User{Username: "baka", Password: "bakabaka"}, "mozilla")

	_, err := a.VerifyToken(token, "chrome")
	if err == nil {
		t.Error("expected error for UA mismatch")
	}
}

func TestVerifyTokenGarbage(t *testing.T) {
	a := newTestAuth()
	_, err := a.VerifyToken("not.a.valid.token", "ua")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestRefreshToken(t *testing.T) {
	a := newTestAuth()
	token, _ := a.Login(config.User{Username: "baka", Password: "bakabaka"}, "ua")

	newToken, err := a.RefreshToken(token)
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if newToken == "" {
		t.Error("refreshed token should not be empty")
	}

	// 新 token 应仍能通过验证
	username, err := a.VerifyToken(newToken, "ua")
	if err != nil {
		t.Fatalf("verify after refresh: %v", err)
	}
	if username != "baka" {
		t.Errorf("expected baka, got %s", username)
	}
}
