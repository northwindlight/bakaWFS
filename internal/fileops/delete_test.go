package fileops

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteCommitAndRecovery(t *testing.T) {
	root := t.TempDir()
	source, trash := filepath.Join(root, "files", "book"), filepath.Join(root, "trash")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "page"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	// 先只运行提交阶段，模拟 rename 后进程退出、尚未物理回收。
	q := &Queue{logger: slog.New(slog.DiscardHandler), trashDir: trash, wake: make(chan struct{}, 1)}
	if err := q.detach(source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still visible: %v", err)
	}
	entries, err := os.ReadDir(trash)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staged tree: %v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(trash, entries[0].Name(), "page"))
	if err != nil || string(data) != "original" {
		t.Fatalf("tree was partially removed: %q, %v", data, err)
	}
	// 同名新目录独立于待清理旧目录，启动恢复不会误删。
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "new"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(q.logger, "", trash)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Close() // 等待启动/退出清理完成
	entries, err = os.ReadDir(trash)
	if err != nil || len(entries) != 0 {
		t.Fatalf("restart did not clean trash: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(source, "new")); err != nil {
		t.Fatal("new directory removed:", err)
	}
}

func TestDeleteRenameFailureLeavesTreeIntact(t *testing.T) {
	root := t.TempDir()
	source, trash := filepath.Join(root, "book"), filepath.Join(root, "not-a-directory")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "page"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trash, nil, 0644); err != nil {
		t.Fatal(err)
	}
	q, err := New(slog.New(slog.DiscardHandler), "", trash)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if r := q.Enqueue(Op{Type: OpDelete, Dst: source}); r.Err == nil {
		t.Fatal("expected staging error")
	}
	data, err := os.ReadFile(filepath.Join(source, "page"))
	if err != nil || string(data) != "original" {
		t.Fatalf("source changed on failure: %q, %v", data, err)
	}
}

func TestCleanupFailureRetriedWithoutRestoringSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary user permissions")
	}
	root := t.TempDir()
	source, trash := filepath.Join(root, "book"), filepath.Join(root, "trash")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "page"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	q := &Queue{logger: slog.New(slog.DiscardHandler), trashDir: trash, wake: make(chan struct{}, 1)}
	if err := q.detach(source); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(trash)
	staged := filepath.Join(trash, entries[0].Name())
	t.Cleanup(func() { os.Chmod(staged, 0700) })
	if err := os.Chmod(staged, 0500); err != nil {
		t.Fatal(err)
	}
	q.cleanTrash()
	if _, err := os.Stat(filepath.Join(staged, "page")); err != nil {
		t.Fatalf("expected retained garbage: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("source reappeared")
	}
	if err := os.Chmod(staged, 0700); err != nil {
		t.Fatal(err)
	}
	q.cleanTrash()
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("retry failed")
	}
}
