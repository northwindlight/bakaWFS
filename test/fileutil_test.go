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
