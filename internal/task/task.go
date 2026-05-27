package task

import (
	"bakaWFS/internal/fileops"
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
	ctx        context.Context
}

// ── Downloader ──────────────────────────────────────────────

type Downloader struct {
	queue     chan DownloadTask
	workers   int
	logger    *slog.Logger
	fileQueue *fileops.Queue

	// filename -> *DownloadProgress，零锁读进度
	progress sync.Map
	// filename -> context.CancelFunc，用于取消
	cancels sync.Map
}

func NewDownloader(workers int, logger *slog.Logger, fileQueue *fileops.Queue) *Downloader {
	if workers <= 0 {
		workers = 1
	}
	return &Downloader{
		queue:     make(chan DownloadTask, workers*4),
		workers:   workers,
		logger:    logger,
		fileQueue: fileQueue,
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

// Enqueue 登记进度、注册 cancel，再投入队列。
// 调用方已确认目标文件不存在。
func (d *Downloader) Enqueue(task DownloadTask) error {
	prog := &DownloadProgress{Username: task.Username}
	// 用 LoadOrStore 保证幂等：若已存在则拒绝
	if _, loaded := d.progress.LoadOrStore(task.Filename, prog); loaded {
		return errors.New("下载任务已存在")
	}
	ctx, cancel := context.WithCancel(context.Background())
	task.ctx = ctx
	d.cancels.Store(task.Filename, cancel)

	select {
	case d.queue <- task:
		return nil
	default:
		cancel()
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
	cancel, ok := val.(context.CancelFunc)
	if !ok || cancel == nil {
		return false
	}
	cancel()
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
		ctx := task.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		d.execute(ctx, task)
		// 释放该任务占用的 cancel 资源
		if val, ok := d.cancels.Load(task.Filename); ok {
			if cancel, _ := val.(context.CancelFunc); cancel != nil {
				cancel()
			}
		}
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

	const maxRemoteSize int64 = 100 * 1024 * 1024 * 1024
	totalSize := resp.ContentLength
	if totalSize > maxRemoteSize {
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
	// LimitReader 兜底：chunked 响应 ContentLength 为 -1 时仍能强制限制实际字节
	limited := io.LimitReader(resp.Body, maxRemoteSize+1)
	written, err := io.Copy(io.MultiWriter(tmpFile, pw), limited)
	tmpFile.Close()

	if err != nil {
		if errors.Is(err, context.Canceled) {
			d.logger.Info("远程下载被取消", "file", task.Filename)
		} else {
			d.logger.Error("远程下载失败: 传输错误", "error", err)
		}
		return
	}
	if written > maxRemoteSize {
		d.logger.Warn("远程下载: 实际传输超过 100GB 限制", "written", written)
		return
	}

	result := d.fileQueue.Enqueue(fileops.Op{
		Type:     fileops.OpRename,
		Src:      tempPath,
		Dst:      task.TargetPath,
		Username: task.Username,
	})
	if result.Err != nil {
		d.logger.Error("远程下载失败: 移动文件失败", "error", result.Err)
		return
	}

	d.logger.Info("远程下载完成", "file", result.Path, "size", totalSize)
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
