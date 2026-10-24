package cloudfs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"bakaWFS/internal/catalog"
	"bakaWFS/internal/handler"
)

// Service keeps file bytes out of SCF responses. The private object store owns
// data; immutable catalog snapshots alone decide which objects are visible.
var ErrObjectMismatch = errors.New("uploaded object mismatch")

type Service struct {
	Kodo                 *Kodo
	Catalog              *catalog.Catalog
	CallbackBase, Secret string
	Manga                bool
}
type treeNode struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Size     int64       `json:"size"`
	Children []*treeNode `json:"children,omitempty"`
	URL      string      `json:"url,omitempty"`
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
func actor(r *http.Request) string {
	a, _ := r.Context().Value(handler.ContextKeyUsername).(string)
	return a
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 256*1024+1))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("invalid request")
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid request")
	}
	return nil
}
func safePath(p string) ([]string, error) {
	if strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return nil, fmt.Errorf("invalid path")
	}
	if p == "" {
		return nil, nil
	}
	a := strings.Split(p, "/")
	for _, s := range a {
		if s == "" || s == "." || s == ".." {
			return nil, fmt.Errorf("invalid path")
		}
	}
	return a, nil
}
func resolve(s *catalog.Snapshot, p string) (catalog.Node, error) {
	a, e := safePath(p)
	if e != nil {
		return catalog.Node{}, e
	}
	n := s.Nodes["root"]
	for _, name := range a {
		found := false
		for _, v := range s.Nodes {
			if v.Parent == n.ID && v.Name == name {
				n = v
				found = true
				break
			}
		}
		if !found {
			return catalog.Node{}, fmt.Errorf("path not found")
		}
	}
	return n, nil
}
func destination(s *catalog.Snapshot, p string) (catalog.Node, string, error) {
	a, e := safePath(p)
	if e != nil || len(a) == 0 {
		return catalog.Node{}, "", fmt.Errorf("invalid destination")
	}
	parent, e := resolve(s, strings.Join(a[:len(a)-1], "/"))
	if e == nil && parent.Kind != "dir" {
		e = fmt.Errorf("parent is not a directory")
	}
	return parent, a[len(a)-1], e
}
func nodePath(s *catalog.Snapshot, n catalog.Node) string {
	a := []string{}
	for n.ID != "root" && n.ID != "" {
		a = append(a, n.Name)
		n = s.Nodes[n.Parent]
	}
	for i, j := 0, len(a)-1; i < j; i, j = i+1, j-1 {
		a[i], a[j] = a[j], a[i]
	}
	return strings.Join(a, "/")
}
func (h *Service) current(w http.ResponseWriter, r *http.Request) *catalog.Snapshot {
	min, _ := strconv.ParseUint(r.URL.Query().Get("min_revision"), 10, 64)
	s, e := h.Catalog.Current(r.Context(), min)
	if e != nil {
		failure(w, 503, "catalog_unavailable")
		return nil
	}
	w.Header().Set("X-Catalog-Revision", strconv.FormatUint(s.Revision, 10))
	return s
}
func (h *Service) Ready(w http.ResponseWriter, r *http.Request) {
	s := h.current(w, r)
	if s != nil {
		respond(w, 200, map[string]any{"status": "ready", "revision": s.Revision, "nodes": len(s.Nodes)})
	}
}
func (h *Service) List(w http.ResponseWriter, r *http.Request) {
	s := h.current(w, r)
	if s == nil {
		return
	}
	n, e := resolve(s, r.URL.Query().Get("path"))
	if e != nil || n.Kind != "dir" {
		failure(w, 404, "directory_not_found")
		return
	}
	children := map[string][]catalog.Node{}
	for _, v := range s.Nodes {
		children[v.Parent] = append(children[v.Parent], v)
	}
	depth := 2
	if r.URL.Path == "/tree" {
		depth = 65
	}
	var build func(catalog.Node, int) *treeNode
	build = func(n catalog.Node, d int) *treeNode {
		out := &treeNode{ID: n.ID, Name: n.Name, Type: n.Kind, Size: n.Size}
		if n.ID == "root" {
			if h.Manga {
				out.Name = "漫画"
			} else {
				out.Name = "files"
			}
		}
		if n.Kind == "dir" && d > 0 {
			ns := children[n.ID]
			sort.Slice(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
			for _, c := range ns {
				out.Children = append(out.Children, build(c, d-1))
			}
			if out.Children == nil {
				out.Children = []*treeNode{}
			}
		}
		if n.Kind == "file" && depth == 2 {
			out.URL, _ = h.Kodo.URL(r.Context(), n.ObjectKey, "", time.Hour)
		}
		return out
	}
	respond(w, 200, build(n, depth))
}
func (h *Service) File(w http.ResponseWriter, r *http.Request) {
	s := h.current(w, r)
	if s == nil {
		return
	}
	n, e := resolve(s, strings.TrimPrefix(r.URL.Path, "/files/"))
	if e != nil || n.Kind != "file" {
		failure(w, 404, "file_not_found")
		return
	}
	command := ""
	if r.URL.Query().Get("download") == "1" {
		command = "attname=" + url.QueryEscape(n.Name)
	}
	u, e := h.Kodo.URL(r.Context(), n.ObjectKey, command, time.Hour)
	if e != nil {
		failure(w, 503, "object_unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u, 302)
}
func thumbKey(n catalog.Node, size string) string {
	return "thumbs/" + strings.TrimPrefix(n.ObjectKey, "objects/") + "/" + size + ".jpg"
}
func (h *Service) Thumb(w http.ResponseWriter, r *http.Request) {
	if !h.Manga {
		failure(w, 404, "not_found")
		return
	}
	s := h.current(w, r)
	if s == nil {
		return
	}
	n, e := resolve(s, strings.TrimPrefix(r.URL.Path, "/thumb/"))
	if e != nil || n.Kind != "file" {
		failure(w, 404, "file_not_found")
		return
	}
	size := "list"
	if r.URL.Query().Get("size") == "mid" {
		size = "mid"
	}
	key := thumbKey(n, size)
	if _, e = h.Kodo.Stat(r.Context(), key); errors.Is(e, ErrObjectMissing) {
		e = h.makeThumbnails(r.Context(), n)
	}
	if e != nil {
		failure(w, 404, "thumbnail_unavailable")
		return
	}
	u, e := h.Kodo.URL(r.Context(), key, "", time.Hour)
	if e != nil {
		failure(w, 503, "object_unavailable")
		return
	}
	http.Redirect(w, r, u, 302)
}
func (h *Service) Thumbs(w http.ResponseWriter, r *http.Request) {
	if !h.Manga {
		failure(w, 404, "not_found")
		return
	}
	var q struct {
		Paths []string `json:"paths"`
	}
	if decode(r, &q) != nil || len(q.Paths) > 100 {
		failure(w, 400, "invalid_paths")
		return
	}
	s := h.current(w, r)
	if s == nil {
		return
	}
	out := []map[string]string{}
	for _, p := range q.Paths {
		n, e := resolve(s, p)
		if e != nil || n.Kind != "file" {
			continue
		}
		u, e := h.Kodo.URL(r.Context(), thumbKey(n, "list"), "", time.Hour)
		if e == nil {
			out = append(out, map[string]string{"path": p, "url": u})
		}
	}
	respond(w, 200, out)
}

type mutationRequest struct {
	Path string `json:"path"`
	Dst  string `json:"dst,omitempty"`
	ID   string `json:"operation_id"`
}

func (h *Service) Mutate(w http.ResponseWriter, r *http.Request) {
	var q mutationRequest
	if decode(r, &q) != nil || len(q.ID) < 8 || len(q.ID) > 128 {
		failure(w, 400, "operation_id_required")
		return
	}
	s := h.current(w, r)
	if s == nil {
		return
	}
	kind := strings.TrimPrefix(r.URL.Path, "/")
	if kind == "rename" {
		kind = "move"
	}
	hash := digest([]any{kind, q})
	if receipt, ok := s.Receipts[actor(r)+"\x00"+q.ID]; ok {
		if receipt.RequestHash != hash {
			failure(w, 409, "operation_id_reused")
			return
		}
		h.result(w, receipt.Result, nil)
		return
	}
	op := catalog.Operation{ID: q.ID, Actor: actor(r), Kind: kind, RequestHash: hash}
	if kind != "mkdir" {
		n, e := resolve(s, q.Path)
		if e != nil {
			failure(w, 404, "source_not_found")
			return
		}
		op.NodeID = n.ID
	}
	if kind != "delete" {
		dst := q.Dst
		if kind == "mkdir" {
			dst = q.Path
		}
		p, name, e := destination(s, dst)
		if e != nil {
			failure(w, 400, "invalid_destination")
			return
		}
		op.ParentID = p.ID
		op.Name = name
	}
	result, e := h.Catalog.Apply(r.Context(), op)
	h.result(w, result, e)
}
func (h *Service) result(w http.ResponseWriter, result catalog.Result, e error) {
	if errors.Is(e, ErrObjectMismatch) {
		failure(w, 409, "uploaded_object_mismatch_start_new_upload")
		return
	}
	if errors.Is(e, catalog.ErrCapacity) {
		failure(w, 507, "catalog_capacity_reached")
		return
	}
	if e != nil {
		failure(w, 503, "commit_uncertain_retry_same_operation")
		return
	}
	w.Header().Set("X-Catalog-Revision", strconv.FormatUint(result.Revision, 10))
	status := 200
	if result.Error != "" {
		status = 409
	}
	respond(w, status, result)
}
func (h *Service) callbackMAC(id string) string {
	m := hmac.New(sha256.New, []byte(h.Secret))
	m.Write([]byte("kodo-callback:" + id))
	return hex.EncodeToString(m.Sum(nil))
}
func (h *Service) FinishJob(ctx context.Context, j catalog.Job) (catalog.Result, error) {
	if j.Kind == "remote" && j.StageKey != "" {
		if _, e := h.Kodo.Stat(ctx, j.ObjectKey); errors.Is(e, ErrObjectMissing) {
			if e = h.Kodo.CopyIfAbsent(ctx, j.StageKey, j.ObjectKey); e != nil {
				return catalog.Result{}, e
			}
		} else if e != nil {
			return catalog.Result{}, e
		}
	}
	info, e := h.Kodo.Stat(ctx, j.ObjectKey)
	if e != nil {
		return catalog.Result{}, e
	}
	if info.Size > maxUpload || (j.Kind == "upload" && (info.Size != j.Size || info.Hash != j.Hash)) {
		if _, err := h.Catalog.Apply(ctx, catalog.Operation{ID: "invalid-object:" + j.ID, Actor: j.Actor, Kind: "job_fail", JobID: j.ID}); err != nil {
			return catalog.Result{}, err
		}
		return catalog.Result{}, ErrObjectMismatch
	}
	result, e := h.Catalog.Apply(ctx, catalog.Operation{ID: "finish:" + j.ID, Actor: j.Actor, Kind: "job_finish", JobID: j.ID, ObjectKey: j.ObjectKey, Size: info.Size, Hash: info.Hash})
	if e == nil && result.Error == "" && h.Manga {
		n := catalog.Node{ID: result.NodeID, Name: j.Name, ObjectKey: j.ObjectKey, Size: info.Size, Hash: info.Hash, Kind: "file"}
		h.makeThumbnails(ctx, n)
	}
	return result, e
}

// A callback only has authority over its pre-existing job. Provider stat, then
// catalog CAS, prevents a forged body or a late upload resurrecting a parent.
func (h *Service) Callback(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("job")
	if id == "" || !hmac.Equal([]byte(r.URL.Query().Get("mac")), []byte(h.callbackMAC(id))) {
		failure(w, 401, "invalid_callback")
		return
	}
	s := h.current(w, r)
	if s == nil {
		return
	}
	j, ok := s.Jobs[id]
	if !ok {
		failure(w, 404, "job_not_found")
		return
	}
	if j.State == "cancelled" || j.State == "failed" {
		h.Kodo.Delete(r.Context(), j.ObjectKey)
		if j.StageKey != "" {
			h.Kodo.Delete(r.Context(), j.StageKey)
		}
		respond(w, 200, map[string]bool{"cancelled": true})
		return
	}
	result, e := h.FinishJob(r.Context(), j)
	h.result(w, result, e)
}
