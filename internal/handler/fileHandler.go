package handler

import (
	"bakaWFS/internal/config"
	"bakaWFS/internal/fileops"
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
	downloader *task.Downloader
	queue      *fileops.Queue
}

func NewFileHandler(cfg config.Config, logger *slog.Logger, downloader *task.Downloader, queue *fileops.Queue) *FileHandler {
	return &FileHandler{
		cfg:        cfg,
		logger:     logger,
		downloader: downloader,
		queue:      queue,
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

	username, _ := r.Context().Value(ContextKeyUsername).(string)
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

	result := h.queue.Enqueue(fileops.Op{
		Type:     fileops.OpRename,
		Src:      tempPath,
		Dst:      targetPath,
		Username: username,
	})
	if result.Err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("上传失败: 持久化移动失败", "error", result.Err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	h.logger.Info("文件上传成功", "file", result.Path, "user", username)
}

func (h *FileHandler) HandleRemoteUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024)
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	username, _ := r.Context().Value(ContextKeyUsername).(string)

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

	dt := task.DownloadTask{
		URL:        req.URL,
		TargetPath: targetPath,
		TempDir:    h.cfg.TempDir,
		Filename:   req.Filename,
		Username:   username,
	}
	if err := h.downloader.Enqueue(dt); err != nil {
		http.Error(w, "Service Unavailable: "+err.Error(), http.StatusServiceUnavailable)
		h.logger.Warn("远程下载: 入队失败", "file", req.Filename, "error", err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	h.logger.Info("远程下载任务已接受", "file", req.Filename, "user", username, "url", req.URL)
}

func (h *FileHandler) HandleProgress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshots := h.downloader.ListProgress()
	response := make(map[string]any, len(snapshots))
	for filename, s := range snapshots {
		response[filename] = map[string]any{
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
	var req struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Filename == "" {
		http.Error(w, "Bad Request: Invalid filename", http.StatusBadRequest)
		return
	}

	if !h.downloader.Cancel(req.Filename) {
		h.logger.Warn("取消失败: 任务不存在", "file", req.Filename)
		http.Error(w, "Not Found: No such task in progress", http.StatusNotFound)
		return
	}

	h.logger.Info("任务取消成功", "file", req.Filename)
	w.WriteHeader(http.StatusNoContent)
}

// HandleChunkUpload 接收单个分片，写入临时目录，幂等（重传直接覆盖）。
//
// Headers:
//
//	X-Upload-Filename  url_encoded 相对路径
//	X-Chunk-Index      分片序号，从 0 开始
//	Content-Length     本片字节数
func (h *FileHandler) HandleChunkUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	username, _ := r.Context().Value(ContextKeyUsername).(string)

	urlFilename := r.Header.Get("X-Upload-Filename")
	indexStr := r.Header.Get("X-Chunk-Index")
	sizeStr := r.Header.Get("Content-Length")

	if urlFilename == "" || indexStr == "" || sizeStr == "" {
		http.Error(w, "Bad Request: Missing headers (X-Upload-Filename, X-Chunk-Index, Content-Length)", http.StatusBadRequest)
		return
	}

	filename, err := url.QueryUnescape(urlFilename)
	if err != nil {
		filename = urlFilename
	}
	if err := fileutil.ValidatePath(filename); err != nil {
		http.Error(w, "Bad Request: Forbidden path", http.StatusBadRequest)
		h.logger.Warn("分片上传拦截: 非法路径", "path", filename, "user", username)
		return
	}

	index, err := strconv.Atoi(indexStr)
	if err != nil || index < 0 {
		http.Error(w, "Bad Request: Invalid X-Chunk-Index", http.StatusBadRequest)
		return
	}

	chunkSize, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || chunkSize <= 0 {
		http.Error(w, "Bad Request: Invalid Content-Length", http.StatusBadRequest)
		return
	}
	// 单片限制 500MB，防止客户端乱传
	if chunkSize > 500*1024*1024 {
		http.Error(w, "Bad Request: Chunk too large", http.StatusBadRequest)
		return
	}

	// 目标文件已存在则整个分片上传流程已无意义
	targetPath := filepath.Join(h.cfg.DirPath, filename)
	if _, err := os.Stat(targetPath); err == nil {
		http.Error(w, "Conflict: File already exists", http.StatusConflict)
		return
	}

	chunkPath := fileutil.ChunkTempPath(h.cfg.TempDir, filename, index)

	// 先写到同目录下的 .tmp 文件，写完再 rename，保证 chunk 文件原子落地
	writeTmp := chunkPath + ".tmp"
	f, err := os.Create(writeTmp)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("分片上传: 创建临时文件失败", "error", err)
		return
	}
	defer os.Remove(writeTmp)

	written, err := io.Copy(f, io.LimitReader(r.Body, chunkSize+1))
	f.Close()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("分片上传: 写入失败", "error", err)
		return
	}
	if written != chunkSize {
		http.Error(w, "Bad Request: Size mismatch", http.StatusBadRequest)
		h.logger.Warn("分片上传: 长度不匹配", "expected", chunkSize, "actual", written)
		return
	}

	if err := os.Rename(writeTmp, chunkPath); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("分片上传: rename 失败", "error", err)
		return
	}

	h.logger.Info("分片接收完成", "file", filename, "index", index, "user", username)
	w.WriteHeader(http.StatusNoContent)
}

// HandleChunkMerge 合并所有分片，校验整体 hash。
//
// 缺片  → 202 {"missing":[...]}
// hash 不符 → 422，删除全部 chunk
// 成功  → 204，删除全部 chunk
func (h *FileHandler) HandleChunkMerge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	username, _ := r.Context().Value(ContextKeyUsername).(string)

	var req struct {
		Filename string `json:"filename"`
		Hash     string `json:"hash"`
		Total    int    `json:"total"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if req.Filename == "" || req.Hash == "" || req.Total <= 0 {
		http.Error(w, "Bad Request: filename, hash, total required", http.StatusBadRequest)
		return
	}
	if err := fileutil.ValidatePath(req.Filename); err != nil {
		http.Error(w, "Bad Request: Forbidden path", http.StatusBadRequest)
		h.logger.Warn("分片合并拦截: 非法路径", "path", req.Filename, "user", username)
		return
	}

	targetPath := filepath.Join(h.cfg.DirPath, req.Filename)
	if _, err := os.Stat(targetPath); err == nil {
		http.Error(w, "Conflict: File already exists", http.StatusConflict)
		return
	}

	// 检查缺片
	missing := fileutil.FindMissingChunks(h.cfg.TempDir, req.Filename, req.Total)
	if len(missing) > 0 {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"missing": missing})
		h.logger.Info("分片合并: 缺少分片", "file", req.Filename, "missing", missing)
		return
	}

	// 合并到临时文件，同时得到 hash
	mergePath, gotHash, err := fileutil.MergeChunks(h.cfg.TempDir, req.Filename, req.Total)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("分片合并失败", "file", req.Filename, "error", err)
		return
	}

	// hash 不符：删合并临时文件 + 所有 chunk，客户端整体重传
	if gotHash != req.Hash {
		os.Remove(mergePath)
		fileutil.DeleteChunks(h.cfg.TempDir, req.Filename, req.Total)
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": "hash mismatch"})
		h.logger.Warn("分片合并: hash 不符，已清理", "file", req.Filename, "expected", req.Hash, "got", gotHash)
		return
	}

	// hash 一致，入队原子落盘
	result := h.queue.Enqueue(fileops.Op{
		Type:     fileops.OpRename,
		Src:      mergePath,
		Dst:      targetPath,
		Username: username,
	})
	if result.Err != nil {
		os.Remove(mergePath)
		fileutil.DeleteChunks(h.cfg.TempDir, req.Filename, req.Total)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("分片合并: 移动文件失败", "file", req.Filename, "error", result.Err)
		return
	}

	// 清理 chunk
	fileutil.DeleteChunks(h.cfg.TempDir, req.Filename, req.Total)
	w.WriteHeader(http.StatusNoContent)
	h.logger.Info("分片合并成功", "file", result.Path, "user", username)
}

func (h *FileHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	username, _ := r.Context().Value(ContextKeyUsername).(string)
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := fileutil.ValidatePath(req.Path); err != nil {
		http.Error(w, "Bad Request: Forbidden path", http.StatusBadRequest)
		return
	}
	targetPath := filepath.Join(h.cfg.DirPath, req.Path)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	result := h.queue.Enqueue(fileops.Op{
		Type:     fileops.OpDelete,
		Dst:      targetPath,
		Username: username,
	})
	if result.Err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("删除失败", "path", req.Path, "error", result.Err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	h.logger.Info("文件已删除", "path", req.Path, "user", username)
}

func (h *FileHandler) HandleRename(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	username, _ := r.Context().Value(ContextKeyUsername).(string)
	var req struct {
		Path string `json:"path"`
		Dst  string `json:"dst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" || req.Dst == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := fileutil.ValidatePath(req.Path); err != nil {
		http.Error(w, "Bad Request: Forbidden path", http.StatusBadRequest)
		return
	}
	if err := fileutil.ValidatePath(req.Dst); err != nil {
		http.Error(w, "Bad Request: Forbidden destination path", http.StatusBadRequest)
		return
	}
	srcPath := filepath.Join(h.cfg.DirPath, req.Path)
	dstPath := filepath.Join(h.cfg.DirPath, req.Dst)
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	result := h.queue.Enqueue(fileops.Op{
		Type:     fileops.OpRename,
		Src:      srcPath,
		Dst:      dstPath,
		Username: username,
	})
	if result.Err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("重命名失败", "src", req.Path, "dst", req.Dst, "error", result.Err)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"path": result.Path})
	h.logger.Info("文件已重命名", "src", req.Path, "final", result.Path, "user", username)
}

func (h *FileHandler) HandleCopy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	username, _ := r.Context().Value(ContextKeyUsername).(string)
	var req struct {
		Path string `json:"path"`
		Dst  string `json:"dst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" || req.Dst == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := fileutil.ValidatePath(req.Path); err != nil {
		http.Error(w, "Bad Request: Forbidden path", http.StatusBadRequest)
		return
	}
	if err := fileutil.ValidatePath(req.Dst); err != nil {
		http.Error(w, "Bad Request: Forbidden destination path", http.StatusBadRequest)
		return
	}
	srcPath := filepath.Join(h.cfg.DirPath, req.Path)
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// 复制到临时目录，不入队，不阻塞
	tempPath := filepath.Join(h.cfg.TempDir, fmt.Sprintf("%d-copy-%s.tmp", time.Now().UnixNano(), filepath.Base(req.Path)))
	if err := fileutil.CopyFile(srcPath, tempPath); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("复制失败", "src", req.Path, "error", err)
		return
	}

	// 入队原子落盘
	dstPath := filepath.Join(h.cfg.DirPath, req.Dst)
	result := h.queue.Enqueue(fileops.Op{
		Type:     fileops.OpRename,
		Src:      tempPath,
		Dst:      dstPath,
		Username: username,
	})
	if result.Err != nil {
		os.Remove(tempPath)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("复制落盘失败", "dst", req.Dst, "error", result.Err)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"path": result.Path})
	h.logger.Info("文件已复制", "src", req.Path, "final", result.Path, "user", username)
}

func (h *FileHandler) HandleMkdir(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	username, _ := r.Context().Value(ContextKeyUsername).(string)
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := fileutil.ValidatePath(req.Path); err != nil {
		http.Error(w, "Bad Request: Forbidden path", http.StatusBadRequest)
		return
	}
	targetPath := filepath.Join(h.cfg.DirPath, req.Path)
	result := h.queue.Enqueue(fileops.Op{
		Type:     fileops.OpMkdir,
		Dst:      targetPath,
		Username: username,
	})
	if result.Err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		h.logger.Error("创建目录失败", "path", req.Path, "error", result.Err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	h.logger.Info("目录已创建", "path", req.Path, "user", username)
}
