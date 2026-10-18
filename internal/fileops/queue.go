package fileops

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bakaWFS/internal/fileutil"
)

type OpType int

const (
	OpRename OpType = iota
	OpDelete
	OpMkdir
)

type Op struct {
	Type     OpType
	Src      string
	Dst      string
	Username string
	ResultCh chan Result
}

type Result struct {
	Path string
	Err  error
}

type Queue struct {
	ops         chan Op
	logger      *slog.Logger
	logf        *os.File
	trashDir    string
	wake        chan struct{}
	done        chan struct{}
	stopCleanup chan struct{}
	cleanupDone chan struct{}
}

func New(logger *slog.Logger, logPath, trashDir string) (*Queue, error) {
	if trashDir == "" {
		return nil, fmt.Errorf("trash directory is required")
	}
	var logf *os.File
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("open audit log: %w", err)
		}
		logf = f
	}
	q := &Queue{
		ops:      make(chan Op, 64),
		logger:   logger,
		logf:     logf,
		trashDir: trashDir, wake: make(chan struct{}, 1), done: make(chan struct{}),
		stopCleanup: make(chan struct{}), cleanupDone: make(chan struct{}),
	}
	go q.loop()
	go q.cleanupLoop()
	logger.Info("文件操作队列已启动")
	return q, nil
}

func (q *Queue) Close() {
	close(q.ops)
	<-q.done
	close(q.stopCleanup)
	<-q.cleanupDone
	if q.logf != nil {
		q.logf.Close()
	}
}

func (q *Queue) Enqueue(op Op) Result {
	if op.ResultCh == nil {
		op.ResultCh = make(chan Result, 1)
	}
	q.ops <- op
	return <-op.ResultCh
}

func (q *Queue) loop() {
	defer close(q.done)
	for op := range q.ops {
		r := q.exec(op)
		op.ResultCh <- r
	}
}

func (q *Queue) exec(op Op) Result {
	switch op.Type {
	case OpRename:
		dst := resolveConflict(op.Dst)
		if err := fileutil.MoveFile(op.Src, dst); err != nil {
			return Result{Err: fmt.Errorf("move: %w", err)}
		}
		if dst != op.Dst {
			q.logger.Info("路径冲突已自动重命名", "original", op.Dst, "resolved", dst)
		}
		q.audit(op.Username, "rename", op.Dst, dst)
		return Result{Path: dst}

	case OpDelete:
		if err := q.detach(op.Dst); err != nil {
			return Result{Err: fmt.Errorf("delete: %w", err)}
		}
		q.audit(op.Username, "delete", op.Dst, "")
		return Result{}

	case OpMkdir:
		if err := os.MkdirAll(op.Dst, 0755); err != nil {
			return Result{Err: fmt.Errorf("mkdir: %w", err)}
		}
		q.audit(op.Username, "mkdir", op.Dst, "")
		return Result{Path: op.Dst}
	}
	return Result{}
}

func resolveConflict(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s(%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func (q *Queue) audit(username, action, path, finalPath string) {
	if q.logf == nil {
		return
	}
	// 用 json.Marshal 而非手工拼接：username/path 来自用户输入（文件名可含引号、
	// 反斜杠、换行），手拼会导致审计日志 JSON 损坏甚至被注入伪造条目。转义交给标准库。
	entry := map[string]string{
		"time":   time.Now().Format(time.RFC3339),
		"user":   username,
		"action": action,
		"path":   path,
	}
	if finalPath != "" {
		entry["final"] = finalPath
	}
	b, err := json.Marshal(entry)
	if err != nil {
		q.logger.Warn("审计日志序列化失败", "error", err)
		return
	}
	q.logf.Write(append(b, '\n'))
}

// detach 的提交点只有一次 rename；不允许跨盘 copy/remove 回退。
// 成功后原路径整体消失，清理失败只留下文件区外的垃圾，不会部分删除原目录。
func (q *Queue) detach(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(q.trashDir, 0700); err != nil {
		return err
	}
	dst := filepath.Join(q.trashDir, "deleted-"+rand.Text())
	if err := os.Rename(path, dst); err != nil {
		return err
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return nil
}

// 磁盘目录就是待清理队列；启动即重扫，失败每分钟重试，无索引和额外状态文件。
func (q *Queue) cleanupLoop() {
	defer close(q.cleanupDone)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	q.cleanTrash()
	for {
		select {
		case <-q.stopCleanup:
			q.cleanTrash()
			return
		case <-q.wake:
			q.cleanTrash()
		case <-ticker.C:
			q.cleanTrash()
		}
	}
}

func (q *Queue) cleanTrash() {
	entries, err := os.ReadDir(q.trashDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		q.logger.Warn("读取删除暂存目录失败", "error", err)
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "deleted-") {
			continue
		}
		path := filepath.Join(q.trashDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			q.logger.Warn("删除已提交，后台清理稍后重试", "path", path, "error", err)
		}
	}
}
