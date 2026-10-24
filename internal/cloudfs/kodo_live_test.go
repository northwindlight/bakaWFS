package cloudfs

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"bakaWFS/internal/catalog"
	"bytes"
	"github.com/qiniu/go-sdk/v7/storage"
	"github.com/qiniu/go-sdk/v7/storagev2/apis"
)

// This explicitly opted-in test requires an empty, dedicated validation bucket.
// It cannot run against the imported manga bucket or load production user files.
func TestLiveRetentionCollection(t *testing.T) {
	bucket := os.Getenv("BAKAWFS_TEST_KODO_BUCKET")
	keys := os.Getenv("BAKAWFS_TEST_KODO_KEYS")
	if bucket == "" || keys == "" {
		t.Skip("dedicated live validation bucket not configured")
	}
	if !strings.Contains(bucket, "validation") {
		t.Fatal("refusing a non-validation bucket")
	}
	ctx := context.Background()
	ak, sk, e := ReadKeys(keys)
	if e != nil {
		t.Fatal(e)
	}
	k, e := NewKodo(ak, sk, bucket)
	if e != nil {
		t.Fatal(e)
	}
	if e = k.EnsurePrivateBucket(ctx, true); e != nil {
		t.Fatal(e)
	}
	page, e := k.api.GetObjects(ctx, &apis.GetObjectsRequest{Bucket: bucket, Limit: 1}, nil)
	if e != nil || len(page.Items) != 0 {
		t.Fatal("validation bucket must be empty")
	}
	known := []string{"objects/live", "objects/orphan", "objects/pending", "thumbs/live/list.jpg", "thumbs/live/mid.jpg", "thumbs/orphan/list.jpg", "staging/replayed", "objects/published-copy"}
	defer func() {
		for _, key := range known {
			k.Delete(ctx, key)
		}
		for i := 0; i <= 1; i++ {
			k.Delete(ctx, fmt.Sprintf("catalog/probe/%020d.json", i))
		}
		k.api.DeleteBucket(ctx, &apis.DeleteBucketRequest{Bucket: bucket}, nil)
	}()
	var live ObjectInfo
	for _, key := range known {
		if key == "objects/published-copy" {
			continue
		}
		info, e := k.PutBytes(ctx, key, []byte("isolated retention fixture"), "application/octet-stream")
		if e != nil {
			t.Fatal(e)
		}
		if key == "objects/live" {
			live = info
		}
	}
	if e = k.SetObjectCache(ctx, "staging/replayed", "no-cache, max-age=0, must-revalidate"); e != nil {
		t.Fatal(e)
	}
	if e = k.CopyIfAbsent(ctx, "staging/replayed", "objects/published-copy"); e != nil {
		t.Fatal(e)
	}
	signed, e := k.URL(ctx, "objects/published-copy", "", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	request, _ := http.NewRequestWithContext(ctx, "HEAD", signed, nil)
	response, e := http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal("copy header verification transport failed")
	}
	response.Body.Close()
	if !strings.Contains(response.Header.Get("Cache-Control"), "max-age=0") {
		t.Fatal("native copy did not retain entry cache metadata")
	}
	var replaced storage.PutRet
	policy := storage.PutPolicy{Scope: bucket + ":staging/replayed", Expires: 300}
	changed := []byte("changed staging after an asynchronous retry")
	if e = k.uploader.Put(ctx, &replaced, policy.UploadToken(k.creds), "staging/replayed", bytes.NewReader(changed), int64(len(changed)), nil); e != nil {
		t.Fatal("staging replacement failed")
	}
	if e = k.CopyIfAbsent(ctx, "staging/replayed", "objects/published-copy"); e != nil {
		t.Fatal(e)
	}
	final, e := k.Stat(ctx, "objects/published-copy")
	if e != nil || final.Hash != live.Hash {
		t.Fatal("late staging write overwrote immutable published object")
	}
	store, e := catalog.NewKodoStore(ak, sk, bucket, "catalog/probe/")
	if e != nil {
		t.Fatal(e)
	}
	c := catalog.New(store)
	if e = c.Seed(ctx, map[string]catalog.Node{"root": {ID: "root", Kind: "dir"}, "copy": {ID: "copy", Kind: "file", Parent: "root", Name: "copy.bin", ObjectKey: "objects/published-copy", Size: live.Size, Hash: live.Hash}, "live": {ID: "live", Kind: "file", Parent: "root", Name: "live.bin", ObjectKey: "objects/live", Size: live.Size, Hash: live.Hash}}); e != nil {
		t.Fatal(e)
	}
	future := time.Now().Add(8 * 24 * time.Hour)
	result, e := c.Apply(ctx, catalog.Operation{ID: "pending", Actor: "test", Kind: "job_start", Job: &catalog.Job{ID: "pending", Actor: "test", Kind: "upload", ParentID: "root", Name: "pending.bin", ObjectKey: "objects/pending", Expires: future.Add(time.Hour).Unix()}})
	if e != nil || result.Error != "" {
		t.Fatal(result, e)
	}
	h := &Service{Kodo: k, Catalog: c}
	deleted, e := h.collect(ctx, future, 100)
	if e != nil || deleted != 5 {
		t.Fatalf("collection: %d %v", deleted, e)
	}
	for _, key := range []string{"objects/live", "objects/pending", "objects/published-copy"} {
		if _, e = k.Stat(ctx, key); e != nil {
			t.Fatalf("collected protected blob %s", key)
		}
	}
	for _, key := range []string{"objects/orphan", "thumbs/orphan/list.jpg"} {
		if _, e = k.Stat(ctx, key); e != ErrObjectMissing {
			t.Fatalf("orphan retained: %s", key)
		}
	}
	t.Log("real Kodo gzip snapshots, live/pending protection and batch orphan collection passed")
}
