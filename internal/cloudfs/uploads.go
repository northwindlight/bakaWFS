package cloudfs

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bakaWFS/internal/catalog"
	"bakaWFS/internal/task"
	"bakaWFS/internal/thumb"
	"github.com/qiniu/go-sdk/v7/storagev2/apis"
	complete "github.com/qiniu/go-sdk/v7/storagev2/apis/resumable_upload_v2_complete_multipart_upload"
	"github.com/qiniu/go-sdk/v7/storagev2/uptoken"
)

const maxUpload = 100 * 1024 * 1024 * 1024
const partSize = 8 * 1024 * 1024

func uploadPartSize(size int64) int64 {
	return max(partSize, ((size+9999)/10000+1024*1024-1)/(1024*1024)*(1024*1024))
}

type uploadRequest struct {
	ID       string `json:"operation_id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Hash     string `json:"etag"`
	URL      string `json:"url,omitempty"`
}

func (h *Service) Prepare(w http.ResponseWriter, r *http.Request) {
	var q uploadRequest
	if decode(r, &q) != nil || len(q.ID) < 8 || len(q.ID) > 128 || q.Size < 0 || q.Size > maxUpload {
		failure(w, 400, "invalid_upload")
		return
	}
	remote := r.URL.Path == "/remote-upload"
	if remote {
		if h.CallbackBase == "" || task.ValidateRemoteURL(q.URL) != nil || strings.Contains(q.URL, ";") {
			failure(w, 400, "invalid_remote_url")
			return
		}
	} else if len(q.Hash) != 28 {
		failure(w, 400, "etag_required")
		return
	}
	s := h.current(w, r)
	if s == nil {
		return
	}
	id := digest([]string{actor(r), q.ID})
	hash := digest(q)
	j, exists := s.Jobs[id]
	if exists {
		if j.RequestHash != hash || j.Actor != actor(r) {
			failure(w, 409, "operation_id_reused")
			return
		}
	} else {
		parent, name, e := destination(s, q.Filename)
		if e != nil {
			failure(w, 400, "invalid_destination")
			return
		}
		if remote {
			for _, active := range s.Jobs {
				if active.Actor == actor(r) && active.Kind == "remote" && active.State == "pending" && active.Expires >= time.Now().Unix() && active.ParentID == parent.ID && active.Name == name {
					failure(w, 409, "remote_task_already_pending")
					return
				}
			}
		}
		kind := "upload"
		if remote {
			kind = "remote"
		}
		j = catalog.Job{ID: id, Actor: actor(r), Kind: kind, ParentID: parent.ID, Name: name, ObjectKey: "objects/upload-" + id, Size: q.Size, Hash: q.Hash, RequestHash: hash, SourceURL: q.URL}
		if remote {
			j.StageKey = "staging/remote-" + id
		}
		result, e := h.Catalog.Apply(r.Context(), catalog.Operation{ID: "start:" + id, Actor: j.Actor, Kind: "job_start", Job: &j})
		if e != nil || result.Error != "" {
			h.result(w, result, e)
			return
		}
		j.State = "pending"
	}
	if j.State != "pending" {
		respond(w, 200, map[string]any{"job_id": j.ID, "state": j.State})
		return
	}
	if remote {
		if j.UploadID == "" {
			cb := strings.TrimRight(h.CallbackBase, "/") + "/cloud/callback?job=" + id + "&mac=" + h.callbackMAC(id)
			ret, e := h.Kodo.api.AsyncFetchObject(r.Context(), &apis.AsyncFetchObjectRequest{Url: q.URL, Bucket: h.Kodo.Bucket, Key: j.StageKey, CallbackUrl: cb, CallbackBody: "key=$(key)&hash=$(etag)&size=$(fsize)", IgnoreSameKey: false}, nil)
			if e != nil {
				failure(w, 503, "fetch_submission_uncertain_retry_same_operation")
				return
			}
			result, e := h.Catalog.Apply(r.Context(), catalog.Operation{ID: "fetch-id:" + id, Actor: j.Actor, Kind: "job_upload_id", JobID: id, UploadID: ret.Id})
			if e != nil || result.Error != "" {
				h.result(w, result, e)
				return
			}
		}
		respond(w, 202, map[string]string{"job_id": id, "state": "pending"})
		return
	}
	if j.Expires != 0 && j.Expires < time.Now().Unix() {
		failure(w, 409, "job_expired_start_new_upload")
		return
	}
	token := h.Kodo.UploadToken(j.ObjectKey, j.Size, 3600)
	if j.Size > 10*1024*1024 && j.UploadID == "" {
		ret, e := h.Kodo.api.ResumableUploadV2InitiateMultipartUpload(r.Context(), &apis.ResumableUploadV2InitiateMultipartUploadRequest{BucketName: h.Kodo.Bucket, ObjectName: &j.ObjectKey, UpToken: uptoken.NewParser(token)}, nil)
		if e != nil {
			failure(w, 503, "multipart_init_failed")
			return
		}
		result, e := h.Catalog.Apply(r.Context(), catalog.Operation{ID: "upload-id:" + id + ":" + ret.UploadId, Actor: j.Actor, Kind: "job_upload_id", JobID: id, UploadID: ret.UploadId})
		if e != nil {
			h.result(w, result, e)
			return
		}
		latest, e := h.Catalog.Current(r.Context(), 0)
		if e != nil {
			failure(w, 503, "catalog_unavailable")
			return
		}
		j = latest.Jobs[id]
		if j.UploadID != ret.UploadId {
			h.Kodo.abort(r.Context(), j.ObjectKey, ret.UploadId, token)
		}
		if result.Error != "" && j.UploadID == "" {
			h.result(w, result, nil)
			return
		}
	}
	existingParts := complete.Parts{}
	if j.UploadID != "" {
		marker := int64(0)
		for {
			page, e := h.Kodo.api.ResumableUploadV2ListParts(r.Context(), &apis.ResumableUploadV2ListPartsRequest{BucketName: h.Kodo.Bucket, ObjectName: &j.ObjectKey, UploadId: j.UploadID, UpToken: uptoken.NewParser(token), MaxParts: 1000, PartNumberMarker: marker}, nil)
			if e != nil {
				failure(w, 503, "multipart_resume_lookup_failed")
				return
			}
			for _, part := range page.Parts {
				existingParts = append(existingParts, complete.PartInfo{PartNumber: part.PartNumber, Etag: part.Etag})
			}
			if page.PartNumberMarker == 0 {
				break
			}
			marker = page.PartNumberMarker
		}
	}
	respond(w, 200, map[string]any{"job_id": id, "state": j.State, "token": token, "key": j.ObjectKey, "upload_url": "https://upload.qiniup.com", "multipart_url": "https://upload.qiniup.com/buckets/" + url.PathEscape(h.Kodo.Bucket) + "/objects/" + base64.URLEncoding.EncodeToString([]byte(j.ObjectKey)) + "/uploads/" + j.UploadID, "upload_id": j.UploadID, "part_size": uploadPartSize(j.Size), "parts": existingParts})
}
func (k *Kodo) abort(ctx context.Context, key, id, token string) {
	if id == "" {
		return
	}
	k.api.ResumableUploadV2AbortMultipartUpload(ctx, &apis.ResumableUploadV2AbortMultipartUploadRequest{BucketName: k.Bucket, ObjectName: &key, UploadId: id, UpToken: uptoken.NewParser(token)}, nil)
}

type finishRequest struct {
	JobID string         `json:"job_id"`
	Parts complete.Parts `json:"parts,omitempty"`
}

func (h *Service) Complete(w http.ResponseWriter, r *http.Request) {
	var q finishRequest
	if decode(r, &q) != nil || len(q.Parts) > 10000 {
		failure(w, 400, "invalid_upload")
		return
	}
	s := h.current(w, r)
	if s == nil {
		return
	}
	j, ok := s.Jobs[q.JobID]
	if !ok || j.Actor != actor(r) {
		failure(w, 404, "job_not_found")
		return
	}
	if j.State == "published" {
		h.result(w, catalog.Result{Revision: s.Revision, NodeID: j.NodeID}, nil)
		return
	}
	if j.State != "pending" || j.Expires < time.Now().Unix() {
		failure(w, 409, "job_not_pending")
		return
	}
	if j.Kind != "upload" {
		failure(w, 400, "wrong_job_kind")
		return
	}
	_, e := h.Kodo.Stat(r.Context(), j.ObjectKey)
	if errors.Is(e, ErrObjectMissing) && j.UploadID != "" {
		expected := (j.Size + uploadPartSize(j.Size) - 1) / uploadPartSize(j.Size)
		if int64(len(q.Parts)) != expected {
			failure(w, 400, "incomplete_parts")
			return
		}
		for i, p := range q.Parts {
			if p.PartNumber != int64(i+1) || p.Etag == "" {
				failure(w, 400, "invalid_parts")
				return
			}
		}
		_, e = h.Kodo.api.ResumableUploadV2CompleteMultipartUpload(r.Context(), &apis.ResumableUploadV2CompleteMultipartUploadRequest{BucketName: h.Kodo.Bucket, ObjectName: &j.ObjectKey, UploadId: j.UploadID, UpToken: uptoken.NewParser(h.Kodo.UploadToken(j.ObjectKey, j.Size, 3600)), Parts: q.Parts, FileName: j.Name}, nil)
		if e != nil {
			failure(w, 503, "multipart_completion_uncertain_retry_same_job")
			return
		}
	} else if e != nil {
		failure(w, 503, "object_not_ready")
		return
	}
	result, e := h.FinishJob(r.Context(), j)
	h.result(w, result, e)
}
func (h *Service) Progress(w http.ResponseWriter, r *http.Request) {
	s := h.current(w, r)
	if s == nil {
		return
	}
	out := map[string]any{}
	for _, j := range s.Jobs {
		if j.Actor != actor(r) || j.Kind != "remote" || j.State != "pending" {
			continue
		}
		if j.Expires < time.Now().Unix() {
			h.Catalog.Apply(r.Context(), catalog.Operation{ID: "expired:" + j.ID, Actor: j.Actor, Kind: "job_fail", JobID: j.ID})
			continue
		}
		if result, e := h.FinishJob(r.Context(), j); e == nil {
			if e == nil && result.Error == "" {
				continue
			}
		}
		parent := s.Nodes[j.ParentID]
		p := nodePath(s, parent)
		if p != "" {
			p += "/"
		}
		out[p+j.Name] = map[string]any{"username": j.Actor, "downloadSize": 0, "expectedSize": j.Size, "job_id": j.ID, "state": "fetching"}
	}
	respond(w, 200, out)
}
func (h *Service) Cancel(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Filename string `json:"filename"`
		JobID    string `json:"job_id,omitempty"`
	}
	if decode(r, &q) != nil {
		failure(w, 400, "invalid_cancel")
		return
	}
	s := h.current(w, r)
	if s == nil {
		return
	}
	for _, j := range s.Jobs {
		if j.Actor != actor(r) {
			continue
		}
		p := nodePath(s, s.Nodes[j.ParentID])
		if p != "" {
			p += "/"
		}
		if j.ID != q.JobID && (q.JobID != "" || p+j.Name != q.Filename || j.State != "pending") {
			continue
		}
		result, e := h.Catalog.Apply(r.Context(), catalog.Operation{ID: "cancel:" + j.ID, Actor: j.Actor, Kind: "job_cancel", JobID: j.ID})
		if e == nil && result.Error == "" && j.Kind == "upload" {
			h.Kodo.abort(r.Context(), j.ObjectKey, j.UploadID, h.Kodo.UploadToken(j.ObjectKey, j.Size, 3600))
		}
		h.result(w, result, e)
		return
	}
	failure(w, 404, "job_not_found")
}
func (h *Service) makeThumbnails(ctx context.Context, n catalog.Node) error {
	if !thumb.Supported(n.Name) || n.Size > 128*1024*1024 {
		return fmt.Errorf("thumbnail input unsupported")
	}
	dir, e := os.MkdirTemp("", "cloud-thumb-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	src, e := h.Kodo.Get(ctx, n.ObjectKey)
	if e != nil {
		return e
	}
	defer src.Close()
	filename := filepath.Join(dir, "input"+filepath.Ext(n.Name))
	f, e := os.Create(filename)
	if e != nil {
		return e
	}
	written, e := io.Copy(f, io.LimitReader(src, 128*1024*1024+1))
	closeErr := f.Close()
	if e != nil || closeErr != nil || written != n.Size {
		return fmt.Errorf("thumbnail download failed")
	}
	imageFile, e := os.Open(filename)
	if e != nil {
		return e
	}
	dimensions, _, e := image.DecodeConfig(imageFile)
	imageFile.Close()
	if e != nil || dimensions.Width <= 0 || dimensions.Height <= 0 || int64(dimensions.Width)*int64(dimensions.Height) > 40_000_000 {
		return fmt.Errorf("thumbnail dimensions exceed cloud memory budget")
	}
	_, e = h.Kodo.ImportThumbnails(ctx, filename, strings.TrimPrefix(n.ObjectKey, "objects/"), filepath.Join(dir, "cache"))
	return e
}
