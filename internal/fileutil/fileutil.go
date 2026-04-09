package fileutil

import (
	"bakaWFS/internal/dto"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
)

func ValidatePath(filename string) error {
	cleaned := filepath.Clean(filename)
	if filepath.IsAbs(cleaned) || strings.Contains(cleaned, "..") || strings.HasPrefix(cleaned, "/") {
		return fmt.Errorf("unsafe path detected")
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
		os.Remove(src)
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

func ScanDir(path string) (*dto.Node, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	node := &dto.Node{
		Name: filepath.Base(path),
		Type: "file",
		Size: info.Size(),
	}

	if info.IsDir() {
		node.Type = "dir"
		node.Size = 0

		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			child, err := ScanDir(filepath.Join(path, entry.Name()))
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
