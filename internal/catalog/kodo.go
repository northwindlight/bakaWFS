package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/qiniu/go-sdk/v7/auth"
	"github.com/qiniu/go-sdk/v7/client"
	"github.com/qiniu/go-sdk/v7/storage"
)

const MaxSnapshotBytes = 32 * 1024 * 1024

type KodoStore struct {
	bucket   string
	prefix   string
	creds    *auth.Credentials
	manager  *storage.BucketManager
	uploader *storage.FormUploader
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
		manager: storage.NewBucketManager(creds, cfg), uploader: storage.NewFormUploader(cfg)}, nil
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
	data, err := io.ReadAll(io.LimitReader(output, MaxSnapshotBytes+1))
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
	policy := storage.PutPolicy{Scope: s.bucket + ":" + key, InsertOnly: 1, Expires: 300, FsizeLimit: MaxSnapshotBytes}
	var result storage.PutRet
	err := s.uploader.Put(ctx, &result, policy.UploadToken(s.creds), key, bytes.NewReader(data), int64(len(data)), &storage.PutExtra{MimeType: "application/json"})
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
