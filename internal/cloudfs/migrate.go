package cloudfs

import (
	"bakaWFS/internal/catalog"
	"bakaWFS/internal/fileutil"
	"bakaWFS/internal/thumb"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ImportFile struct {
	Path     string `json:"path"`
	Modified int64  `json:"modified_ns"`
	Fingerprint
}
type ImportManifest struct {
	Format      int          `json:"format"`
	Root        string       `json:"root"`
	Files       []ImportFile `json:"files"`
	Directories []string     `json:"directories"`
	Excluded    []string     `json:"excluded"`
	Bytes       int64        `json:"bytes"`
}

func (m *ImportManifest) Digest() string {
	data, _ := json.Marshal(m)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func VerifySource(state string, workers int) error {
	expected, e := LoadManifest(state)
	if e != nil {
		return e
	}
	actual, e := Inventory(expected.Root, filepath.Join(state, "source-audit"), workers)
	if e != nil {
		return e
	}
	if actual.Digest() != expected.Digest() {
		return fmt.Errorf("source inventory changed; refusing to publish a stale import")
	}
	return saveJSON(filepath.Join(state, "source-verification.json"), map[string]any{"passed": true, "manifest_sha256": expected.Digest(), "checked_at": time.Now().UTC()})
}

type ImportReceipt struct {
	Path    string                `json:"path"`
	SHA256  string                `json:"sha256"`
	Objects map[string]ObjectInfo `json:"objects"`
}

func saveJSON(filename string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filename+".tmp", append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(filename+".tmp", filename)
}

func Inventory(root, state string, workers int) (*ImportManifest, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	tree, err := fileutil.ScanDir(root)
	if err != nil {
		return nil, err
	}
	m := &ImportManifest{Format: 1, Root: root, Directories: []string{""}, Files: []ImportFile{}, Excluded: []string{}}
	var visit func(*fileutil.Node, string) error
	visit = func(node *fileutil.Node, rel string) error {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if len(entries) != len(node.Children) {
			return fmt.Errorf("directory scan skipped entries at %s", rel)
		}
		for _, child := range node.Children {
			p := path.Join(rel, child.Name)
			name := strings.ToLower(child.Name)
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".log") || name == "users.yaml" || name == "config.yaml" || name == "keys.env" || name == "private.key" {
				m.Excluded = append(m.Excluded, p)
				continue
			}
			if child.Type == "dir" {
				m.Directories = append(m.Directories, p)
				if err := visit(child, p); err != nil {
					return err
				}
			} else {
				m.Files = append(m.Files, ImportFile{Path: p})
			}
		}
		return nil
	}
	if err := visit(tree, ""); err != nil {
		return nil, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	sort.Strings(m.Directories)
	sort.Strings(m.Excluded)
	indices := make(chan int)
	failures := make(chan error, len(m.Files))
	var wg sync.WaitGroup
	var done atomic.Int64
	for range workers {
		wg.Go(func() {
			for i := range indices {
				f := &m.Files[i]
				filename := filepath.Join(root, filepath.FromSlash(f.Path))
				stat, err := os.Stat(filename)
				if err != nil {
					failures <- err
					continue
				}
				fp, err := FingerprintFile(filename)
				if err != nil {
					failures <- fmt.Errorf("hash %s: %w", f.Path, err)
					continue
				}
				f.Fingerprint = fp
				f.Modified = stat.ModTime().UnixNano()
				n := done.Add(1)
				if n%500 == 0 {
					fmt.Printf("hashed %d/%d files\n", n, len(m.Files))
				}
			}
		})
	}
	for i := range m.Files {
		indices <- i
	}
	close(indices)
	wg.Wait()
	close(failures)
	for err := range failures {
		return nil, err
	}
	for _, f := range m.Files {
		m.Bytes += f.Size
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return nil, err
	}
	if err := saveJSON(filepath.Join(state, "manifest.json"), m); err != nil {
		return nil, err
	}
	fmt.Printf("inventory complete: %d files, %d directories, %d bytes, %d excluded entries\n", len(m.Files), len(m.Directories), m.Bytes, len(m.Excluded))
	return m, nil
}

func LoadManifest(state string) (*ImportManifest, error) {
	data, err := os.ReadFile(filepath.Join(state, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m ImportManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Format != 1 || m.Root == "" || len(m.Directories) == 0 {
		return nil, fmt.Errorf("invalid import manifest")
	}
	return &m, nil
}
func ImportNodeID(rel string) string {
	if rel == "" {
		return "root"
	}
	digest := sha256.Sum256([]byte("bakawfs-import-v1\x00" + rel))
	return hex.EncodeToString(digest[:16])
}
func (m *ImportManifest) Nodes() map[string]catalog.Node {
	nodes := map[string]catalog.Node{"root": {ID: "root", Kind: "dir"}}
	for _, rel := range m.Directories {
		if rel == "" {
			continue
		}
		id := ImportNodeID(rel)
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		nodes[id] = catalog.Node{ID: id, Parent: ImportNodeID(parent), Name: path.Base(rel), Kind: "dir"}
	}
	for _, file := range m.Files {
		id := ImportNodeID(file.Path)
		parent := path.Dir(file.Path)
		if parent == "." {
			parent = ""
		}
		nodes[id] = catalog.Node{ID: id, Parent: ImportNodeID(parent), Name: path.Base(file.Path), Kind: "file", ObjectKey: "objects/" + file.SHA256, Size: file.Size, Hash: file.ETag}
	}
	return nodes
}

func (k *Kodo) ImportFiles(ctx context.Context, m *ImportManifest, state, cache string, workers int) error {
	if err := k.EnsurePrivateBucket(ctx, false); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(state, "receipts"), 0700); err != nil {
		return err
	}
	jobs := make(chan ImportFile)
	failures := make(chan error, len(m.Files))
	var wg sync.WaitGroup
	var done atomic.Int64
	for range workers {
		wg.Go(func() {
			for file := range jobs {
				filename := filepath.Join(m.Root, filepath.FromSlash(file.Path))
				receiptName := filepath.Join(state, "receipts", ImportNodeID(file.Path)+".json")
				stat, err := os.Stat(filename)
				if err != nil || stat.Size() != file.Size || stat.ModTime().UnixNano() != file.Modified {
					failures <- fmt.Errorf("source changed: %s", file.Path)
					continue
				}
				var previous ImportReceipt
				if data, err := os.ReadFile(receiptName); err == nil && json.Unmarshal(data, &previous) == nil && previous.SHA256 == file.SHA256 {
					n := done.Add(1)
					if n%200 == 0 {
						fmt.Printf("verified/resumed %d/%d files\n", n, len(m.Files))
					}
					continue
				}
				receipt := ImportReceipt{Path: file.Path, SHA256: file.SHA256, Objects: map[string]ObjectInfo{}}
				key := "objects/" + file.SHA256
				for attempt := 0; attempt < 4; attempt++ {
					err = k.PutFile(ctx, key, filename, file.Fingerprint)
					if err == nil || ctx.Err() != nil {
						break
					}
					time.Sleep(time.Duration(attempt+1) * time.Second)
				}
				if err != nil {
					failures <- fmt.Errorf("import %s: %w", file.Path, err)
					continue
				}
				receipt.Objects[key] = ObjectInfo{Size: file.Size, Hash: file.ETag}
				if thumb.Supported(file.Path) {
					objects, err := k.ImportThumbnails(ctx, filename, file.SHA256, cache)
					if err != nil {
						failures <- fmt.Errorf("thumbnails %s: %w", file.Path, err)
						continue
					}
					for key, info := range objects {
						receipt.Objects[key] = info
					}
				}
				if err := saveJSON(receiptName, receipt); err != nil {
					failures <- err
					continue
				}
				n := done.Add(1)
				if n%100 == 0 {
					fmt.Printf("uploaded and checked %d/%d files\n", n, len(m.Files))
				}
			}
		})
	}
	for _, file := range m.Files {
		if ctx.Err() != nil {
			break
		}
		jobs <- file
	}
	close(jobs)
	wg.Wait()
	close(failures)
	var count int
	var first error
	for err := range failures {
		count++
		if first == nil {
			first = err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if count > 0 {
		return fmt.Errorf("%d files need retry; first error: %w", count, first)
	}
	return nil
}

func (k *Kodo) ImportThumbnails(ctx context.Context, filename, digest, cache string) (map[string]ObjectInfo, error) {
	result := map[string]ObjectInfo{}
	hash, err := thumb.ReadEmbeddedHash(filename)
	if err != nil {
		return nil, err
	}
	if hash == "" {
		hash, err = thumb.ComputeHash(filename)
		if err != nil {
			return nil, err
		}
	}
	var temp string
	defer func() {
		if temp != "" {
			os.RemoveAll(temp)
		}
	}()
	for _, spec := range []struct {
		name string
		size thumb.Size
	}{{"list", thumb.SizeList}, {"mid", thumb.SizeMid}} {
		key := "thumbs/" + digest + "/" + spec.name + ".jpg"
		if info, err := k.Stat(ctx, key); err == nil && info.Size > 0 {
			result[key] = info
			continue
		}
		data, err := os.ReadFile(filepath.Join(cache, hash+"-"+spec.name+".jpg"))
		if err != nil {
			if temp == "" {
				temp, err = os.MkdirTemp("", "bakawfs-thumbs-")
				if err != nil {
					return nil, err
				}
				input, err := os.Open(filename)
				if err != nil {
					return nil, err
				}
				output, err := os.Create(filepath.Join(temp, "source"+filepath.Ext(filename)))
				if err != nil {
					input.Close()
					return nil, err
				}
				_, err = io.Copy(output, input)
				input.Close()
				output.Close()
				if err != nil {
					return nil, err
				}
			}
			gen, err := thumb.New(filepath.Join(temp, "cache"))
			if err != nil {
				return nil, err
			}
			data, err = gen.Get(filepath.Join(temp, "source"+filepath.Ext(filename)), spec.size)
			if err != nil {
				return nil, err
			}
		}
		info, err := k.PutBytes(ctx, key, data, "image/jpeg")
		if err != nil {
			return nil, err
		}
		result[key] = info
	}
	return result, nil
}

func (k *Kodo) VerifyImport(ctx context.Context, m *ImportManifest, state string, workers int) error {
	expected := map[string]ObjectInfo{}
	for _, file := range m.Files {
		data, err := os.ReadFile(filepath.Join(state, "receipts", ImportNodeID(file.Path)+".json"))
		if err != nil {
			return fmt.Errorf("missing import receipt")
		}
		var r ImportReceipt
		if json.Unmarshal(data, &r) != nil || r.SHA256 != file.SHA256 || r.Path != file.Path {
			return fmt.Errorf("invalid import receipt")
		}
		original, ok := r.Objects["objects/"+file.SHA256]
		if !ok || original.Size != file.Size || original.Hash != file.ETag {
			return fmt.Errorf("missing or incorrect original receipt")
		}
		if thumb.Supported(file.Path) {
			for _, size := range []string{"list", "mid"} {
				info, ok := r.Objects["thumbs/"+file.SHA256+"/"+size+".jpg"]
				if !ok || info.Size <= 0 || info.Hash == "" {
					return fmt.Errorf("missing thumbnail receipt")
				}
			}
		}
		for key, info := range r.Objects {
			if prior, ok := expected[key]; ok && (prior.Hash != info.Hash || prior.Size != info.Size) {
				return fmt.Errorf("conflicting import expectations")
			}
			expected[key] = info
		}
	}
	jobs := make(chan string)
	failures := make(chan error, len(expected))
	var wg sync.WaitGroup
	var done atomic.Int64
	for range workers {
		wg.Go(func() {
			for key := range jobs {
				actual, err := k.Stat(ctx, key)
				want := expected[key]
				if err != nil || actual.Size != want.Size || actual.Hash != want.Hash {
					failures <- fmt.Errorf("remote verification mismatch for %s", key)
					continue
				}
				n := done.Add(1)
				if n%1000 == 0 {
					fmt.Printf("remote audit %d/%d objects\n", n, len(expected))
				}
			}
		})
	}
	for key := range expected {
		jobs <- key
	}
	close(jobs)
	wg.Wait()
	close(failures)
	for err := range failures {
		return err
	}
	return saveJSON(filepath.Join(state, "verification.json"), map[string]any{"verified_at": time.Now().UTC(), "manifest_sha256": m.Digest(), "source_files": len(m.Files), "source_directories": len(m.Directories), "source_bytes": m.Bytes, "unique_objects": len(expected), "objects_verified": done.Load(), "passed": true})
}
