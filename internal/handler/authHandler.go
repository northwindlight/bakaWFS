package handler

import (
	"bakaWFS/internal/auth"
	"bakaWFS/internal/config"
	"bakaWFS/internal/dto"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

type AuthHandler struct {
	auth   *auth.Auth
	logger *slog.Logger
}

func NewAuthHandler(auth *auth.Auth, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{auth: auth, logger: logger}
}

func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024)
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var creds config.User
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	token, err := h.auth.Login(creds)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.logger.Warn("用户登录失败", "username", creds.Username)
		return
	}
	json.NewEncoder(w).Encode(dto.JwtClaims{Username: creds.Username, Token: token})
	h.logger.Info("用户登录成功", "username", creds.Username)
}

func (h *AuthHandler) HandleVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	tokenString := ExtractToken(r)
	if tokenString == "" {
		http.Error(w, "Unauthorized: missing token", http.StatusUnauthorized)
		return
	}
	newToken, err := h.auth.RefreshToken(tokenString)
	if err != nil {
		http.Error(w, "Unauthorized: invalid or expired token", http.StatusUnauthorized)
		h.logger.Warn("token 续签失败", "error", err)
		return
	}
	username, _ := h.auth.VerifyToken(newToken)
	json.NewEncoder(w).Encode(dto.JwtClaims{Username: username, Token: newToken})
	h.logger.Info("token 续签成功", "username", username)
}

// ExtractToken 从 Authorization header 或 query param 提取 token。
func ExtractToken(r *http.Request) string {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && parts[0] == "Bearer" {
			return parts[1]
		}
		return authHeader
	}
	return r.URL.Query().Get("token")
}
