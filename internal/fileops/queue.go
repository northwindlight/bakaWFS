package fileops

import (
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
	ops    chan Op
	logger *slog.Logger
	logf   *os.File
}

func New(logger *slog.Logger, logPath string) (*Queue, error) {
	var logf *os.File
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("open audit log: %w", err)
		}
		logf = f
	}
	q := &Queue{
		ops:    make(chan Op, 64),
		logger: logger,
		logf:   logf,
	}
	go q.loop()
	logger.Info("文件操作队列已启动")
	return q, nil
}

func (q *Queue) Close() {
	close(q.ops)
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
		if err := os.RemoveAll(op.Dst); err != nil {
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
	entry := fmt.Sprintf(`{"time":"%s","user":"%s","action":"%s","path":"%s"`,
		time.Now().Format(time.RFC3339), username, action, path)
	if finalPath != "" {
		entry += fmt.Sprintf(`,"final":"%s"`, finalPath)
	}
	entry += "}\n"
	q.logf.WriteString(entry)
}
