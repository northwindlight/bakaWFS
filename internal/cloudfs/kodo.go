package cloudfs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/qiniu/go-sdk/v7/auth"
	"github.com/qiniu/go-sdk/v7/client"
	"github.com/qiniu/go-sdk/v7/storage"
	"github.com/qiniu/go-sdk/v7/storagev2/apis"
	"github.com/qiniu/go-sdk/v7/storagev2/apis/set_bucket_cors_rules"
	"github.com/qiniu/go-sdk/v7/storagev2/downloader"
	"github.com/qiniu/go-sdk/v7/storagev2/http_client"
)

var ErrObjectMissing = errors.New("object missing")

type ObjectInfo struct {
	Size int64  `json:"size"`
	Hash string `json:"hash"`
	Mime string `json:"mime"`
}

type Fingerprint struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	ETag   string `json:"etag"`
	XXHash string `json:"xxhash"`
}

type Kodo struct {
	Bucket   string
	creds    *auth.Credentials
	api      *apis.Storage
	manager  *storage.BucketManager
	uploader *storage.FormUploader
	urls     downloader.DownloadURLsProvider
}

func ReadKeys(filename string) (string, string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", "", fmt.Errorf("cannot read credential file")
	}
	defer f.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "="); ok {
			values[k] = strings.Trim(v, "\"'")
		}
	}
	if scanner.Err() != nil || values["QINIU_AK"] == "" || values["QINIU_SK"] == "" {
		return "", "", fmt.Errorf("credential file is incomplete")
	}
	return values["QINIU_AK"], values["QINIU_SK"], nil
}

func NewKodo(ak, sk, bucket string) (*Kodo, error) {
	if ak == "" || sk == "" || bucket == "" {
		return nil, fmt.Errorf("Kodo credentials and private bucket are required")
	}
	creds := auth.New(ak, sk)
	cfg := &storage.Config{UseHTTPS: true}
	return &Kodo{Bucket: bucket, creds: creds, api: apis.NewStorage(&http_client.Options{Credentials: creds}), manager: storage.NewBucketManager(creds, cfg), uploader: storage.NewFormUploader(cfg), urls: downloader.NewDefaultSrcURLsProvider(ak, nil)}, nil
}

func (k *Kodo) EnsurePrivateBucket(ctx context.Context, create bool) error {
	buckets, err := k.manager.Buckets(false)
	if err != nil {
		return fmt.Errorf("cannot list owned buckets")
	}
	found := false
	for _, b := range buckets {
		if b == k.Bucket {
			found = true
		}
	}
	if !found {
		if !create {
			return fmt.Errorf("private bucket is missing")
		}
		if _, err := k.api.CreateBucket(ctx, &apis.CreateBucketRequest{Bucket: k.Bucket, Region: "z0"}, nil); err != nil {
			return fmt.Errorf("cannot create private library bucket")
		}
		if err := k.manager.MakeBucketPrivate(k.Bucket); err != nil {
			return fmt.Errorf("cannot make new bucket private")
		}
	}
	info, err := k.manager.GetBucketInfo(k.Bucket)
	if err != nil || !info.IsPrivate() {
		return fmt.Errorf("library bucket must be private; refusing existing public bucket")
	}
	return nil
}

func (k *Kodo) ConfigureCORS(ctx context.Context) error {
	// The legacy SDK AddCorsRules incorrectly copies AllowedMethod into
	// AllowedOrigin in v7.26.12. Use the generated v2 API with explicit fields.
	_, err := k.api.SetBucketCORSRules(ctx, &apis.SetBucketCORSRulesRequest{Bucket: k.Bucket, CORSRules: set_bucket_cors_rules.CORSRules{{AllowedOrigin: []string{"*"}, AllowedMethod: []string{"GET", "HEAD", "POST", "PUT", "DELETE"}, AllowedHeader: []string{"Authorization", "Content-Type", "Content-MD5", "Range", "X-Requested-With", "*"}, ExposedHeader: []string{"ETag", "Content-Length", "Content-Range", "Content-Disposition", "Accept-Ranges"}, MaxAge: 600}}}, nil)
	if err != nil {
		return fmt.Errorf("cannot configure library CORS")
	}
	return nil
}

func (k *Kodo) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	r, err := k.api.StatObject(ctx, &apis.StatObjectRequest{Entry: k.Bucket + ":" + key}, nil)
	if err != nil {
		var e *client.ErrorInfo
		if errors.As(err, &e) && (e.Code == 612 || e.Code == 404) {
			return ObjectInfo{}, ErrObjectMissing
		}
		if ctx.Err() != nil {
			return ObjectInfo{}, ctx.Err()
		}
		return ObjectInfo{}, fmt.Errorf("object stat failed")
	}
	return ObjectInfo{Size: r.Size, Hash: r.Hash, Mime: r.MimeType}, nil
}

func (k *Kodo) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := k.manager.Get(k.Bucket, key, &storage.GetObjectInput{Context: ctx})
	if err != nil {
		if r != nil {
			r.Close()
		}
		var providerError *client.ErrorInfo
		if errors.As(err, &providerError) && (providerError.Code == 612 || providerError.Code == 404) {
			return nil, ErrObjectMissing
		}
		return nil, fmt.Errorf("object download failed")
	}
	return r, nil
}

func (k *Kodo) URL(ctx context.Context, key, command string, ttl time.Duration) (string, error) {
	// Kodo default origin rejects deadlines more than five minutes ahead.
	// Keep margin for clock skew; callers refresh expired URLs through the API.
	ttl = min(ttl, 240*time.Second)
	iter, err := k.urls.GetURLsIter(ctx, key, &downloader.GenerateOptions{BucketName: k.Bucket, Command: command})
	if err != nil {
		return "", fmt.Errorf("cannot discover object endpoint")
	}
	var u url.URL
	ok, err := iter.Peek(&u)
	if err != nil || !ok || u.Scheme != "https" {
		return "", fmt.Errorf("no HTTPS object endpoint")
	}
	if err := downloader.NewCredentialsSigner(k.creds).Sign(ctx, &u, &downloader.SignOptions{TTL: ttl}); err != nil {
		return "", fmt.Errorf("cannot sign object URL")
	}
	return u.String(), nil
}

func (k *Kodo) UploadToken(key string, size int64, expires uint64) string {
	p := storage.PutPolicy{Scope: k.Bucket + ":" + key, InsertOnly: 1, Expires: expires, FsizeMin: size, FsizeLimit: size}
	return p.UploadToken(k.creds)
}

func bytesETag(data []byte) string {
	var blocks []byte
	for start := 0; start < len(data); start += 4 * 1024 * 1024 {
		sum := sha1.Sum(data[start:min(start+4*1024*1024, len(data))])
		blocks = append(blocks, sum[:]...)
	}
	if len(blocks) == 0 {
		sum := sha1.Sum(nil)
		blocks = sum[:]
	}
	digest := append([]byte{0x16}, blocks...)
	if len(blocks) > 20 {
		sum := sha1.Sum(blocks)
		digest = append([]byte{0x96}, sum[:]...)
	}
	return base64.RawURLEncoding.EncodeToString(digest)
}
func (k *Kodo) PutBytes(ctx context.Context, key string, data []byte, contentType string) (ObjectInfo, error) {
	expected := bytesETag(data)
	for attempt := 0; attempt < 4; attempt++ {
		var result storage.PutRet
		p := storage.PutPolicy{Scope: k.Bucket + ":" + key, InsertOnly: 1, Expires: 3600, FsizeLimit: int64(len(data))}
		k.uploader.Put(ctx, &result, p.UploadToken(k.creds), key, bytes.NewReader(data), int64(len(data)), &storage.PutExtra{MimeType: contentType})
		// A response can disappear after commit. Stat recovers that outcome and
		// rejects an existing immutable object with different bytes, including 614.
		info, e := k.Stat(ctx, key)
		if e == nil {
			if info.Size != int64(len(data)) || info.Hash != expected {
				return ObjectInfo{}, fmt.Errorf("existing immutable object differs")
			}
			return info, nil
		}
		if ctx.Err() != nil {
			return ObjectInfo{}, ctx.Err()
		}
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return ObjectInfo{}, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return ObjectInfo{}, fmt.Errorf("object upload outcome unknown")
}

func (k *Kodo) PutFile(ctx context.Context, key, filename string, expected Fingerprint) error {
	if existing, err := k.Stat(ctx, key); err == nil {
		if existing.Size == expected.Size && existing.Hash == expected.ETag {
			return nil
		}
		return fmt.Errorf("existing immutable object differs")
	} else if !errors.Is(err, ErrObjectMissing) {
		return err
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var result storage.PutRet
	p := storage.PutPolicy{Scope: k.Bucket + ":" + key, InsertOnly: 1, Expires: 7200, FsizeLimit: expected.Size}
	err := k.uploader.PutFile(ctx, &result, p.UploadToken(k.creds), key, filename, &storage.PutExtra{MimeType: contentType})
	if err != nil {
		var e *client.ErrorInfo
		if !errors.As(err, &e) || e.Code != 614 {
			return fmt.Errorf("file upload outcome unknown")
		}
	}
	info, err := k.Stat(ctx, key)
	if err != nil {
		return err
	}
	if info.Size != expected.Size || info.Hash != expected.ETag {
		return fmt.Errorf("remote size/hash mismatch")
	}
	return nil
}

func FingerprintFile(filename string) (Fingerprint, error) {
	f, err := os.Open(filename)
	if err != nil {
		return Fingerprint{}, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return Fingerprint{}, fmt.Errorf("not a regular input file")
	}
	sha := sha256.New()
	xx := xxhash.New()
	reader := io.TeeReader(f, io.MultiWriter(sha, xx))
	buf := make([]byte, 4*1024*1024)
	var blocks []byte
	var first []byte
	var size int64
	for {
		n, err := io.ReadFull(reader, buf)
		if n > 0 {
			sum := sha1.Sum(buf[:n])
			if first == nil {
				first = append([]byte(nil), sum[:]...)
			}
			blocks = append(blocks, sum[:]...)
			size += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return Fingerprint{}, err
		}
	}
	var digest []byte
	if size <= int64(len(buf)) {
		if first == nil {
			sum := sha1.Sum(nil)
			first = sum[:]
		}
		digest = append([]byte{0x16}, first...)
	} else {
		sum := sha1.Sum(blocks)
		digest = append([]byte{0x96}, sum[:]...)
	}
	after, err := f.Stat()
	if err != nil || before.Size() != after.Size() || before.ModTime() != after.ModTime() || size != before.Size() {
		return Fingerprint{}, fmt.Errorf("input changed while hashing")
	}
	return Fingerprint{Size: size, SHA256: hex.EncodeToString(sha.Sum(nil)), ETag: base64.RawURLEncoding.EncodeToString(digest), XXHash: fmt.Sprintf("%016x", xx.Sum64())}, nil
}

func (k *Kodo) Delete(ctx context.Context, key string) error {
	_, err := k.api.DeleteObject(ctx, &apis.DeleteObjectRequest{Entry: k.Bucket + ":" + key}, nil)
	if err != nil {
		var e *client.ErrorInfo
		if errors.As(err, &e) && e.Code == 612 {
			return nil
		}
		return fmt.Errorf("object cleanup failed")
	}
	return nil
}

func (k *Kodo) CheckReadURL(ctx context.Context, key string) (int, error) {
	u, err := k.URL(ctx, key, "", time.Minute)
	if err != nil {
		return 0, err
	}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("object HTTPS check failed")
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// The native RS copy with force=false creates the immutable published object.
// AsyncFetch may retry/replace a staging object, which is never exposed in paths.
func (k *Kodo) CopyIfAbsent(ctx context.Context, source, destination string) error {
	_, err := k.api.CopyObject(ctx, &apis.CopyObjectRequest{SrcEntry: k.Bucket + ":" + source, DestEntry: k.Bucket + ":" + destination, IsForce: false}, nil)
	if err != nil {
		var e *client.ErrorInfo
		if errors.As(err, &e) && e.Code == 614 {
			return nil
		}
		if errors.As(err, &e) && e.Code == 612 {
			return ErrObjectMissing
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("immutable object copy outcome unknown")
	}
	return nil
}
