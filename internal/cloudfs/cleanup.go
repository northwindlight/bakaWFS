package cloudfs

import (
	"context"
	"fmt"
	"strings"

	"bakaWFS/internal/catalog"
	"github.com/qiniu/go-sdk/v7/storage"
)

// CleanupValidation requires a completed production catalog and a separate
// validation namespace. Shared original blobs are protected by production refs.
func (k *Kodo) CleanupValidation(ctx context.Context, prefix, production string) (int, error) {
	if !strings.HasPrefix(prefix, "catalog/validation-") || prefix == production {
		return 0, fmt.Errorf("explicit separate validation namespace required")
	}
	prodStore, e := catalog.NewKodoStore(k.creds.AccessKey, string(k.creds.SecretKey), k.Bucket, production)
	if e != nil {
		return 0, e
	}
	live, e := catalog.New(prodStore).Current(ctx, 0)
	if e != nil {
		return 0, fmt.Errorf("production catalog must be readable before cleanup")
	}
	keep := map[string]bool{}
	for _, n := range live.Nodes {
		if n.ObjectKey != "" {
			keep[n.ObjectKey] = true
		}
	}
	for _, j := range live.Jobs {
		keep[j.ObjectKey] = true
		keep[j.StageKey] = true
	}
	store, e := catalog.NewKodoStore(k.creds.AccessKey, string(k.creds.SecretKey), k.Bucket, prefix)
	if e != nil {
		return 0, e
	}
	test, e := catalog.New(store).Current(ctx, 0)
	if e != nil {
		return 0, e
	}
	keys := map[string]bool{}
	for _, n := range test.Nodes {
		if n.ObjectKey != "" && !keep[n.ObjectKey] {
			keys[n.ObjectKey] = true
		}
	}
	for _, j := range test.Jobs {
		if j.Kind == "upload" && j.UploadID != "" {
			k.abort(ctx, j.ObjectKey, j.UploadID, k.UploadToken(j.ObjectKey, j.Size, 3600))
		}
		for _, key := range []string{j.ObjectKey, j.StageKey} {
			if key != "" && !keep[key] {
				keys[key] = true
			}
		}
	}
	for revision := uint64(0); revision <= test.Revision; revision++ {
		keys[fmt.Sprintf("%s%020d.json", strings.TrimRight(prefix, "/")+"/", revision)] = true
	}
	var ops []string
	for key := range keys {
		ops = append(ops, storage.URIDelete(k.Bucket, key))
	}
	done := 0
	for start := 0; start < len(ops); start += 200 {
		batch := ops[start:min(start+200, len(ops))]
		result, e := k.manager.BatchWithContext(ctx, k.Bucket, batch)
		if e != nil || len(result) != len(batch) {
			return done, fmt.Errorf("validation cleanup incomplete")
		}
		for _, r := range result {
			if r.Code != 200 && r.Code != 612 {
				return done, fmt.Errorf("validation cleanup rejected")
			}
			done++
		}
	}
	return done, nil
}
