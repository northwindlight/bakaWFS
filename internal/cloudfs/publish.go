package cloudfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiniu/go-sdk/v7/cdn"
	"github.com/qiniu/go-sdk/v7/storagev2/apis"
)

// PublishUI uploads and verifies all immutable dependencies before replacing the
// entry point. Existing hashed assets remain available for already-open tabs.
func (k *Kodo) PublishUI(ctx context.Context, directory, domain, backup string) error {
	var assets []string
	err := filepath.WalkDir(directory, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			rel, e := filepath.Rel(directory, p)
			if e != nil {
				return e
			}
			rel = filepath.ToSlash(rel)
			if rel != "index.html" {
				assets = append(assets, rel)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, e := os.Stat(filepath.Join(directory, "index.html")); e != nil {
		return e
	}
	// Capture the old entry for a local rollback before making any public change.
	if e := os.MkdirAll(filepath.Dir(backup), 0700); e != nil {
		return e
	}
	if old, e := k.Get(ctx, "index.html"); e == nil {
		data, e := io.ReadAll(io.LimitReader(old, 2*1024*1024))
		old.Close()
		if e != nil {
			return e
		}
		if e = os.WriteFile(backup, data, 0600); e != nil {
			return e
		}
	} else if !errors.Is(e, ErrObjectMissing) {
		return fmt.Errorf("cannot back up existing entry")
	}
	for _, key := range assets {
		file := filepath.Join(directory, filepath.FromSlash(key))
		fp, e := FingerprintFile(file)
		if e != nil {
			return e
		}
		if e = k.PutFile(ctx, key, file, fp); e != nil {
			return fmt.Errorf("immutable UI asset upload failed: %s", key)
		}
	}
	// Verify bytes, rather than trusting that a matching-length object is intact.
	for _, key := range assets {
		want, e := os.ReadFile(filepath.Join(directory, filepath.FromSlash(key)))
		if e != nil {
			return e
		}
		r, e := k.Get(ctx, key)
		if e != nil {
			return e
		}
		sum := sha256.New()
		_, e = io.Copy(sum, r)
		r.Close()
		expected := sha256.Sum256(want)
		if e != nil || hex.EncodeToString(sum.Sum(nil)) != hex.EncodeToString(expected[:]) {
			return fmt.Errorf("UI dependency verification failed: %s", key)
		}
	}
	entryFile := filepath.Join(directory, "index.html")
	fingerprint, e := FingerprintFile(entryFile)
	if e != nil {
		return e
	}
	versionKey := "index." + fingerprint.SHA256[:12] + ".html"
	if e = k.PutFile(ctx, versionKey, entryFile, fingerprint); e != nil {
		return e
	}
	if e = k.SetObjectCache(ctx, versionKey, "no-cache, max-age=0, must-revalidate"); e != nil {
		return e
	}
	// Native copy atomically publishes the already-verified bytes AND cache
	// metadata. There is no interval where a new entry has a one-year cache.
	if _, e = k.api.CopyObject(ctx, &apis.CopyObjectRequest{SrcEntry: k.Bucket + ":" + versionKey, DestEntry: k.Bucket + ":index.html", IsForce: true}, nil); e != nil {
		return fmt.Errorf("entry replacement outcome unknown")
	}
	published, e := k.Stat(ctx, "index.html")
	if e != nil || published.Hash != fingerprint.ETag || published.Size != fingerprint.Size {
		return fmt.Errorf("entry verification failed")
	}
	// Refresh both root spellings; many CDNs cache '/' independently.
	refreshed, e := cdn.NewCdnManager(k.creds).RefreshUrls([]string{strings.TrimRight(domain, "/") + "/", strings.TrimRight(domain, "/") + "/index.html"})
	if e != nil || refreshed.Code != 200 {
		return fmt.Errorf("entry published, CDN refresh failed; retry refresh")
	}
	fmt.Printf("published and verified %d UI dependencies; entry replaced last at %s\n", len(assets), time.Now().UTC().Format(time.RFC3339))
	return nil
}

func (k *Kodo) cdnRequest(ctx context.Context, method, domain, suffix string, body any) (map[string]any, error) {
	u, e := url.Parse(domain)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("explicit HTTPS CDN domain required")
	}
	var data []byte
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil {
			return nil, e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, "https://api.qiniu.com/domain/"+url.PathEscape(u.Hostname())+suffix, bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Qiniu-Date", time.Now().UTC().Format("20060102T150405Z"))
	token, e := k.creds.SignRequestV2(req)
	if e != nil {
		return nil, fmt.Errorf("cannot authorize CDN configuration")
	}
	req.Header.Set("Authorization", "Qiniu "+token)
	response, e := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if e != nil {
		return nil, fmt.Errorf("CDN configuration transport failed")
	}
	defer response.Body.Close()
	var result map[string]any
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result)
	if response.StatusCode == 404 || result["code"] == float64(404001) || result["code"] == float64(400001) {
		return nil, errCDNMissing
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("invalid CDN configuration response")
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("CDN configuration rejected: HTTP %d, code %v, reason %v", response.StatusCode, result["code"], "see provider API code")
	}
	if code, ok := result["code"].(float64); ok && code != 200 {
		return nil, fmt.Errorf("CDN configuration returned code %.0f", code)
	}
	return result, nil
}
func (k *Kodo) InspectStaticCDN(ctx context.Context, domain string) (map[string]any, error) {
	r, e := k.cdnRequest(ctx, "GET", domain, "", nil)
	if e != nil {
		return nil, e
	}
	out := map[string]any{}
	for _, key := range []string{"name", "operatingState", "operation", "cache", "responseHeaderControls"} {
		out[key] = r[key]
	}
	return out, nil
}
func (k *Kodo) ConfigureStaticCDN(ctx context.Context, domain, backup string) error {
	current, e := k.cdnRequest(ctx, "GET", domain, "", nil)
	if e != nil {
		return e
	}
	data, _ := json.MarshalIndent(current, "", "  ")
	if e = os.MkdirAll(filepath.Dir(backup), 0700); e != nil {
		return e
	}
	file, e := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e == nil {
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	} else if !os.IsExist(e) {
		return e
	}
	desired := map[string]any{"cacheControls": []map[string]any{{"time": 1, "timeunit": 6, "type": "suffix", "rule": ".js;.css;.wasm;.woff;.woff2"}, {"time": 1, "timeunit": 6, "type": "suffix", "rule": ".png;.jpg;.jpeg;.webp;.svg"}, {"time": 1, "timeunit": 6, "type": "suffix", "rule": ".ico;.txt"}, {"time": 0, "timeunit": 0, "type": "all", "rule": "*"}}, "ignoreParam": false}
	cache, _ := current["cache"].(map[string]any)
	oldRules, _ := json.Marshal(cache["cacheControls"])
	newRules, _ := json.Marshal(desired["cacheControls"])
	if string(oldRules) != string(newRules) || cache["ignoreParam"] != false {
		if _, e = k.cdnRequest(ctx, "PUT", domain, "/cache", desired); e != nil {
			return e
		}
	}
	return nil
}

func (k *Kodo) SetObjectCache(ctx context.Context, key, value string) error {
	// SDK v7.26.12 predates a dedicated CacheControl field. Its generic chgm
	// segment map emits the documented /cacheControl/<base64> operation verbatim.
	_, e := k.api.ModifyObjectMetadata(ctx, &apis.ModifyObjectMetadataRequest{Entry: k.Bucket + ":" + key, MetaData: map[string]string{"cacheControl": value}}, nil)
	if e != nil {
		return fmt.Errorf("entry cache metadata update failed")
	}
	return nil
}
func (k *Kodo) SetEntryCache(ctx context.Context) error {
	return k.SetObjectCache(ctx, "index.html", "no-cache, max-age=0, must-revalidate")
}
