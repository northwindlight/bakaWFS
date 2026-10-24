package catalog

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/qiniu/go-sdk/v7/auth"
	"github.com/qiniu/go-sdk/v7/client"
	"github.com/qiniu/go-sdk/v7/storage"
	"github.com/qiniu/go-sdk/v7/storagev2/apis"
	"github.com/qiniu/go-sdk/v7/storagev2/http_client"
)

const MaxSnapshotBytes = 32 * 1024 * 1024

type KodoStore struct {
	bucket   string
	prefix   string
	creds    *auth.Credentials
	manager  *storage.BucketManager
	uploader *storage.FormUploader
	api      *apis.Storage
}

// NewKodoStore requires an explicit namespace. Metadata should be stored in a
// private bucket; callers must verify bucket access policy before real imports.
func NewKodoStore(accessKey, secretKey, bucket, prefix string) (*KodoStore, error) {
	if accessKey == "" || secretKey == "" || bucket == "" || prefix == "" || strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") {
		return nil, fmt.Errorf("explicit Kodo credentials, bucket and safe catalog prefix are required")
	}
	creds := auth.New(accessKey, secretKey)
	cfg := &storage.Config{UseHTTPS: true}
	return &KodoStore{bucket: bucket, prefix: strings.TrimSuffix(prefix, "/") + "/", creds: creds,
		manager: storage.NewBucketManager(creds, cfg), uploader: storage.NewFormUploader(cfg), api: apis.NewStorage(&http_client.Options{Credentials: creds})}, nil
}

func (s *KodoStore) key(revision uint64) string {
	return fmt.Sprintf("%s%020d.json", s.prefix, revision)
}

func (s *KodoStore) Read(ctx context.Context, revision uint64) ([]byte, error) {
	output, err := s.manager.Get(s.bucket, s.key(revision), &storage.GetObjectInput{Context: ctx})
	if output != nil {
		defer output.Close()
	}
	if err != nil {
		var providerError *client.ErrorInfo
		if errors.As(err, &providerError) && (providerError.Code == 612 || providerError.Code == 404) {
			return nil, ErrNotFound
		}
		// SDK errors can contain signed request URLs. Do not expose them.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Kodo catalog read failed at revision %d", revision)
	}
	var input io.Reader = output
	// Existing plain JSON snapshots remain readable during rolling updates.
	prefix := make([]byte, 2)
	n, readErr := io.ReadFull(output, prefix)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return nil, fmt.Errorf("snapshot header read failed")
	}
	input = io.MultiReader(bytes.NewReader(prefix[:n]), output)
	if n == 2 && prefix[0] == 0x1f && prefix[1] == 0x8b {
		gz, e := gzip.NewReader(input)
		if e != nil {
			return nil, fmt.Errorf("invalid compressed snapshot")
		}
		defer gz.Close()
		input = gz
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxSnapshotBytes+1))
	if err != nil {
		return nil, fmt.Errorf("Kodo catalog body read failed at revision %d", revision)
	}
	if len(data) > MaxSnapshotBytes {
		return nil, fmt.Errorf("snapshot exceeds size limit")
	}
	return data, nil
}

func (s *KodoStore) Create(ctx context.Context, revision uint64, data []byte) error {
	if len(data) > MaxSnapshotBytes {
		return fmt.Errorf("snapshot exceeds size limit")
	}
	key := s.key(revision)
	policy := storage.PutPolicy{Scope: s.bucket + ":" + key, InsertOnly: 1, Expires: 300, FsizeLimit: MaxSnapshotBytes + 1024*1024}
	var compressed bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if _, e := gz.Write(data); e != nil {
		return e
	}
	if e := gz.Close(); e != nil {
		return e
	}
	body := compressed.Bytes()
	var result storage.PutRet
	err := s.uploader.Put(ctx, &result, policy.UploadToken(s.creds), key, bytes.NewReader(body), int64(len(body)), &storage.PutExtra{MimeType: "application/gzip"})
	if err == nil {
		return nil
	}
	var providerError *client.ErrorInfo
	if errors.As(err, &providerError) && providerError.Code == 614 {
		return ErrExists
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("Kodo catalog create outcome unknown at revision %d; retry the same operation", revision)
}

// Exists locates immutable numbered slots without downloading older complete
// snapshots. It is a lookup optimization; Current still reads the winning slot.
func (s *KodoStore) Exists(ctx context.Context, revision uint64) (bool, error) {
	_, err := s.api.StatObject(ctx, &apis.StatObjectRequest{Entry: s.bucket + ":" + s.key(revision)}, nil)
	if err == nil {
		return true, nil
	}
	var e *client.ErrorInfo
	if errors.As(err, &e) && (e.Code == 612 || e.Code == 404) {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return false, fmt.Errorf("catalog slot lookup failed")
}
