package scf

import (
	"bakaWFS/internal/auth"
	"bakaWFS/internal/config"
	"bakaWFS/internal/handler"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// LoadEnvironment reads only explicitly supplied function settings. It never
// loads local production config, generates users, or writes the function disk.
func LoadEnvironment(getenv func(string) string) (config.Config, config.UsersConfig, error) {
	cfg := config.Config{Secret: getenv("BAKAWFS_JWT_SECRET"), AuthMode: true}
	var users config.UsersConfig
	if strings.TrimSpace(cfg.Secret) == "" {
		return cfg, users, fmt.Errorf("BAKAWFS_JWT_SECRET 未配置")
	}
	for _, setting := range []struct {
		name  string
		value *bool
	}{{"BAKAWFS_AUTH_MODE", &cfg.AuthMode}, {"BAKAWFS_MANGA_MODE", &cfg.MangaMode}} {
		if raw := getenv(setting.name); raw != "" {
			value, err := strconv.ParseBool(raw)
			if err != nil {
				return cfg, users, fmt.Errorf("%s 必须是布尔值", setting.name)
			}
			*setting.value = value
		}
	}
	decoder := yaml.NewDecoder(strings.NewReader(getenv("BAKAWFS_USERS_YAML")))
	decoder.KnownFields(true)
	if err := decoder.Decode(&users); err != nil {
		// YAML decoder errors may quote input, including passwords.
		return cfg, users, fmt.Errorf("BAKAWFS_USERS_YAML 缺失或格式错误")
	}
	if len(users.Users) == 0 {
		return cfg, users, fmt.Errorf("BAKAWFS_USERS_YAML 至少需要一个用户")
	}
	names := make(map[string]bool)
	for _, user := range users.Users {
		if strings.TrimSpace(user.Username) == "" || user.Password == "" || names[user.Username] {
			return cfg, users, fmt.Errorf("BAKAWFS_USERS_YAML 含空账号、空口令或重复账号")
		}
		names[user.Username] = true
	}
	return cfg, users, nil
}

// NewBootstrap initializes on the first invocation: scf-go-lib can populate
// environment variables from InvokeRequest.Environment before calling us.
func NewBootstrap(getenv func(string) string, logger *slog.Logger) http.Handler {
	initialize := sync.OnceValue(func() http.Handler {
		cfg, users, err := LoadEnvironment(getenv)
		if err != nil {
			logger.Error("SCF 配置无效", "error", err)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				json.NewEncoder(w).Encode(map[string]string{"error": "function_not_configured"})
			})
		}
		return newControlAPI(cfg, users, logger)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initialize().ServeHTTP(w, r)
	})
}

func newControlAPI(cfg config.Config, users config.UsersConfig, logger *slog.Logger) http.Handler {
	authSvc := auth.NewAuth(cfg, users)
	ah := handler.NewAuthHandler(authSvc, logger, cfg.AuthMode, cfg.MangaMode)
	authMW := handler.AuthMiddleware(authSvc, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "storage_not_configured"})
	})
	mux.HandleFunc("GET /api/config", ah.HandleServerConfig)
	mux.HandleFunc("/login", ah.HandleLogin)
	mux.HandleFunc("/verify", authMW(ah.HandleVerify))
	return handler.RequestLogger(logger)(handler.CORSMiddleware(handler.StatusOK(mux)))
}

func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"bootstrap"}`))
}

// NewProbe is a separate empty-workload function for Go/Node runtime comparisons.
func NewProbe() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	return mux
}
