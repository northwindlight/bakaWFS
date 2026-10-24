package scf

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func testEnvironment() map[string]string {
	return map[string]string{
		"BAKAWFS_JWT_SECRET": "isolated-test-secret",
		"BAKAWFS_USERS_YAML": "users:\n  - username: admin\n    password: test-password\n    role: admin\n  - username: baka\n    password: baka\n    role: guest\n  - username: unknown\n    password: test-password\n    role: other\n",
		"BAKAWFS_MANGA_MODE": "true",
	}
}

func testAPI(env map[string]string) *Adapter {
	return NewAdapter(NewBootstrap(func(key string) string { return env[key] }, slog.New(slog.NewTextHandler(io.Discard, nil))))
}

func invoke(t *testing.T, api *Adapter, event Event, status int) Response {
	t.Helper()
	response, err := api.Handle(context.Background(), event)
	if err != nil || response.StatusCode != status {
		t.Fatalf("status=%d, wanted=%d, err=%v, body=%s", response.StatusCode, status, err, response.Body)
	}
	if response.Headers["Cache-Control"] != "no-store" {
		t.Fatal("control response must not be cached")
	}
	return response
}

func TestAuthAcrossInstances(t *testing.T) {
	for _, account := range []struct{ name, password, role string }{{"admin", "test-password", "admin"}, {"baka", "baka", "guest"}, {"unknown", "test-password", "guest"}} {
		t.Run(account.name, func(t *testing.T) {
			first := testAPI(testEnvironment())
			body, _ := json.Marshal(map[string]string{"username": account.name, "password": account.password})
			login := invoke(t, first, Event{HTTPMethod: "POST", Path: "/login", Headers: map[string]string{"user-agent": "browser"}, Body: string(body)}, 200)
			var claims struct{ Token, Role string }
			if err := json.Unmarshal([]byte(login.Body), &claims); err != nil || claims.Token == "" || claims.Role != account.role {
				t.Fatalf("invalid login response: %v", err)
			}
			second := testAPI(testEnvironment())
			event := Event{HTTPMethod: "POST", Path: "/verify", Headers: map[string]string{"user-agent": "browser", "authorization": "Bearer " + claims.Token}}
			verified := invoke(t, second, event, 200)
			if first.instance == second.instance || verified.Headers["X-BakaWFS-Invocation"] != "1" {
				t.Fatal("test must use independent function instances")
			}
			if err := json.Unmarshal([]byte(verified.Body), &claims); err != nil || claims.Role != account.role {
				t.Fatal("refresh changed role")
			}
			event.Headers["user-agent"] = "another-browser"
			invoke(t, second, event, 401)
			delete(event.Headers, "authorization")
			event.Headers["user-agent"] = "browser"
			event.QueryString = map[string]string{"token": claims.Token}
			invoke(t, second, event, 200)
		})
	}
}

func TestBootstrapRoutes(t *testing.T) {
	api := testAPI(testEnvironment())
	invoke(t, api, Event{HTTPMethod: "GET", Path: "/healthz"}, 200)
	config := invoke(t, api, Event{HTTPMethod: "GET", Path: "/api/config"}, 200)
	if !strings.Contains(config.Body, `"auth_mode":true`) || !strings.Contains(config.Body, `"manga_mode":true`) {
		t.Fatal(config.Body)
	}
	invoke(t, api, Event{HTTPMethod: "GET", Path: "/readyz"}, 503)
	invoke(t, api, Event{HTTPMethod: "POST", Path: "/verify"}, 401)
	invoke(t, api, Event{HTTPMethod: "GET", Path: "/login"}, 405)
	invoke(t, api, Event{HTTPMethod: "POST", Path: "/login", Body: `{"username":"admin","password":"wrong"}`}, 401)
	invoke(t, api, Event{HTTPMethod: "POST", Path: "/login", Body: strings.Repeat(" ", 11*1024) + `{}`}, 400)
	for _, path := range []string{"/list", "/tree", "/upload", "/delete", "/files/private.jpg"} {
		invoke(t, api, Event{HTTPMethod: "POST", Path: path}, 404)
	}
	preflight := invoke(t, api, Event{HTTPMethod: "OPTIONS", Path: "/login", Headers: map[string]string{"origin": "https://cloud.northwind.site"}}, 200)
	if preflight.Headers["Access-Control-Allow-Origin"] != "*" {
		t.Fatal("missing CORS")
	}
}

func TestEnvironmentValidation(t *testing.T) {
	for _, mutation := range []struct{ key, value string }{
		{"BAKAWFS_JWT_SECRET", ""}, {"BAKAWFS_AUTH_MODE", "invalid"}, {"BAKAWFS_MANGA_MODE", "invalid"},
		{"BAKAWFS_USERS_YAML", ""}, {"BAKAWFS_USERS_YAML", "users: []"},
		{"BAKAWFS_USERS_YAML", "users: private-password"},
		{"BAKAWFS_USERS_YAML", "users: [{username: a, password: b}, {username: a, password: c}]"},
		{"BAKAWFS_USERS_YAML", "users: [{username: a, password: ''}]"},
		{"BAKAWFS_USERS_YAML", "users: [{username: a, password: b, typo: c}]"},
	} {
		t.Run(mutation.key+mutation.value, func(t *testing.T) {
			env := testEnvironment()
			env[mutation.key] = mutation.value
			_, _, err := LoadEnvironment(func(key string) string { return env[key] })
			if err == nil || strings.Contains(err.Error(), "private-password") {
				t.Fatal("configuration must fail without quoting secret input")
			}
			response := invoke(t, testAPI(env), Event{HTTPMethod: "GET", Path: "/healthz"}, 503)
			if strings.Contains(response.Body, "private-password") {
				t.Fatal("secret leaked to HTTP response")
			}
		})
	}
	// Construct before settings exist, mirroring SDK invocation-time injection.
	env := map[string]string{}
	api := testAPI(env)
	for key, value := range testEnvironment() {
		env[key] = value
	}
	invoke(t, api, Event{HTTPMethod: "GET", Path: "/healthz"}, 200)
}

func TestEventTranslation(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	api := NewAdapter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/封面 #100%.jpg" || r.URL.Query().Get("q") != "#100%+&漫画" || r.URL.Query().Get("once") != "%23" {
			t.Errorf("corrupted path/query: %s", r.URL)
		}
		if d, ok := r.Context().Deadline(); !ok || !d.Equal(deadline) || r.RemoteAddr != "[2001:db8::1]:0" || r.Header.Get("User-Agent") != "browser" {
			t.Error("lost context or headers")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		w.WriteHeader(500)
		w.Write([]byte(`{"ok":true}`))
	}))
	event := Event{HTTPMethod: "GET", Path: "/封面 #100%.jpg", QueryString: map[string]string{"q": "#100%+&漫画", "once": "%23"}, Headers: map[string]string{"user-agent": "browser", "x-scf-remote-addr": "2001:db8::1"}}
	response, err := api.Handle(ctx, event)
	if err != nil || response.StatusCode != 201 || response.Body != `{"ok":true}` {
		t.Fatal(response, err)
	}
}

func TestMalformedEvents(t *testing.T) {
	api := NewAdapter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid event reached handler") }))
	for _, event := range []Event{
		{}, {HTTPMethod: "GET", Path: "https://example.com/"}, {HTTPMethod: "GET", Path: "//example.com/"},
		{HTTPMethod: "GET", Path: "/bad\x00"}, {HTTPMethod: "bad method", Path: "/"},
	} {
		invoke(t, api, event, 400)
	}
	invoke(t, api, Event{HTTPMethod: "POST", Path: "/login", Body: strings.Repeat("x", maxControlBody+1)}, 413)
}

func TestProbeConcurrentReuse(t *testing.T) {
	api := NewAdapter(NewProbe())
	const count = 32
	var wg sync.WaitGroup
	sequences := make(chan string, count)
	for range count {
		wg.Go(func() {
			response := invoke(t, api, Event{HTTPMethod: "GET", Path: "/healthz"}, 200)
			sequences <- response.Headers["X-BakaWFS-Invocation"]
			if response.Headers["X-BakaWFS-Instance"] != api.instance {
				t.Error("instance identity changed")
			}
		})
	}
	wg.Wait()
	close(sequences)
	seen := make(map[string]bool)
	for sequence := range sequences {
		if seen[sequence] {
			t.Fatal("duplicate invocation number")
		}
		seen[sequence] = true
	}
	if len(seen) != count {
		t.Fatal("lost calls")
	}
}

func TestDecodedFunctionURLPathPreservesLiteralEscapes(t *testing.T) {
	for _, path := range []string{"/files/中文 #100%.txt", "/files/literal %23.txt", "/files/literal %2F.txt", "/files/literal ?.txt", "/files/字面?token=not-a-query"} {
		t.Run(path, func(t *testing.T) {
			api := NewAdapter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path || r.URL.RawQuery != "" {
					t.Fatalf("path changed: %q query %q", r.URL.Path, r.URL.RawQuery)
				}
				w.Write([]byte("ok"))
			}))
			invoke(t, api, Event{HTTPMethod: "GET", Path: path}, 200)
		})
	}
}
