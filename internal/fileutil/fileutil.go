package fileutil

import (
	"bakaWFS/internal/dto"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
