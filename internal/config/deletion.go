package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeletionDir 返回文件区外的删除暂存目录，解析已有软链以免暂存数据仍能被公开访问。
func (c Config) DeletionDir() (string, error) {
	root, err := resolveExisting(c.DirPath)
	if err != nil {
		return "", err
	}
	trash := c.TrashDir
	if trash == "" {
		trash = root + ".bakawfs-trash"
	}
	trash, err = resolveExisting(trash)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, trash)
	if err != nil {
		return "", err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("trash_dir 必须位于 file_dir 之外")
	}
	// 暂存目录也不能是文件区的祖先，避免清理误触源目录。
	rel, err = filepath.Rel(trash, root)
	if err != nil {
		return "", err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("trash_dir 不能包含 file_dir")
	}
	return trash, nil
}

func resolveExisting(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	resolved, err = resolveExisting(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(absolute)), nil
}
