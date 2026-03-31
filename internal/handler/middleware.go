package handler

import (
	"bakaWFS/internal/auth"
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type contextKey string

const ContextKeyUsername contextKey = "username"

// FileServerHandler 限制只允许 GET。
func FileServerHandler(fs http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		fs.ServeHTTP(w, r)
	}
}

// HtmlFileServerHandler 服务 html 目录，根路径重定向到 index.html。
func HtmlFileServerHandler(htmlDir string, embedded embed.FS, logger *slog.Logger) http.HandlerFunc {
	var fsys http.FileSystem

	if htmlDir == "built-in" || htmlDir == "internal" {
		sub, err := fs.Sub(embedded, "html")
		if err != nil {
			panic("embedded html not found: " + err.Error())
		}
		fsys = http.FS(sub)
	} else {
		fsys = http.Dir(htmlDir)
	}

	fs := http.FileServer(fsys)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/html/index.html", http.StatusFound)
			return
		}
		if r.URL.Path == "/favicon.ico" {
			http.Redirect(w, r, "/html/favicon.ico", http.StatusFound)
			return
		}
		http.StripPrefix("/html/", fs).ServeHTTP(w, r)
	}
}

// AuthMiddleware 验证 JWT。
func AuthMiddleware(authSvc *auth.Auth, logger *slog.Logger) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tokenString := extractToken(r)
			if tokenString == "" {
				http.Error(w, "Unauthorized: missing token", http.StatusUnauthorized)
				logger.Warn("请求缺少 token", "ip", clientIP(r), "path", r.URL.Path)
				return
			}
			username, err := authSvc.VerifyToken(tokenString)
			if err != nil {
				http.Error(w, "Unauthorized: invalid token", http.StatusUnauthorized)
				logger.Warn("无效 token", "ip", clientIP(r), "path", r.URL.Path, "error", err)
				return
			}
			ctx := context.WithValue(r.Context(), ContextKeyUsername, username)
			next(w, r.WithContext(ctx))
		}
	}
}

// RequestLogger 记录每次请求，/progress 除外。
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/progress" {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logFn := logger.Info
			if rec.status >= 400 {
				logFn = logger.Warn
			}
			logFn("request",
				"method", r.Method,
				"path", r.URL.Path,
				"ip", clientIP(r),
				"status", rec.status,
				"duration", time.Since(start).Round(time.Microsecond).String(),
			)
		})
	}
}

// CORSMiddleware 添加跨域响应头。
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range, Authorization, X-Upload-Filename, X-Upload-SHA256, X-Upload-Size")
		next.ServeHTTP(w, r)
	})
}

// StatusOK 处理 OPTIONS 预检请求。
func StatusOK(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func clientIP(r *http.Request) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func extractToken(r *http.Request) string {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && parts[0] == "Bearer" {
			return parts[1]
		}
		return authHeader
	}
	return r.URL.Query().Get("token")
}
