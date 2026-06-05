package fileutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
)

type Node struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"` // "file" or "dir"
	Size     int64   `json:"size,omitempty"`
	Children []*Node `json:"children,omitempty"`
}

func ValidatePath(filename string) error {
	cleaned := filepath.Clean(filename)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "/") {
		return fmt.Errorf("unsafe path detected")
	}
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if part == ".." {
			return fmt.Errorf("unsafe path detected")
		}
	}
	return nil
}

func MoveFile(src, dst string) error {
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		if err := CopyFile(src, dst); err != nil {
			return err
		}
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("移动后删除源文件失败: %w", err)
		}
	}
	return nil
}

func CopyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("not a directory: %s", src)
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := CopyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := CopyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return dstFile.Sync()
}

func CalculateFilexxhash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hash := xxhash.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x", hash.Sum64()), nil
}

// maxScanDepth 限制目录递归深度，防止软链环导致无限递归爆栈。
const maxScanDepth = 64

// ScanDir 递归扫描整棵树（深度上限 maxScanDepth，防软链环）。供搜索用整树。
func ScanDir(path string) (*Node, error) {
	return scanDir(path, 0, maxScanDepth, make(map[string]bool))
}

// ScanDirDepth 浅扫描，最多展开 limit 层（limit=1 即仅当前层直接子项，
// 子目录只给名不展开；limit=2 再多预拉一层）。供浏览懒加载用。
// limit 仍受 maxScanDepth 兜底防环。
func ScanDirDepth(path string, limit int) (*Node, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > maxScanDepth {
		limit = maxScanDepth
	}
	return scanDir(path, 0, limit, make(map[string]bool))
}

// scanDir 递归扫描。depth 是当前深度，limit 是本次扫描允许展开的最大层数
// （与 maxScanDepth 取小者生效）；visited 记录已进入的目录真实路径，
// 防止软链环（A→B→A）造成无限递归。os.Stat 跟随软链，
// 所以软链目录会被正常当作目录遍历，但同一真实目录不会重复进入。
func scanDir(path string, depth, limit int, visited map[string]bool) (*Node, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	node := &Node{
		Name: filepath.Base(path),
		Type: "file",
		Size: info.Size(),
	}

	if info.IsDir() {
		node.Type = "dir"
		node.Size = 0

		if depth >= limit {
			return node, nil // 触底（达本次扫描层数上限），返回不展开的目录节点
		}
		// 用真实路径判环：软链指向已访问过的目录则跳过其子树
		real, rerr := filepath.EvalSymlinks(path)
		if rerr != nil {
			real = path
		}
		if visited[real] {
			return node, nil
		}
		visited[real] = true
		defer delete(visited, real) // 出栈后允许同级其它分支再访问

		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			child, err := scanDir(filepath.Join(path, entry.Name()), depth+1, limit, visited)
			if err != nil {
				continue // 跳过无权限的文件
			}
			node.Children = append(node.Children, child)
		}
	}

	return node, nil
}

// ── 分片上传工具函数 ─────────────────────────────────────────

// ChunkFileKey 根据文件路径生成唯一前缀，防止不同路径下同名文件的 chunk 冲突。
func ChunkFileKey(filename string) string {
	return fmt.Sprintf("%016x", xxhash.Sum64String(filename))
}

// ChunkTempPath 返回某一片的临时文件路径。
// 格式：{tempDir}/{fileKey}-{index}.chunk
func ChunkTempPath(tempDir, filename string, index int) string {
	return filepath.Join(tempDir, fmt.Sprintf("%s-%d.chunk", ChunkFileKey(filename), index))
}

// FindMissingChunks 检查 [0, total) 中哪些片不存在，返回缺失的索引列表。
func FindMissingChunks(tempDir, filename string, total int) []int {
	var missing []int
	for i := 0; i < total; i++ {
		if _, err := os.Stat(ChunkTempPath(tempDir, filename, i)); err != nil {
			missing = append(missing, i)
		}
	}
	return missing
}

// MergeChunks 将 total 片按序合并到 tempDir 下的临时文件，同时计算 xxhash。
// 返回 (合并临时文件路径, hash)，调用方校验 hash 后自行 MoveFile 或 Remove。
// 合并过程出错时临时文件已清理，调用方无需额外处理。
func MergeChunks(tempDir, filename string, total int) (mergePath, hash string, err error) {
	mergePath = filepath.Join(tempDir, fmt.Sprintf("%s-%d-merge.tmp", ChunkFileKey(filename), time.Now().UnixNano()))
	f, createErr := os.Create(mergePath)
	if createErr != nil {
		return "", "", fmt.Errorf("创建合并临时文件失败: %w", createErr)
	}

	h := xxhash.New()
	w := io.MultiWriter(f, h)

	for i := 0; i < total; i++ {
		chunkPath := ChunkTempPath(tempDir, filename, i)
		chunk, openErr := os.Open(chunkPath)
		if openErr != nil {
			f.Close()
			os.Remove(mergePath)
			return "", "", fmt.Errorf("打开 chunk %d 失败: %w", i, openErr)
		}
		_, copyErr := io.Copy(w, chunk)
		chunk.Close()
		if copyErr != nil {
			f.Close()
			os.Remove(mergePath)
			return "", "", fmt.Errorf("合并 chunk %d 失败: %w", i, copyErr)
		}
	}

	if syncErr := f.Sync(); syncErr != nil {
		f.Close()
		os.Remove(mergePath)
		return "", "", fmt.Errorf("sync 失败: %w", syncErr)
	}
	f.Close()

	return mergePath, fmt.Sprintf("%016x", h.Sum64()), nil
}

// DeleteChunks 删除某次上传的所有 chunk 文件，忽略不存在的片。
func DeleteChunks(tempDir, filename string, total int) {
	for i := 0; i < total; i++ {
		os.Remove(ChunkTempPath(tempDir, filename, i))
	}
}

// CleanStaleChunks 删除 tempDir 下超过 maxAge 未修改的 .chunk 文件。
// 在后台 goroutine 定期调用即可。
func CleanStaleChunks(tempDir string, maxAge time.Duration) error {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".chunk") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			os.Remove(filepath.Join(tempDir, entry.Name()))
		}
	}
	return nil
}
