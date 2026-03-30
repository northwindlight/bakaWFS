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
	"time"
)

// ── 任务类型 ────────────────────────────────────────────────

type TaskType string

const (
	TaskUpload   TaskType = "upload"
	TaskDownload TaskType = "download"
)

// Task 代表一个正在进行的上传或下载任务。
type Task struct {
	Filename   string
	Username   string
	Type       TaskType
	Downloaded int64
	TotalSize  int64 // -1 表示未知
	cancel     context.CancelFunc
}

// ── TaskManager ─────────────────────────────────────────────

// TaskManager 管理所有进行中的任务，是唯一的并发状态持有者。
type TaskManager struct {
	mu    sync.Mutex
	tasks map[string]*Task
}

func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks: make(map[string]*Task),
	}
}

// TryAdd 尝试注册任务，如果同名任务已存在则返回 false（幂等保护）。
func (m *TaskManager) TryAdd(filename, username string, typ TaskType) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tasks[filename]; exists {
		return false
	}
	m.tasks[filename] = &Task{
		Filename:  filename,
		Username:  username,
		Type:      typ,
		TotalSize: -1,
	}
	return true
}

// SetCancel 绑定 context cancel 函数，worker 启动后调用。
func (m *TaskManager) SetCancel(filename string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[filename]; ok {
		t.cancel = cancel
	}
}

// UpdateProgress 更新下载进度。
func (m *TaskManager) UpdateProgress(filename string, downloaded, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[filename]; ok {
		t.Downloaded = downloaded
		t.TotalSize = total
	}
}

// Remove 任务完成或失败后清除记录。
func (m *TaskManager) Remove(filename string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, filename)
}

// Cancel 取消任务并从 map 中删除，返回 false 表示任务不存在。
func (m *TaskManager) Cancel(filename string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[filename]
	if !ok {
		return false
	}
	if t.cancel != nil {
		t.cancel()
	}
	delete(m.tasks, filename)
	return true
}

// ListDownloads 返回所有下载任务的快照，供 /progress 接口使用。
func (m *TaskManager) ListDownloads() []TaskSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]TaskSnapshot, 0, len(m.tasks))
	for _, t := range m.tasks {
		if t.Type == TaskDownload {
			result = append(result, TaskSnapshot{
				Filename:   t.Filename,
				Username:   t.Username,
				Downloaded: t.Downloaded,
				TotalSize:  t.TotalSize,
			})
		}
	}
	return result
}

// TaskSnapshot 是对外暴露的只读任务状态，不含 cancel 函数。
type TaskSnapshot struct {
	Filename   string
	Username   string
	Downloaded int64
	TotalSize  int64
}

// ── DownloadTask ────────────────────────────────────────────

// DownloadTask 是 FileHandler 校验完毕后交给 Downloader 的任务描述。
// 路径校验、文件存在检查由 FileHandler 负责，Downloader 只管 I/O。
type DownloadTask struct {
	URL        string
	TargetPath string // 完整绝对路径，由 FileHandler 计算好
	TempDir    string // 临时目录
	Filename   string // 用于进度追踪的 key（相对路径）
	Username   string
}

// ── Downloader ──────────────────────────────────────────────

// Downloader 持有任务队列，管理固定数量的 worker goroutine。
// worker 数量在 Start() 时确定，运行期间不变。
type Downloader struct {
	tasks   *TaskManager
	queue   chan DownloadTask
	workers int
	logger  *slog.Logger
}

// NewDownloader 创建 Downloader，workers 从 config.DownloadWorkers 传入。
// queueSize 建议设为 workers 的 2-4 倍，防止 handler 被阻塞。
func NewDownloader(tasks *TaskManager, workers int, logger *slog.Logger) *Downloader {
	if workers <= 0 {
		workers = 1
	}
	return &Downloader{
		tasks:   tasks,
		queue:   make(chan DownloadTask, workers*4),
		workers: workers,
		logger:  logger,
	}
}

// Start 启动 worker goroutine，应在 main 里调用一次。
func (d *Downloader) Start() {
	for i := 0; i < d.workers; i++ {
		go d.worker()
	}
	d.logger.Info("下载 worker 已启动", "workers", d.workers)
}

// Enqueue 将任务投入队列，队列满时非阻塞返回错误。
// FileHandler 在调用 Enqueue 之前已通过 TaskManager.TryAdd 登记任务。
func (d *Downloader) Enqueue(task DownloadTask) error {
	select {
	case d.queue <- task:
		return nil
	default:
		return errors.New("下载队列已满")
	}
}

func (d *Downloader) worker() {
	for task := range d.queue {
		ctx, cancel := context.WithCancel(context.Background())
		d.tasks.SetCancel(task.Filename, cancel)
		d.execute(ctx, task)
	}
}

// execute 执行单次下载，所有路径已由 FileHandler 校验，此处只做 I/O。
func (d *Downloader) execute(ctx context.Context, task DownloadTask) {
	defer d.tasks.Remove(task.Filename)

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
	d.tasks.UpdateProgress(task.Filename, 0, totalSize)

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
		filename: task.Filename,
		total:    totalSize,
		tasks:    d.tasks,
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

	if err := fileutil.MoveFile(tempPath, task.TargetPath); err != nil {
		d.logger.Error("远程下载失败: 移动文件失败", "error", err)
		return
	}

	d.logger.Info("远程下载完成", "file", task.Filename, "size", totalSize)
}

// ── progressWriter ──────────────────────────────────────────

type progressWriter struct {
	filename string
	total    int64
	written  int64
	tasks    *TaskManager
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	pw.tasks.UpdateProgress(pw.filename, pw.written, pw.total)
	return n, nil
}
