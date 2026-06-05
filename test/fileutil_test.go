package test

import (
	"bakaWFS/internal/fileutil"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		valid bool
	}{
		{"simple", "file.txt", true},
		{"subdir", "sub/dir/file.txt", true},
		{"dotfile", ".hidden", true},
		{"dotdot_in_name", "my..backup.txt", true},
		{"dotdot_segment_in_path", "sub/my..backup/file.txt", true},
		{"absolute", "/etc/passwd", false},
		{"parent_traversal", "../etc/passwd", false},
		{"nested_traversal", "sub/../../etc/passwd", false},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fileutil.ValidatePath(tt.path)
			if tt.valid && err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
			if !tt.valid && err == nil {
				t.Errorf("expected invalid for path %q", tt.path)
			}
		})
	}
}

func TestChunkTempPath(t *testing.T) {
	p0 := fileutil.ChunkTempPath("/tmp/u", "sub/file.txt", 0)
	p1 := fileutil.ChunkTempPath("/tmp/u", "sub/file.txt", 1)
	if p0 == p1 {
		t.Error("different chunk indices should produce different paths")
	}
	pA := fileutil.ChunkTempPath("/tmp/u", "a.txt", 0)
	pB := fileutil.ChunkTempPath("/tmp/u", "b.txt", 0)
	if pA == pB {
		t.Error("different filenames should produce different chunk paths")
	}
	if filepath.Ext(p0) != ".chunk" {
		t.Errorf("expected .chunk extension, got %s", filepath.Ext(p0))
	}
}

func TestFindMissingChunks(t *testing.T) {
	dir := t.TempDir()
	for _, i := range []int{0, 2} {
		os.WriteFile(fileutil.ChunkTempPath(dir, "test.bin", i), []byte{byte(i)}, 0644)
	}
	missing := fileutil.FindMissingChunks(dir, "test.bin", 3)
	if len(missing) != 1 || missing[0] != 1 {
		t.Errorf("expected [1], got %v", missing)
	}
}

func TestMergeChunks(t *testing.T) {
	dir := t.TempDir()
	const total = 3
	for i := 0; i < total; i++ {
		os.WriteFile(fileutil.ChunkTempPath(dir, "test.bin", i), []byte{byte(i)}, 0644)
	}
	mergePath, hash, err := fileutil.MergeChunks(dir, "test.bin", total)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	defer os.Remove(mergePath)
	if hash == "" {
		t.Error("hash should not be empty")
	}
	data, _ := os.ReadFile(mergePath)
	if len(data) != total {
		t.Errorf("merged size %d, expected %d", len(data), total)
	}
}

func TestMergeChunksMissing(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(fileutil.ChunkTempPath(dir, "test.bin", 0), []byte{0}, 0644)
	_, _, err := fileutil.MergeChunks(dir, "test.bin", 2)
	if err == nil {
		t.Error("expected error for missing chunk")
	}
}

func TestDeleteChunks(t *testing.T) {
	dir := t.TempDir()
	const total = 3
	for i := 0; i < total; i++ {
		os.WriteFile(fileutil.ChunkTempPath(dir, "test.bin", i), []byte{byte(i)}, 0644)
	}
	fileutil.DeleteChunks(dir, "test.bin", total)
	for i := 0; i < total; i++ {
		if _, err := os.Stat(fileutil.ChunkTempPath(dir, "test.bin", i)); !os.IsNotExist(err) {
			t.Errorf("chunk %d should have been deleted", i)
		}
	}
}

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "sub", "dst.txt")
	content := []byte("hello")
	os.WriteFile(src, content, 0644)

	if err := fileutil.MoveFile(src, dst); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source should be removed")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(content) {
		t.Errorf("content mismatch")
	}
}

func TestMoveFileSameDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	os.WriteFile(src, []byte("cross"), 0644)
	dst := filepath.Join(dir, "b.txt")
	if err := fileutil.MoveFile(src, dst); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Error("dst should exist")
	}
}

func TestCalculateFilexxhash(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.bin")
	os.WriteFile(p, []byte("hello"), 0644)
	h1, err := fileutil.CalculateFilexxhash(p)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h2, _ := fileutil.CalculateFilexxhash(p)
	if h1 != h2 {
		t.Error("same file should produce same hash")
	}
}

func TestScanDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("b"), 0644)

	node, err := fileutil.ScanDir(dir)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if node.Type != "dir" || len(node.Children) != 2 {
		t.Errorf("expected dir with 2 children, got type=%s children=%d", node.Type, len(node.Children))
	}
}

// TestScanDirDepth 验证浅扫描的层数控制：limit=2 展开到第二层即止，
// 更深的目录返回不带 children 的节点（懒加载浏览靠它，避免一次拉全树）。
func TestScanDirDepth(t *testing.T) {
	// 构造 root/L1/L2/L3，每层一个子目录 + 一个文件
	root := t.TempDir()
	l1 := filepath.Join(root, "L1")
	l2 := filepath.Join(l1, "L2")
	l3 := filepath.Join(l2, "L3")
	os.MkdirAll(l3, 0755)
	os.WriteFile(filepath.Join(l1, "f1.txt"), []byte("1"), 0644)
	os.WriteFile(filepath.Join(l2, "f2.txt"), []byte("2"), 0644)
	os.WriteFile(filepath.Join(l3, "f3.txt"), []byte("3"), 0644)

	// 找指定名字的子节点
	find := func(n *fileutil.Node, name string) *fileutil.Node {
		for _, c := range n.Children {
			if c.Name == name {
				return c
			}
		}
		return nil
	}

	// limit=1：只展开当前层，L1 是目录但不展开其内容
	d1, err := fileutil.ScanDirDepth(root, 1)
	if err != nil {
		t.Fatalf("ScanDirDepth limit=1: %v", err)
	}
	l1n := find(d1, "L1")
	if l1n == nil || l1n.Type != "dir" {
		t.Fatalf("limit=1: L1 应为 dir 节点")
	}
	if len(l1n.Children) != 0 {
		t.Errorf("limit=1: L1 不应展开内容，却有 %d 个 children", len(l1n.Children))
	}

	// limit=2：展开两层，L1 内容可见(L2 + f1.txt)，但 L2 不再展开
	d2, err := fileutil.ScanDirDepth(root, 2)
	if err != nil {
		t.Fatalf("ScanDirDepth limit=2: %v", err)
	}
	l1n = find(d2, "L1")
	if l1n == nil || len(l1n.Children) != 2 {
		t.Fatalf("limit=2: L1 应展开 2 个 children(L2+f1.txt)，得到 %v", l1n)
	}
	l2n := find(l1n, "L2")
	if l2n == nil || l2n.Type != "dir" {
		t.Fatalf("limit=2: 应见 L2 目录节点")
	}
	if len(l2n.Children) != 0 {
		t.Errorf("limit=2: L2 不应展开(超出 limit)，却有 %d 个 children", len(l2n.Children))
	}

	// limit<1 归一为 1（防御性）
	d0, err := fileutil.ScanDirDepth(root, 0)
	if err != nil {
		t.Fatalf("ScanDirDepth limit=0: %v", err)
	}
	if l := find(d0, "L1"); l == nil || len(l.Children) != 0 {
		t.Errorf("limit=0 应等价 limit=1：L1 不展开")
	}
}

// ScanDir 应跟随软链目录（os.Stat 语义），把它当普通目录遍历。
func TestScanDirFollowsSymlinkDir(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.MkdirAll(real, 0755)
	os.WriteFile(filepath.Join(real, "x.txt"), []byte("x"), 0644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	node, err := fileutil.ScanDir(link)
	if err != nil {
		t.Fatalf("ScanDir on symlink: %v", err)
	}
	if node.Type != "dir" || len(node.Children) != 1 {
		t.Errorf("expected symlinked dir with 1 child, got type=%s children=%d", node.Type, len(node.Children))
	}
}

// 软链环（子目录软链指回祖先）不应导致无限递归/爆栈。
func TestScanDirSymlinkCycle(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(a, "b")
	os.MkdirAll(b, 0755)
	os.WriteFile(filepath.Join(a, "file.txt"), []byte("hi"), 0644)
	// a/b/loop -> a  形成环
	if err := os.Symlink(a, filepath.Join(b, "loop")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	done := make(chan struct{})
	var node *fileutil.Node
	var err error
	go func() {
		node, err = fileutil.ScanDir(a)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ScanDir did not terminate on symlink cycle (likely infinite recursion)")
	}
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if node.Type != "dir" {
		t.Errorf("expected dir node, got %s", node.Type)
	}
}

// 同级两个软链指向同一真实目录（非环）应都被正常遍历，不被误判为环跳过。
func TestScanDirDuplicateSymlinkNotCycle(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.MkdirAll(real, 0755)
	os.WriteFile(filepath.Join(real, "x.txt"), []byte("x"), 0644)
	if err := os.Symlink(real, filepath.Join(dir, "l1")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	os.Symlink(real, filepath.Join(dir, "l2"))

	node, err := fileutil.ScanDir(dir)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	// real, l1, l2 三个目录节点，各含 1 个子文件
	dirChildren := 0
	for _, c := range node.Children {
		if c.Type == "dir" {
			dirChildren++
			if len(c.Children) != 1 {
				t.Errorf("symlinked/real dir %q should have 1 child, got %d", c.Name, len(c.Children))
			}
		}
	}
	if dirChildren != 3 {
		t.Errorf("expected 3 dir entries (real, l1, l2), got %d", dirChildren)
	}
}

func TestScanDirFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "single.txt")
	os.WriteFile(p, []byte("hi"), 0644)
	node, err := fileutil.ScanDir(p)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if node.Type != "file" || node.Size != 2 {
		t.Errorf("expected file size 2, got type=%s size=%d", node.Type, node.Size)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	content := []byte("copy me")
	os.WriteFile(src, content, 0644)

	if err := fileutil.CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch: got %q, want %q", string(got), string(content))
	}

	// source should still exist
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Error("source should not be removed after copy")
	}
}

func TestCopyFileNotFound(t *testing.T) {
	if err := fileutil.CopyFile("/nonexistent", "/tmp/out"); err == nil {
		t.Error("expected error for nonexistent source")
	}
}

func TestCopyDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "srcdir")
	dst := filepath.Join(dir, "dstdir")

	// create source tree: srcdir/
	//   a.txt
	//   sub/
	//     b.txt
	os.MkdirAll(filepath.Join(src, "sub"), 0755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("top"), 0644)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("nested"), 0644)

	if err := fileutil.CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	// check structure
	for _, p := range []string{
		filepath.Join(dst, "a.txt"),
		filepath.Join(dst, "sub"),
		filepath.Join(dst, "sub", "b.txt"),
	} {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			t.Errorf("expected %s to exist", p)
		}
	}

	// check content
	b, _ := os.ReadFile(filepath.Join(dst, "a.txt"))
	if string(b) != "top" {
		t.Errorf("a.txt: got %q, want %q", string(b), "top")
	}
	b, _ = os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if string(b) != "nested" {
		t.Errorf("b.txt: got %q, want %q", string(b), "nested")
	}

	// source should still exist
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Error("source dir should not be removed")
	}
}

func TestCopyDirNotDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "file.txt")
	os.WriteFile(src, []byte("x"), 0644)
	if err := fileutil.CopyDir(src, filepath.Join(dir, "out")); err == nil {
		t.Error("expected error when source is not a directory")
	}
}

func TestCleanStaleChunks(t *testing.T) {
	dir := t.TempDir()

	// create 3 chunk files
	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("abc-%d.chunk", i)), []byte{byte(i)}, 0644)
	}
	// create a non-chunk file that should survive
	os.WriteFile(filepath.Join(dir, "keep.me"), []byte("keep"), 0644)

	// maxAge=0: all chunks just created are older than 0s, should be deleted
	if err := fileutil.CleanStaleChunks(dir, 0); err != nil {
		t.Fatalf("CleanStaleChunks: %v", err)
	}
	remaining, _ := os.ReadDir(dir)
	if len(remaining) != 1 || remaining[0].Name() != "keep.me" {
		t.Errorf("expected only keep.me after maxAge=0, got %v", names(remaining))
	}

	// recreate chunks, now test with large maxAge — all should survive
	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("abc-%d.chunk", i)), []byte{byte(i)}, 0644)
	}
	if err := fileutil.CleanStaleChunks(dir, time.Hour); err != nil {
		t.Fatalf("CleanStaleChunks: %v", err)
	}
	remaining, _ = os.ReadDir(dir)
	if len(remaining) != 4 {
		t.Errorf("expected 4 entries (3 chunks + keep.me) with maxAge=1h, got %d", len(remaining))
	}
}

func names(entries []os.DirEntry) []string {
	var ns []string
	for _, e := range entries {
		ns = append(ns, e.Name())
	}
	return ns
}
