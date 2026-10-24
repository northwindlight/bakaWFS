package cloudfs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintQiniuBlockBoundaries(t *testing.T) {
	// Fixed vectors cross-checked with a separate implementation and real Kodo.
	for _, vector := range []struct {
		size int
		etag string
	}{{0, "Fto5o-5ea0sNMlW_75VgGJCv2AcJ"}, {1, "Ft9YJIxBTzQsgeBWtAvuEtF6CL9h"}, {4194304, "Fo_ztJ9AU5Xwk-23Cy8ocI1thd0j"}, {4194305, "llDSCrTe3oitPQMq_Q2z8ea28otS"}, {8388608, "lpHFuw-xe68H0wAM27mRJymsOcgq"}} {
		t.Run(fmt.Sprint(vector.size), func(t *testing.T) {
			data := bytes.Repeat([]byte{42}, vector.size)
			filename := filepath.Join(t.TempDir(), "fixture")
			if e := os.WriteFile(filename, data, 0600); e != nil {
				t.Fatal(e)
			}
			fp, e := FingerprintFile(filename)
			if e != nil {
				t.Fatal(e)
			}
			if fp.Size != int64(vector.size) || fp.ETag != vector.etag || bytesETag(data) != vector.etag {
				t.Fatalf("wrong ETag at %d", vector.size)
			}
		})
	}
}
func TestInventoryPreservesOriginalBytes(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	data := []byte("unaltered source")
	for _, p := range []string{"普通文件.txt", "config.yaml", ".hidden", "crawler.log"} {
		if e := os.WriteFile(filepath.Join(root, p), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Mkdir(filepath.Join(root, "空目录"), 0700); e != nil {
		t.Fatal(e)
	}
	m, e := Inventory(root, state, 2)
	if e != nil {
		t.Fatal(e)
	}
	if len(m.Files) != 1 || len(m.Excluded) != 3 || len(m.Directories) != 2 {
		t.Fatalf("unexpected inventory: %+v", m)
	}
	got, e := os.ReadFile(filepath.Join(root, "普通文件.txt"))
	if e != nil || !bytes.Equal(data, got) {
		t.Fatal("source changed")
	}
	if len(m.Nodes()) != 3 {
		t.Fatal("incomplete seed")
	}
}

func TestSourceAuditRejectsSameLengthChangedFile(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	filename := filepath.Join(root, "original.txt")
	if e := os.WriteFile(filename, []byte("original"), 0600); e != nil {
		t.Fatal(e)
	}
	m, e := Inventory(root, state, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifySource(state, 1); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filename, []byte("modified"), 0600); e != nil {
		t.Fatal(e)
	}
	changed, e := Inventory(root, t.TempDir(), 1)
	if e != nil {
		t.Fatal(e)
	}
	if changed.Bytes != m.Bytes || changed.Digest() == m.Digest() {
		t.Fatal("manifest did not bind actual bytes")
	}
	if e = VerifySource(state, 1); e == nil {
		t.Fatal("accepted changed source with the same total size")
	}
}
