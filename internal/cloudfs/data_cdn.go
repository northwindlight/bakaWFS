package cloudfs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/qiniu/go-sdk/v7/storagev2/downloader"
)

var errCDNMissing = errors.New("CDN domain not found")

// UseDownloadDomain selects an explicitly configured private data CDN. Internal
// reads and catalog traffic continue to use authenticated storage SDK endpoints.
func (k *Kodo) UseDownloadDomain(domain string) error {
	u, e := url.Parse(domain)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid private download domain")
	}
	k.urls = downloader.NewStaticDomainBasedURLsProvider([]string{strings.TrimRight(domain, "/")})
	k.customDomain = true
	return nil
}
func (k *Kodo) DataCDN(ctx context.Context, domain, certificate, registration, testKey string, create bool) (map[string]any, error) {
	data, e := k.cdnRequest(ctx, "GET", domain, "", nil)
	if errors.Is(e, errCDNMissing) && create {
		body := map[string]any{"type": "normal", "platform": "web", "geoCover": "china", "protocol": "https", "ipTypes": 3, "registerNo": registration, "source": map[string]any{"sourceType": "qiniuBucket", "sourceQiniuBucket": k.Bucket, "sourceURLScheme": "https", "testURLPath": "/" + testKey}, "testURLPath": "/" + testKey, "cache": map[string]any{"cacheControls": []map[string]any{{"time": 1, "timeunit": 6, "type": "all", "rule": "*"}}, "ignoreParam": false}, "bsauth": map[string]any{"enable": true, "isQiniuPrivate": true}, "responseHeaderControls": []map[string]string{{"op": "set", "key": "Access-Control-Allow-Origin", "value": "*"}, {"op": "set", "key": "Access-Control-Expose-Headers", "value": "ETag, Content-Length, Content-Range, Content-Disposition, Accept-Ranges"}}, "https": map[string]any{"certId": certificate, "forceHttps": true, "http2Enable": true}}
		if _, e = k.cdnRequest(ctx, "POST", domain, "", body); e != nil {
			return nil, e
		}
		data, e = k.cdnRequest(ctx, "GET", domain, "", nil)
	}
	if e != nil {
		return nil, e
	}
	source, _ := data["source"].(map[string]any)
	if source["sourceQiniuBucket"] != k.Bucket {
		return nil, fmt.Errorf("data domain belongs to another source")
	}
	return data, nil
}
func (k *Kodo) ConfigureDataHeaders(ctx context.Context, domain string) error {
	_, e := k.cdnRequest(ctx, "PUT", domain, "/respheader", map[string]any{"responseHeaderControls": []map[string]string{{"op": "set", "key": "Access-Control-Allow-Origin", "value": "*"}, {"op": "set", "key": "Access-Control-Expose-Headers", "value": "ETag, Content-Length, Content-Range, Content-Disposition, Accept-Ranges"}}})
	return e
}

func (k *Kodo) ConfigureDataAuth(ctx context.Context, domain string) error {
	_, e := k.cdnRequest(ctx, "PUT", domain, "/bsauth", map[string]any{"enable": true, "isQiniuPrivate": true})
	return e
}
