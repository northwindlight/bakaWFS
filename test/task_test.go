package test

import (
	"bakaWFS/internal/fileops"
	"bakaWFS/internal/task"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestDownloader(t *testing.T, workers int) (*task.Downloader, string, string) {
	t.Helper()
	tempDir := t.TempDir()
	targetDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q, err := fileops.New(logger, "")
	if err != nil {
		t.Fatalf("fileops.New: %v", err)
	}
	d := task.NewDownloader(workers, logger, q)
	return d, tempDir, targetDir
}

// Cancel 在任务被 worker 拿走之前调用，仍应让其立即结束、
// 不会写出目标文件。修复前：占位 cancel 为 nil，Cancel 返回 true 但任务继续。
func TestDownloaderCancelBeforeWorkerPicksUp(t *testing.T) {
	// 慢响应服务器：阻塞直到客户端断开
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	// workers=0 会被规范化为 1；用 1 个 worker，先用一个长任务把它占住
	d, tempDir, targetDir := newTestDownloader(t, 1)
	d.Start()

	// 任务 A：占住唯一 worker
	if err := d.Enqueue(task.DownloadTask{
		URL:        srv.URL,
		TargetPath: filepath.Join(targetDir, "a.bin"),
		TempDir:    tempDir,
		Filename:   "a.bin",
		Username:   "u",
	}); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	<-started // worker 已开始处理 A

	// 任务 B：进入队列但还没被 worker 拿
	if err := d.Enqueue(task.DownloadTask{
		URL:        srv.URL,
		TargetPath: filepath.Join(targetDir, "b.bin"),
		TempDir:    tempDir,
		Filename:   "b.bin",
		Username:   "u",
	}); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	// 立刻取消 B —— 此时 B 还在 channel 里
	if !d.Cancel("b.bin") {
		t.Fatal("Cancel should return true for queued task")
	}
	// 取消 A 让 worker 回到 loop 处理 B
	if !d.Cancel("a.bin") {
		t.Fatal("Cancel should return true for running task")
	}

	// 给 worker 时间处理 B（应该立刻因 ctx 已 cancel 而退出）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !d.IsRunning("b.bin") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d.IsRunning("b.bin") {
		t.Fatal("task b should have been cancelled and cleaned up")
	}

	// 目标文件不应存在
	if _, err := os.Stat(filepath.Join(targetDir, "b.bin")); !os.IsNotExist(err) {
		t.Errorf("b.bin should not exist after cancel, stat err=%v", err)
	}
}

func TestDownloaderCancelUnknown(t *testing.T) {
	d, _, _ := newTestDownloader(t, 1)
	d.Start()
	if d.Cancel("nope") {
		t.Error("Cancel of unknown task should return false")
	}
}
