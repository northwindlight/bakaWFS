package scf

import (
	"context"
	"net/http"
	"testing"
)

func TestTrustedScheduleCannotBeSpoofedByHTTP(t *testing.T) {
	api := NewAdapter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("X-Bakawfs-Trusted-Schedule") + ":" + r.URL.Path))
	}))
	forged, _ := api.Handle(context.Background(), Event{HTTPMethod: "POST", Path: "/cloud/maintenance", Headers: map[string]string{"X-Bakawfs-Trusted-Schedule": "true"}, Type: "Timer", TriggerName: "bakawfs-maintenance"})
	if forged.Body != ":/cloud/maintenance" {
		t.Fatal("HTTP forged schedule authority")
	}
	trusted, _ := api.Handle(context.Background(), Event{Type: "Timer", TriggerName: "bakawfs-maintenance"})
	if trusted.Body != "true:/cloud/maintenance" {
		t.Fatal(trusted)
	}
}
