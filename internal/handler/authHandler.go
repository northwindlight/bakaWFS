package handler

import (
	"bakaWFS/internal/auth"
	"bakaWFS/internal/config"
	"encoding/json"
	"log/slog"
	"net/http"
)

type jwtClaims struct {
	Username string `json:"username"`
	Token    string `json:"token"`
	Role     string `json:"role"`
}

type AuthHandler struct {
	auth    *auth.Auth
	logger  *slog.Logger
	authMode bool
}

func NewAuthHandler(auth *auth.Auth, logger *slog.Logger, authMode bool) *AuthHandler {
	return &AuthHandler{auth: auth, logger: logger, authMode: authMode}
}

func (h *AuthHandler) HandleServerConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"auth_mode": h.authMode})
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
	token, role, err := h.auth.Login(creds, r.Header.Get("User-Agent"))
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		h.logger.Warn("用户登录失败", "username", creds.Username)
		return
	}
	json.NewEncoder(w).Encode(jwtClaims{Username: creds.Username, Token: token, Role: role})
	h.logger.Info("用户登录成功", "username", creds.Username, "role", role)
}

func (h *AuthHandler) HandleVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	username, _ := r.Context().Value(ContextKeyUsername).(string)
	role, _ := r.Context().Value(ContextKeyRole).(string)
	token, _ := r.Context().Value(ContextKeyToken).(string)
	newToken, err := h.auth.RefreshToken(token)
	if err != nil {
		http.Error(w, "Unauthorized: invalid or expired token", http.StatusUnauthorized)
		h.logger.Warn("token 续签失败", "error", err)
		return
	}
	json.NewEncoder(w).Encode(jwtClaims{Username: username, Token: newToken, Role: role})
	h.logger.Info("token 续签成功", "username", username)
}
