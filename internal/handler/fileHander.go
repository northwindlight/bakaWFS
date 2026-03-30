package handler

import (
	"bakaWFS/internal/config"
	"bakaWFS/internal/fileutil"
	"bakaWFS/internal/task"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type FileHandler struct {
	cfg        config.Config
	logger     *slog.Logger
	tasks      *task.TaskManager
	downloader *task.Downloader
}

func NewFileHandler(cfg config.Config, logger *slog.Logger, tasks *task.TaskManager, downloader *task.Downloader) *FileHandler {
	return &FileHandler{
		cfg:        cfg,
		logger:     logger,
		tasks:      tasks,
		downloader: downloader,
	}
}

func (h *FileHandler) HandleNode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	rootNode, err := fileutil.ScanDir(h.cfg.DirPath)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("扫描目录失败", "error", err, "path", h.cfg.DirPath)
		return
	}
	if err := json.NewEncoder(w).Encode(rootNode); err != nil {
		h.logger.Error("JSON编码失败", "error", err)
	}
}

func (h *FileHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	username := r.Header.Get("X-Username")
	urlFilename := r.Header.Get("X-Upload-Filename")
	sizeStr := r.Header.Get("Content-Length")

	if urlFilename == "" || sizeStr == "" {
		http.Error(w, "Bad Request: Missing upload headers", http.StatusBadRequest)
		h.logger.Warn("上传拦截: 缺少必要请求头", "user", username)
		return
	}

	expectedSize, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || expectedSize <= 0 {
		http.Error(w, "Bad Request: Invalid file size", http.StatusBadRequest)
		h.logger.Warn("上传拦截: 尺寸解析错误", "size_str", sizeStr)
		return
	}
	if expectedSize > 10*1024*1024*1024 {
		http.Error(w, "Bad Request: File size exceeds limit", http.StatusBadRequest)
		h.logger.Warn("上传拦截: 文件过大", "size", expectedSize)
		return
	}

	filename, err := url.QueryUnescape(urlFilename)
	if err != nil {
		filename = urlFilename
	}
	if err := fileutil.ValidatePath(filename); err != nil {
		http.Error(w, "Bad Request: Forbidden path", http.StatusBadRequest)
		h.logger.Warn("上传拦截: 非法路径尝试", "path", filename, "user", username)
		return
	}

	targetPath := filepath.Join(h.cfg.DirPath, filename)
	if _, err := os.Stat(targetPath); err == nil {
		http.Error(w, "Conflict: File already exists", http.StatusConflict)
		h.logger.Warn("上传拦截: 文件已存在", "path", filename)
		return
	}

	if !h.tasks.TryAdd(filename, username, task.TaskUpload) {
		http.Error(w, "Conflict: Another upload in progress", http.StatusConflict)
		h.logger.Warn("上传拦截: 并发上传冲突", "path", filename, "user", username)
		return
	}
	defer h.tasks.Remove(filename)

	h.tasks.UpdateProgress(filename, 0, expectedSize)

	tempPath := filepath.Join(h.cfg.TempDir, fmt.Sprintf("%d-%s.tmp", time.Now().UnixNano(), username))
	tempFile, err := os.Create(tempPath)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("上传失败: 无法创建临时文件", "error", err)
		return
	}
	defer os.Remove(tempPath)

	h.logger.Info("接收上传数据...", "file", filename, "user", username, "size", expectedSize)

	written, err := io.Copy(tempFile, io.LimitReader(r.Body, expectedSize+1))
	tempFile.Close()

	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("上传失败: 读取数据错误", "error", err)
		return
	}
	if written != expectedSize {
		errMsg := "Size mismatch"
		if written > expectedSize {
			errMsg = "Data exceeds expected size"
		}
		http.Error(w, "Bad Request: "+errMsg, http.StatusBadRequest)
		h.logger.Warn("上传失败: 长度不匹配", "expected", expectedSize, "actual", written)
		return
	}

	if err := fileutil.MoveFile(tempPath, targetPath); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("上传失败: 持久化移动失败", "error", err)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success", "file": filename})
	h.logger.Info("文件上传成功", "file", filename, "user", username)
}

func (h *FileHandler) HandleRemoteUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024)
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	username := r.Header.Get("X-Username")

	var req struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		h.logger.Warn("远程下载请求解析失败", "error", err)
		return
	}

	if err := fileutil.ValidatePath(req.Filename); err != nil {
		http.Error(w, "Bad Request: Forbidden path", http.StatusBadRequest)
		h.logger.Warn("远程下载: 非法路径", "path", req.Filename, "user", username)
		return
	}

	targetPath := filepath.Join(h.cfg.DirPath, req.Filename)
	if _, err := os.Stat(targetPath); err == nil {
		http.Error(w, "Conflict: File already exists", http.StatusConflict)
		h.logger.Warn("远程下载: 文件已存在", "path", req.Filename)
		return
	}

	if !h.tasks.TryAdd(req.Filename, username, task.TaskDownload) {
		http.Error(w, "Conflict: Another download in progress", http.StatusConflict)
		h.logger.Warn("远程下载: 并发冲突", "path", req.Filename)
		return
	}

	dt := task.DownloadTask{
		URL:        req.URL,
		TargetPath: targetPath,
		TempDir:    h.cfg.TempDir,
		Filename:   req.Filename,
		Username:   username,
	}
	if err := h.downloader.Enqueue(dt); err != nil {
		h.tasks.Remove(req.Filename)
		http.Error(w, "Service Unavailable: Download queue is full", http.StatusServiceUnavailable)
		h.logger.Warn("远程下载: 队列已满", "file", req.Filename)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "accepted",
		"message": "Download started",
		"file":    req.Filename,
	})
	h.logger.Info("远程下载任务已接受", "file", req.Filename, "user", username, "url", req.URL)
}

func (h *FileHandler) HandleProgress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshots := h.tasks.ListDownloads()
	response := make(map[string]any, len(snapshots))
	for _, s := range snapshots {
		response[s.Filename] = map[string]any{
			"username":     s.Username,
			"downloadSize": s.Downloaded,
			"expectedSize": s.TotalSize,
		}
	}
	json.NewEncoder(w).Encode(response)
}

func (h *FileHandler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	filename, err := url.QueryUnescape(r.URL.Query().Get("filename"))
	if err != nil || filename == "" {
		http.Error(w, "Bad Request: Invalid filename parameter", http.StatusBadRequest)
		return
	}
	if !h.tasks.Cancel(filename) {
		h.logger.Warn("取消失败: 任务不存在", "file", filename)
		http.Error(w, "Not Found: No such task in progress", http.StatusNotFound)
		return
	}
	h.logger.Info("任务取消成功", "file", filename)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelled", "file": filename})
}
