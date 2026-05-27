package test

import (
	"bakaWFS/internal/auth"
	"bakaWFS/internal/config"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

// 构造一个签名正确但 claims 缺字段或类型不对的 token，
// RefreshToken 不能 panic，应返回普通错误。
func TestRefreshTokenMalformedClaims(t *testing.T) {
	secret := []byte("test-secret")
	a := newTestAuth()

	cases := []struct {
		name   string
		claims jwt.MapClaims
	}{
		{
			name: "missing_abs_exp",
			claims: jwt.MapClaims{
				"username": "baka",
				"ua":       "ua",
				"exp":      time.Now().Add(time.Hour).Unix(),
				"iat":      time.Now().Unix(),
			},
		},
		{
			name: "abs_exp_wrong_type",
			claims: jwt.MapClaims{
				"username": "baka",
				"ua":       "ua",
				"exp":      time.Now().Add(time.Hour).Unix(),
				"iat":      time.Now().Unix(),
				"abs_exp":  "not-a-number",
			},
		},
		{
			name: "username_wrong_type",
			claims: jwt.MapClaims{
				"username": 12345,
				"ua":       "ua",
				"exp":      time.Now().Add(time.Hour).Unix(),
				"iat":      time.Now().Unix(),
				"abs_exp":  time.Now().Add(7 * 24 * time.Hour).Unix(),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := jwt.NewWithClaims(jwt.SigningMethodHS256, tc.claims)
			signed, err := tok.SignedString(secret)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("RefreshToken panicked on malformed claims: %v", r)
				}
			}()
			if _, err := a.RefreshToken(signed); err == nil {
				t.Error("expected error for malformed claims")
			}
		})
	}
}
