package cloudfs

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bakaWFS/internal/catalog"
	"github.com/qiniu/go-sdk/v7/storage"
	"github.com/qiniu/go-sdk/v7/storagev2/apis"
)

const ScheduledHeader = "X-Bakawfs-Trusted-Schedule"
const ObjectRetention = 7 * 24 * time.Hour

// referencedKeys protects visible blobs and all live upload reservations.
// Uploads always reserve a unique fresh key, and expire after 24h. Therefore
// an unreferenced blob older than seven days cannot acquire a new live owner.
// Copy/move can only reference blobs that are already visible in a snapshot.
func referencedKeys(s *catalog.Snapshot, now time.Time) map[string]bool {
	keep := map[string]bool{}
	for _, n := range s.Nodes {
		if n.Kind == "file" {
			keep[n.ObjectKey] = true
		}
	}
	for _, j := range s.Jobs {
		if j.State == "pending" && j.Expires >= now.Unix() {
			keep[j.ObjectKey] = true
			if j.StageKey != "" {
				keep[j.StageKey] = true
			}

		}
	}
	return keep
}
func (h *Service) Maintain(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(ScheduledHeader) != "true" {
		failure(w, 403, "scheduler_only")
		return
	}
	s := h.current(w, r)
	if s == nil {
		return
	}
	now := time.Now()
	recovered, expired := 0, 0
	for _, j := range s.Jobs {
		if recovered+expired >= 25 {
			break
		}
		if j.State != "pending" {
			continue
		}
		if j.Expires < now.Unix() {
			_, e := h.Catalog.Apply(r.Context(), catalog.Operation{ID: "expired:" + j.ID, Actor: j.Actor, Kind: "job_fail", JobID: j.ID})
			if e != nil {
				failure(w, 503, "maintenance_retry")
				return
			}
			expired++
			continue
		}
		if j.Kind == "remote" {
			if result, e := h.FinishJob(r.Context(), j); e == nil {
				if e == nil && result.Error == "" {
					recovered++
				}
			}
		}
	}
	deleted := 0
	// Once daily at 02:00 China time; all other invocations only recover jobs.
	if now.UTC().Hour() == 18 && now.UTC().Minute() < 15 {
		var e error
		deleted, e = h.collect(r.Context(), now, 5000)
		if e != nil {
			failure(w, 503, "collection_retry")
			return
		}
	}
	respond(w, 200, map[string]int{"recovered": recovered, "expired": expired, "collected": deleted})
}
func (h *Service) collect(ctx context.Context, now time.Time, limit int) (int, error) {
	s, e := h.Catalog.Current(ctx, 0)
	if e != nil {
		return 0, e
	}
	keep := referencedKeys(s, now)
	var candidates []string
scan:
	for _, prefix := range []string{"objects/", "thumbs/", "staging/"} {
		marker := ""
		for {
			page, e := h.Kodo.api.GetObjects(ctx, &apis.GetObjectsRequest{Bucket: h.Kodo.Bucket, Prefix: prefix, Marker: marker, Limit: 1000}, nil)
			if e != nil {
				return 0, fmt.Errorf("object listing failed")
			}
			for _, object := range page.Items {
				if keep[object.Key] || time.Unix(0, object.PutTime*100).After(now.Add(-ObjectRetention)) {
					continue
				}
				if !strings.HasPrefix(object.Key, prefix) {
					return 0, fmt.Errorf("unexpected collection prefix")
				}
				candidates = append(candidates, object.Key)
				if len(candidates) >= limit {
					break scan
				}
			}
			marker = page.Marker
			if marker == "" {
				break
			}
		}
	}
	// Recheck the latest authority immediately before deletion. Listing is only
	// an advisory source of old candidates, never the authority for live paths.
	latest, e := h.Catalog.Current(ctx, s.Revision)
	if e != nil {
		return 0, e
	}
	live := referencedKeys(latest, now)
	var operations []string
	for _, key := range candidates {
		if !live[key] {
			operations = append(operations, storage.URIDelete(h.Kodo.Bucket, key))
		}
	}
	deleted := 0
	for start := 0; start < len(operations); start += 1000 {
		results, e := h.Kodo.manager.BatchWithContext(ctx, h.Kodo.Bucket, operations[start:min(start+1000, len(operations))])
		if e != nil {
			return deleted, fmt.Errorf("object collection failed")
		}
		if len(results) != min(1000, len(operations)-start) {
			return deleted, fmt.Errorf("incomplete collection result")
		}
		for _, r := range results {
			if r.Code != 200 && r.Code != 612 {
				return deleted, fmt.Errorf("object collection rejected")
			}
			deleted++
		}
	}
	return deleted, nil
}

// PurgeLegacyThumbnails removes only the retired generated namespace. Visible
// original objects and catalog snapshots are outside this fixed prefix.
func (k *Kodo) PurgeLegacyThumbnails(ctx context.Context) (int, int64, error) {
	if e := k.EnsurePrivateBucket(ctx, false); e != nil {
		return 0, 0, e
	}
	count := 0
	var bytes int64
	for {
		page, e := k.api.GetObjects(ctx, &apis.GetObjectsRequest{Bucket: k.Bucket, Prefix: "thumbs/", Limit: 1000}, nil)
		if e != nil {
			return count, bytes, fmt.Errorf("cannot list retired thumbnails")
		}
		if len(page.Items) == 0 {
			return count, bytes, nil
		}
		operations := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			if !strings.HasPrefix(item.Key, "thumbs/") {
				return count, bytes, fmt.Errorf("invalid thumbnail cleanup prefix")
			}
			operations = append(operations, storage.URIDelete(k.Bucket, item.Key))
		}
		results, e := k.manager.BatchWithContext(ctx, k.Bucket, operations)
		if e != nil || len(results) != len(operations) {
			return count, bytes, fmt.Errorf("thumbnail cleanup outcome unknown")
		}
		for i, result := range results {
			if result.Code != 200 && result.Code != 612 {
				return count, bytes, fmt.Errorf("thumbnail cleanup rejected")
			}
			count++
			bytes += page.Items[i].Size
		}
	}
}
