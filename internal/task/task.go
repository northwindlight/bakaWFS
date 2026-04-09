package task

import (
	"bakaWFS/internal/fileutil"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ── 进度快照（对外只读） ─────────────────────────────────────

type DownloadProgress struct {
	Username   string
	downloaded atomic.Int64
	totalSize  atomic.Int64
}

func (p *DownloadProgress) Update(downloaded, total int64) {
	p.downloaded.Store(downloaded)
	p.totalSize.Store(total)
}

func (p *DownloadProgress) Snapshot() (downloaded, total int64) {
	return p.downloaded.Load(), p.totalSize.Load()
}

// ── DownloadTask ────────────────────────────────────────────

type DownloadTask struct {
	URL        string
	TargetPath string
	TempDir    string
	Filename   string
	Username   string
}

// ── Downloader ──────────────────────────────────────────────

type Downloader struct {
	queue   chan DownloadTask
	workers int
	logger  *slog.Logger

	// filename -> *DownloadProgress，零锁读进度
	progress sync.Map
	// filename -> context.CancelFunc，用于取消
	cancels sync.Map
}

func NewDownloader(workers int, logger *slog.Logger) *Downloader {
	if workers <= 0 {
		workers = 1
	}
	return &Downloader{
		queue:   make(chan DownloadTask, workers*4),
		workers: workers,
		logger:  logger,
	}
}

func (d *Downloader) Start() {
	for i := 0; i < d.workers; i++ {
		go d.worker()
	}
	d.logger.Info("下载 worker 已启动", "workers", d.workers)
}

// IsRunning 检查某文件是否有下载任务正在进行。
func (d *Downloader) IsRunning(filename string) bool {
	_, ok := d.cancels.Load(filename)
	return ok
}

// Enqueue 登记进度、注册占位 cancel，再投入队列。
// 调用方已确认目标文件不存在。
func (d *Downloader) Enqueue(task DownloadTask) error {
	prog := &DownloadProgress{Username: task.Username}
	// 用 LoadOrStore 保证幂等：若已存在则拒绝
	if _, loaded := d.progress.LoadOrStore(task.Filename, prog); loaded {
		return errors.New("下载任务已存在")
	}
	// 存一个 nil cancel 占位，worker 启动后替换
	d.cancels.Store(task.Filename, context.CancelFunc(nil))

	select {
	case d.queue <- task:
		return nil
	default:
		d.progress.Delete(task.Filename)
		d.cancels.Delete(task.Filename)
		return errors.New("下载队列已满")
	}
}

// Cancel 取消下载，返回 false 表示任务不存在。
func (d *Downloader) Cancel(filename string) bool {
	val, ok := d.cancels.Load(filename)
	if !ok {
		return false
	}
	if cancel, _ := val.(context.CancelFunc); cancel != nil {
		cancel()
	}
	return true
}

// ListProgress 返回所有进行中下载的快照，供 /progress 使用。
func (d *Downloader) ListProgress() map[string]ProgressSnapshot {
	result := make(map[string]ProgressSnapshot)
	d.progress.Range(func(key, val any) bool {
		filename := key.(string)
		prog := val.(*DownloadProgress)
		downloaded, total := prog.Snapshot()
		result[filename] = ProgressSnapshot{
			Username:   prog.Username,
			Downloaded: downloaded,
			TotalSize:  total,
		}
		return true
	})
	return result
}

type ProgressSnapshot struct {
	Username   string
	Downloaded int64
	TotalSize  int64
}

// ── worker & execute ────────────────────────────────────────

func (d *Downloader) worker() {
	for task := range d.queue {
		ctx, cancel := context.WithCancel(context.Background())
		// 替换占位 cancel
		d.cancels.Store(task.Filename, cancel)
		d.execute(ctx, task)
		cancel() // 确保 context 资源释放
	}
}

func (d *Downloader) execute(ctx context.Context, task DownloadTask) {
	defer func() {
		d.progress.Delete(task.Filename)
		d.cancels.Delete(task.Filename)
	}()

	d.logger.Info("开始远程下载", "url", task.URL, "file", task.Filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, task.URL, nil)
	if err != nil {
		d.logger.Error("远程下载失败: 创建请求错误", "url", task.URL, "error", err)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			d.logger.Info("远程下载被取消", "file", task.Filename)
		} else {
			d.logger.Error("远程下载失败: 请求错误", "url", task.URL, "error", err)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		d.logger.Error("远程下载失败: HTTP 错误", "url", task.URL, "status", resp.StatusCode)
		return
	}

	totalSize := resp.ContentLength
	if totalSize > 100*1024*1024*1024 {
		d.logger.Warn("远程下载: 文件超过 100GB 限制", "size", totalSize)
		return
	}

	prog, _ := d.progress.Load(task.Filename)
	prog.(*DownloadProgress).Update(0, totalSize)

	tempPath := filepath.Join(
		task.TempDir,
		fmt.Sprintf("%d-remote-%s.tmp", time.Now().UnixNano(), filepath.Base(task.Filename)),
	)
	tmpFile, err := os.Create(tempPath)
	if err != nil {
		d.logger.Error("远程下载失败: 无法创建临时文件", "error", err)
		return
	}
	defer os.Remove(tempPath)

	pw := &progressWriter{
		prog:  prog.(*DownloadProgress),
		total: totalSize,
	}
	_, err = io.Copy(io.MultiWriter(tmpFile, pw), resp.Body)
	tmpFile.Close()

	if err != nil {
		if errors.Is(err, context.Canceled) {
			d.logger.Info("远程下载被取消", "file", task.Filename)
		} else {
			d.logger.Error("远程下载失败: 传输错误", "error", err)
		}
		return
	}

	// rename 前再次检查目标文件，防止下载期间已有同名文件落地
	if _, err := os.Stat(task.TargetPath); err == nil {
		d.logger.Warn("远程下载: 目标文件已存在，丢弃", "file", task.Filename)
		return
	}

	if err := fileutil.MoveFile(tempPath, task.TargetPath); err != nil {
		d.logger.Error("远程下载失败: 移动文件失败", "error", err)
		return
	}

	d.logger.Info("远程下载完成", "file", task.Filename, "size", totalSize)
}

// ── progressWriter ──────────────────────────────────────────

type progressWriter struct {
	prog    *DownloadProgress
	total   int64
	written int64
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	pw.prog.Update(pw.written, pw.total)
	return n, nil
}
