package main

import (
	"bakaWFS/internal/auth"
	"bakaWFS/internal/config"
	"bakaWFS/internal/fileops"
	"bakaWFS/internal/fileutil"
	"bakaWFS/internal/handler"
	"bakaWFS/internal/task"
	"bakaWFS/internal/thumb"
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

	"github.com/kardianos/service"
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

// program 实现 service.Interface，持有服务器停止通道
type program struct {
	stopCh chan struct{}
}

// chdirToExeDir 将工作目录切到可执行文件所在目录。
// 服务模式下 SCM 给的 CWD 是 System32，所有相对路径（config.yaml、files/、
// .uploads、.thumbcache 等）都会错位，必须在启动前纠正。
func chdirToExeDir() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Chdir(filepath.Dir(exe))
}

func (p *program) Start(s service.Service) error {
	p.stopCh = make(chan struct{})
	go p.run()
	return nil
}

func (p *program) Stop(s service.Service) error {
	close(p.stopCh)
	return nil
}

func (p *program) run() {
	// Windows 服务由 SCM 拉起时 CWD 是 System32，kardianos 的 WorkingDirectory
	// 在 Windows 上不可靠。强制切到可执行文件所在目录，确保所有相对路径正确。
	chdirToExeDir()
	output := setupOutput()
	logger := slog.New(tint.NewHandler(output, &tint.Options{
		TimeFormat: "15:04:05",
	}))
	if err := startServer(logger, output, p.stopCh); err != nil {
		logger.Error("服务器运行失败", "error", err)
	}
}

func main() {
	// 解析子命令
	var cmd string
	if len(os.Args) >= 2 {
		cmd = os.Args[1]
	}

	svcConfig := &service.Config{
		Name:        "bakaWFS",
		DisplayName: "bakaWFS File Server",
		Description: "baka self-hosted file server",
		WorkingDirectory: func() string {
			exe, err := os.Executable()
			if err != nil {
				return "."
			}
			return filepath.Dir(exe)
		}(),
	}

	prg := &program{}
	svc, err := service.New(prg, svcConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建服务失败: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "install":
		if err := svc.Install(); err != nil {
			fmt.Fprintf(os.Stderr, "安装服务失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("服务已安装，将在系统启动时自动运行")
		fmt.Println("使用 bakaWFS start 立即启动")

	case "uninstall":
		if err := svc.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "卸载服务失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("服务已卸载")

	case "start":
		if err := svc.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "启动服务失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("服务已启动")

	case "stop":
		if err := svc.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "停止服务失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("服务已停止")

	case "status":
		st, err := svc.Status()
		if err != nil {
			fmt.Fprintf(os.Stderr, "查询状态失败: %v\n", err)
			os.Exit(1)
		}
		switch st {
		case service.StatusRunning:
			fmt.Println("服务状态: 运行中")
		case service.StatusStopped:
			fmt.Println("服务状态: 已停止")
		default:
			fmt.Println("服务状态: 未安装或未知")
		}

	case "run", "":
		// 直接前台运行（默认行为）
		if service.Interactive() {
			// 前台终端运行
			output := setupOutput()
			logger := slog.New(tint.NewHandler(output, &tint.Options{
				TimeFormat: "15:04:05",
			}))
			stopCh := make(chan struct{})
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-sigCh
				close(stopCh)
			}()
			if err := startServer(logger, output, stopCh); err != nil {
				logger.Error("服务器运行失败", "error", err)
				os.Exit(1)
			}
		} else {
			// 由服务管理器拉起
			if err := svc.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "服务运行失败: %v\n", err)
				os.Exit(1)
			}
		}

	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", cmd)
		fmt.Fprintf(os.Stderr, "用法: bakaWFS [install|uninstall|start|stop|status|run]\n")
		os.Exit(1)
	}
}

func startServer(logger *slog.Logger, output io.Writer, stopCh <-chan struct{}) error {
	cfgPath := filepath.Join(".", "config.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		logger.Warn("配置文件不存在，已生成默认配置，生产环境请修改默认配置文件", "默认路径:", cfgPath)
		if err := config.EnsureConfig(cfgPath); err != nil {
			return fmt.Errorf("初始化配置失败: %w", err)
		}
	}

	cfg, err := config.LoadYAML[config.Config](cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}

	if _, err := os.Stat(cfg.UsersPath); err != nil {
		logger.Warn("用户配置文件不存在，已生成默认配置，请迅速修改密码，如果在公网环境，请配置HTTPS", "用户配置路径:", cfg.UsersPath)
		if err := config.EnsureUsersConfig(cfg.UsersPath); err != nil {
			return fmt.Errorf("初始化用户配置失败: %w", err)
		}
	}

	usersCfg, err := config.LoadYAML[config.UsersConfig](cfg.UsersPath)
	if err != nil {
		return fmt.Errorf("加载用户配置失败: %w", err)
	}

	logger.Info("配置加载成功", "address", cfg.Address,
		"https_port", cfg.HttpsPort, "http_port", cfg.HttpPort,
		"download_workers", cfg.DownloadWorkers,
		"auth_mode", cfg.AuthMode)

	var tlsConfig *tls.Config
	if cfg.HttpsEnabled() {
		cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
		if err != nil {
			return fmt.Errorf("加载证书失败: %w", err)
		}
		logger.Info("加载证书成功", "cert", cfg.CertPath, "key", cfg.KeyPath)
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	tempDir := filepath.Join(".", cfg.TempDir)
	if err := cleanTempDir(tempDir, logger); err != nil {
		return fmt.Errorf("清理临时目录失败: %w", err)
	}

	queue, err := fileops.New(logger, cfg.AuditLogPath)
	if err != nil {
		return fmt.Errorf("初始化审计日志失败: %w", err)
	}

	authSvc := auth.NewAuth(cfg, usersCfg)
	downloader := task.NewDownloader(cfg.DownloadWorkers, logger, queue)
	downloader.Start()

	thumbCacheDir := filepath.Join(".", ".thumbcache")
	thumbGen, err := thumb.New(thumbCacheDir)
	if err != nil {
		return fmt.Errorf("初始化缩略图缓存失败: %w", err)
	}

	ah := handler.NewAuthHandler(authSvc, logger, cfg.AuthMode)
	fh := handler.NewFileHandler(cfg, logger, downloader, queue, thumbGen)

	authMW := handler.AuthMiddleware(authSvc, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", ah.HandleServerConfig)
	if cfg.AuthMode {
		mux.HandleFunc("/list", authMW(fh.HandleNode))
		mux.HandleFunc("/files/", authMW(handler.FileServerHandler(http.StripPrefix("/files/", http.FileServer(http.Dir(cfg.DirPath))))))
		mux.HandleFunc("/thumb/", authMW(fh.HandleThumb))
		mux.HandleFunc("/thumbs", authMW(fh.HandleThumbBatch))
		logger.Info("鉴权模式已启用，所有接口需登录")
	} else {
		mux.HandleFunc("/list", fh.HandleNode)
		mux.HandleFunc("/files/", handler.FileServerHandler(http.StripPrefix("/files/", http.FileServer(http.Dir(cfg.DirPath)))))
		mux.HandleFunc("/thumb/", fh.HandleThumb)
		mux.HandleFunc("/thumbs", fh.HandleThumbBatch)
	}
	mux.HandleFunc("/login", ah.HandleLogin)
	mux.HandleFunc("/upload", authMW(fh.HandleUpload))
	mux.HandleFunc("/verify", authMW(ah.HandleVerify))
	mux.HandleFunc("/remote-upload", authMW(fh.HandleRemoteUpload))
	mux.HandleFunc("/progress", authMW(fh.HandleProgress))
	mux.HandleFunc("/cancel", authMW(fh.HandleCancel))
	mux.HandleFunc("/upload/chunk", authMW(fh.HandleChunkUpload))
	mux.HandleFunc("/upload/merge", authMW(fh.HandleChunkMerge))
	mux.HandleFunc("/delete", authMW(fh.HandleDelete))
	mux.HandleFunc("/rename", authMW(fh.HandleRename))
	mux.HandleFunc("/copy", authMW(fh.HandleCopy))
	mux.HandleFunc("/mkdir", authMW(fh.HandleMkdir))
	if cfg.HtmlEnabled() {
		mux.HandleFunc("/html/", handler.HtmlFileServerHandler(cfg.HtmlDir, embeddedHTML, logger))
		mux.HandleFunc("/", handler.HtmlFileServerHandler(cfg.HtmlDir, embeddedHTML, logger))
	}

	middlewares := []func(http.Handler) http.Handler{
		handler.RequestLogger(logger),
	}
	if cfg.CorsEnabled {
		middlewares = append(middlewares, handler.CORSMiddleware)
		logger.Info("CORS 跨域支持已启用")
	}
	middlewares = append(middlewares, handler.StatusOK)

	globalHandler := chain(mux, middlewares...)

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
			httpHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host, _, err := net.SplitHostPort(r.Host)
				if err != nil {
					host = r.Host
				}
				var target string
				if cfg.HttpsPort == 443 {
					target = fmt.Sprintf("https://%s%s", host, r.RequestURI)
				} else {
					target = fmt.Sprintf("https://%s:%d%s", host, cfg.HttpsPort, r.RequestURI)
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

	select {
	case err := <-errCh:
		return err
	case <-stopCh:
		logger.Info("收到停止信号，服务器退出")
		return nil
	}
}

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
