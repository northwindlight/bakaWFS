package main

import (
	"bakaWFS/internal/auth"
	"bakaWFS/internal/config"
	"bakaWFS/internal/fileutil"
	"bakaWFS/internal/handler"
	"bakaWFS/internal/task"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lmittmann/tint"
)

func chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

func printLogo(out io.Writer) {
	logo := `
 ____          _  __          
|  _ \   /\   | |/ /   /\     
| |_) | /  \  | ' /   /  \    
|  _ < / /\ \ |  <   / /\ \   
| |_) / ____ \| . \ / ____ \  
|____/_/    \_\_|\_\_/    \_\ 
__          __ ______  _____ 
\ \        / /|  ____|/ ____|
 \ \  /\  / / | |__  | (___  
  \ \/  \/ /  |  __|  \___ \ 
   \  /\  /   | |     ____) |
    \/  \/    |_|    |_____/
`
	fmt.Fprintf(out, "%s%s%s\n", "\033[36m", logo, "\033[0m")
}

func main() {
	output := setupOutput()
	logger := slog.New(tint.NewHandler(output, &tint.Options{
		TimeFormat: "15:04:05",
	}))
	cfgPath := filepath.Join(".", "config.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		logger.Warn("配置文件不存在，已生成默认配置，生产环境请修改默认配置文件", "默认路径:", cfgPath)
		if err := config.EnsureConfig(cfgPath); err != nil {
			logger.Error("初始化配置失败", "error", err)
			waitAndExit()
		}
	}

	cfg, err := config.LoadYAML[config.Config](cfgPath)
	if err != nil {
		logger.Error("加载配置失败", "error", err)
		waitAndExit()
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("配置校验失败", "error", err)
		waitAndExit()
	}

	if _, err := os.Stat(cfg.UsersPath); err != nil {
		logger.Warn("用户配置文件不存在，已生成默认配置，请迅速修改密码，如果在公网环境，请配置HTTPS", "用户配置路径:", cfg.UsersPath)
		if err := config.EnsureUsersConfig(cfg.UsersPath); err != nil {
			logger.Error("初始化用户配置失败", "error", err)
			waitAndExit()
		}
	}

	usersCfg, err := config.LoadYAML[config.UsersConfig](cfg.UsersPath)
	if err != nil {
		logger.Error("加载用户配置失败", "error", err)
		waitAndExit()
	}

	logger.Info("配置加载成功", "address", cfg.Address,
		"https_port", cfg.HttpsPort, "http_port", cfg.HttpPort,
		"download_workers", cfg.DownloadWorkers)

	var tlsConfig *tls.Config
	if cfg.HttpsEnabled() {
		cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
		if err != nil {
			logger.Error("加载证书失败", "error", err)
			waitAndExit()
		}
		logger.Info("加载证书成功", "cert", cfg.CertPath, "key", cfg.KeyPath)
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	tempDir := filepath.Join(".", cfg.TempDir)
	if err := cleanTempDir(tempDir, logger); err != nil {
		logger.Error("清理临时目录失败", "error", err)
		waitAndExit()
	}

	authSvc := auth.NewAuth(cfg, usersCfg)
	downloader := task.NewDownloader(cfg.DownloadWorkers, logger)
	downloader.Start()

	ah := handler.NewAuthHandler(authSvc, logger)
	fh := handler.NewFileHandler(cfg, logger, downloader)

	authMW := handler.AuthMiddleware(authSvc, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/list", fh.HandleNode)
	mux.HandleFunc("/files/", handler.FileServerHandler(http.StripPrefix("/files/", http.FileServer(http.Dir(cfg.DirPath)))))
	mux.HandleFunc("/login", ah.HandleLogin)
	mux.HandleFunc("/upload", authMW(fh.HandleUpload))
	mux.HandleFunc("/verify", authMW(ah.HandleVerify))
	mux.HandleFunc("/remote-upload", authMW(fh.HandleRemoteUpload))
	mux.HandleFunc("/progress", authMW(fh.HandleProgress))
	mux.HandleFunc("/cancel", authMW(fh.HandleCancel))
	mux.HandleFunc("/upload/chunk", authMW(fh.HandleChunkUpload))
	mux.HandleFunc("/upload/merge", authMW(fh.HandleChunkMerge))
	mux.HandleFunc("/html/", handler.HtmlFileServerHandler(cfg.HtmlDir, embeddedHTML, logger))
	mux.HandleFunc("/", handler.HtmlFileServerHandler(cfg.HtmlDir, embeddedHTML, logger))

	globalHandler := chain(
		mux,
		handler.RequestLogger(logger),
		//handler.CORSMiddleware,
		handler.StatusOK,
	)
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := fileutil.CleanStaleChunks(cfg.TempDir, 24*time.Hour); err != nil {
				logger.Warn("清理过期 chunk 失败", "error", err)
			}
		}
	}()
	printLogo(output)
	logger.Info("baka文件服务器已启动")

	errCh := make(chan error, 2)

	if cfg.HttpsEnabled() {
		httpsAddr := fmt.Sprintf("%s:%d", cfg.Address, cfg.HttpsPort)
		httpsServer := &http.Server{
			Addr:      httpsAddr,
			TLSConfig: tlsConfig,
			Handler:   globalHandler,
		}
		logger.Info("HTTPS 已启动", "addr", httpsAddr)
		go func() {
			if err := httpsServer.ListenAndServeTLS("", ""); err != nil {
				errCh <- fmt.Errorf("HTTPS 服务器错误: %w", err)
			}
		}()
	}

	if cfg.HttpEnabled() {
		httpAddr := fmt.Sprintf("%s:%d", cfg.Address, cfg.HttpPort)
		var httpHandler http.Handler
		if cfg.HttpsEnabled() {
			// 两者同时开启：HTTP 重定向到 HTTPS
			var target string
			httpHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host, _, err := net.SplitHostPort(r.Host)
				if err != nil {
					host = r.Host
				}
				if cfg.HttpsPort == 443 {
					target = fmt.Sprintf("https://%s%s", host, r.RequestURI)
				} else {
					target = fmt.Sprintf("https://%s:%d%s", host, cfg.HttpsPort, r.RequestURI)
				}
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}

				http.Redirect(w, r, target, http.StatusMovedPermanently)
			})
			logger.Info("HTTP 已启动，自动重定向HTTPS", "addr", httpAddr)
		} else {
			httpHandler = globalHandler
			logger.Info("HTTP 已启动", "addr", httpAddr)
		}
		httpServer := &http.Server{
			Addr:    httpAddr,
			Handler: httpHandler,
		}
		go func() {
			if err := httpServer.ListenAndServe(); err != nil {
				errCh <- fmt.Errorf("HTTP 服务器错误: %w", err)
			}
		}()
	}

	if err := <-errCh; err != nil {
		logger.Error("服务器启动失败", "error", err)
		waitAndExit()
	}
}

// cleanTempDir 删除 tempDir 下的所有 .tmp 文件
func cleanTempDir(dir string, logger *slog.Logger) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	cleaned := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if err := os.Remove(p); err != nil {
			logger.Warn("清理临时文件失败", "file", p, "error", err)
		} else {
			cleaned++
		}
	}
	if cleaned > 0 {
		logger.Info("已清理上次残留临时文件", "count", cleaned, "dir", dir)
	}
	return nil
}

func waitAndExit() {
	fmt.Println("\n按 Ctrl+C 退出")
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
	os.Exit(1)
}
