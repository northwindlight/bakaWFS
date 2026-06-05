package test

import (
	"bakaWFS/internal/fileops"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestQueue(t *testing.T) *fileops.Queue {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	q, err := fileops.New(logger, "")
	if err != nil {
		t.Fatalf("New queue: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

func TestQueueRename(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	os.WriteFile(src, []byte("hello"), 0644)

	q := newTestQueue(t)
	result := q.Enqueue(fileops.Op{
		Type:     fileops.OpRename,
		Src:      src,
		Dst:      dst,
		Username: "test",
	})
	if result.Err != nil {
		t.Fatalf("rename: %v", result.Err)
	}
	if result.Path != dst {
		t.Errorf("expected path %s, got %s", dst, result.Path)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Error("dst should exist")
	}
}

func TestQueueRenameConflict(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	os.WriteFile(src, []byte("new"), 0644)
	os.WriteFile(dst, []byte("old"), 0644) // 目标已存在

	q := newTestQueue(t)
	result := q.Enqueue(fileops.Op{
		Type:     fileops.OpRename,
		Src:      src,
		Dst:      dst,
		Username: "test",
	})
	if result.Err != nil {
		t.Fatalf("rename: %v", result.Err)
	}
	// 冲突时应生成 dst(1).txt
	if result.Path == dst {
		t.Error("should have resolved conflict with a new name")
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Error("resolved path should exist")
	}
}

func TestQueueDelete(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "delete-me.txt")
	os.WriteFile(p, []byte("x"), 0644)

	q := newTestQueue(t)
	result := q.Enqueue(fileops.Op{
		Type:     fileops.OpDelete,
		Dst:      p,
		Username: "test",
	})
	if result.Err != nil {
		t.Fatalf("delete: %v", result.Err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("file should be deleted")
	}
}

func TestQueueDeleteDir(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "sub")
	os.MkdirAll(d, 0755)
	os.WriteFile(filepath.Join(d, "f.txt"), []byte("x"), 0644)

	q := newTestQueue(t)
	result := q.Enqueue(fileops.Op{
		Type:     fileops.OpDelete,
		Dst:      d,
		Username: "test",
	})
	if result.Err != nil {
		t.Fatalf("delete dir: %v", result.Err)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Error("directory should be deleted")
	}
}

func TestQueueMkdir(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "newdir")

	q := newTestQueue(t)
	result := q.Enqueue(fileops.Op{
		Type:     fileops.OpMkdir,
		Dst:      d,
		Username: "test",
	})
	if result.Err != nil {
		t.Fatalf("mkdir: %v", result.Err)
	}
	info, err := os.Stat(d)
	if err != nil || !info.IsDir() {
		t.Error("directory should exist")
	}
}

func TestQueueMkdirNested(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "a", "b", "c")

	q := newTestQueue(t)
	result := q.Enqueue(fileops.Op{
		Type:     fileops.OpMkdir,
		Dst:      d,
		Username: "test",
	})
	if result.Err != nil {
		t.Fatalf("mkdir nested: %v", result.Err)
	}
	if _, err := os.Stat(d); err != nil {
		t.Error("nested directory should exist")
	}
}

func TestQueueSerialOrder(t *testing.T) {
	// 验证队列串行执行：rename 后立即 delete 同一个文件
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	os.WriteFile(src, []byte("x"), 0644)

	q := newTestQueue(t)

	r1 := q.Enqueue(fileops.Op{
		Type:     fileops.OpRename,
		Src:      src,
		Dst:      dst,
		Username: "test",
	})
	if r1.Err != nil {
		t.Fatalf("rename: %v", r1.Err)
	}

	r2 := q.Enqueue(fileops.Op{
		Type:     fileops.OpDelete,
		Dst:      dst,
		Username: "test",
	})
	if r2.Err != nil {
		t.Fatalf("delete: %v", r2.Err)
	}

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("file should be deleted after rename+delete sequence")
	}
}

func TestQueueAuditLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.log")

	logger := slog.New(slog.DiscardHandler)
	q, err := fileops.New(logger, logPath)
	if err != nil {
		t.Fatalf("New queue with audit: %v", err)
	}
	defer q.Close()

	p := filepath.Join(dir, "audit-test.txt")
	os.WriteFile(p, []byte("x"), 0644)

	q.Enqueue(fileops.Op{
		Type:     fileops.OpDelete,
		Dst:      p,
		Username: "baka",
	})

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "baka") || !strings.Contains(content, "delete") {
		t.Errorf("audit log missing expected fields: %s", content)
	}
}

// TestQueueAuditLogInjection 用含引号/反斜杠/换行的恶意文件名做操作，断言审计日志
// 那行仍是合法 JSON 且 path 字段精确还原——证明 JSON 注入被 json.Marshal 转义挡住。
// 旧的手工拼接版会让这行 JSON 损坏甚至被注入伪造条目。
func TestQueueAuditLogInjection(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.log")
	logger := slog.New(slog.DiscardHandler)
	q, err := fileops.New(logger, logPath)
	if err != nil {
		t.Fatalf("New queue with audit: %v", err)
	}
	defer q.Close()

	// 文件系统上合法、但对手拼 JSON 是毒药的名字
	evil := filepath.Join(dir, "ev\"il\\\n\",\"action\":\"FORGED")
	q.Enqueue(fileops.Op{
		Type:     fileops.OpMkdir,
		Dst:      evil,
		Username: "ba\"ka",
	})

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	// 审计日志是 JSONL：应当恰好一条记录（注入若成功会冒出第二条/损坏行）
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 audit line, got %d: %q", len(lines), string(data))
	}
	var rec map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("audit line not valid JSON (injection broke it): %v\nline=%q", err, lines[0])
	}
	if rec["path"] != evil {
		t.Errorf("path not faithfully recorded:\n got=%q\nwant=%q", rec["path"], evil)
	}
	if rec["user"] != "ba\"ka" {
		t.Errorf("user not faithfully recorded: got=%q", rec["user"])
	}
	if rec["action"] != "mkdir" {
		t.Errorf("action tampered: got=%q (forged injection may have leaked)", rec["action"])
	}
}

func TestQueueDeleteNonExistent(t *testing.T) {
	// os.RemoveAll 对不存在的路径不报错，队列也不应报错
	q := newTestQueue(t)
	result := q.Enqueue(fileops.Op{
		Type:     fileops.OpDelete,
		Dst:      "/nonexistent/path/file.txt",
		Username: "test",
	})
	if result.Err != nil {
		t.Errorf("RemoveAll should not error on non-existent path: %v", result.Err)
	}
}
