// Package scf adapts SCF Function URL control requests to the existing HTTP API.
// File bytes must go directly between the browser and object storage.
package scf

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const maxControlBody = 256 * 1024

// Event follows Function URL's event protocol, not API Gateway's base64 protocol.
type Event struct {
	HTTPMethod  string            `json:"httpMethod"`
	Path        string            `json:"path"`
	Headers     map[string]string `json:"headers"`
	QueryString map[string]string `json:"queryString"`
	Body        string            `json:"body"`
}

type Response struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

type Adapter struct {
	handler     http.Handler
	instance    string
	initialized time.Time
	invocations atomic.Uint64
}

func NewAdapter(handler http.Handler) *Adapter {
	return &Adapter{handler: handler, instance: rand.Text(), initialized: time.Now().UTC()}
}

func (a *Adapter) Handle(ctx context.Context, event Event) (Response, error) {
	started := time.Now()
	sequence := a.invocations.Add(1)
	result := a.handle(ctx, event)
	result.Headers["X-BakaWFS-Instance"] = a.instance
	result.Headers["X-BakaWFS-Invocation"] = strconv.FormatUint(sequence, 10)
	result.Headers["X-BakaWFS-Initialized-At"] = a.initialized.Format(time.RFC3339Nano)
	result.Headers["X-BakaWFS-Handler-Ms"] = fmt.Sprintf("%.3f", float64(time.Since(started))/float64(time.Millisecond))
	return result, nil
}

func failure(status int, code string) Response {
	return Response{StatusCode: status, Headers: map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store"}, Body: fmt.Sprintf(`{"error":%q}`, code)}
}

func (a *Adapter) handle(ctx context.Context, event Event) Response {
	if len(event.Body) > maxControlBody {
		return failure(http.StatusRequestEntityTooLarge, "control_body_too_large")
	}
	if event.HTTPMethod == "" || !strings.HasPrefix(event.Path, "/") || strings.HasPrefix(event.Path, "//") {
		return failure(http.StatusBadRequest, "invalid_http_event")
	}
	u, err := url.ParseRequestURI(event.Path)
	if err != nil || u.IsAbs() || u.RawQuery != "" || u.Fragment != "" {
		return failure(http.StatusBadRequest, "invalid_path")
	}
	q := make(url.Values, len(event.QueryString))
	for key, value := range event.QueryString {
		q.Set(key, value)
	}
	u.RawQuery = q.Encode()
	r, err := http.NewRequestWithContext(ctx, event.HTTPMethod, u.String(), strings.NewReader(event.Body))
	if err != nil {
		return failure(http.StatusBadRequest, "invalid_http_event")
	}
	for key, value := range event.Headers {
		r.Header.Set(key, value)
	}
	r.Host = r.Header.Get("Host")
	if ip := net.ParseIP(r.Header.Get("X-Scf-Remote-Addr")); ip != nil {
		r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
	}
	// The event body is already materialized. A client header cannot redefine its length.
	r.ContentLength = int64(len(event.Body))
	w := &responseWriter{header: make(http.Header)}
	a.handler.ServeHTTP(w, r)
	if !utf8.Valid(w.body.Bytes()) {
		return failure(http.StatusBadGateway, "binary_response_requires_direct_storage_url")
	}
	headers := make(map[string]string, len(w.header))
	for key, values := range w.header {
		headers[key] = strings.Join(values, ", ")
	}
	headers["Cache-Control"] = "no-store"
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	return Response{StatusCode: status, Headers: headers, Body: w.body.String()}
}

type responseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		if w.header.Get("Content-Type") == "" {
			w.header.Set("Content-Type", http.DetectContentType(body))
		}
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(body)
}
