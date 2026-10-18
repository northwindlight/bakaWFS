package test

import (
	"bakaWFS/internal/config"
	"bakaWFS/internal/handler"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnifiedModeDefaultsAndConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.EnsureConfig(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadYAML[config.Config](path)
	if err != nil || cfg.MangaMode {
		t.Fatalf("ordinary mode must be default: %v", err)
	}
	for _, enabled := range []bool{false, true} {
		ah := handler.NewAuthHandler(nil, slog.New(slog.DiscardHandler), false, enabled)
		w := httptest.NewRecorder()
		ah.HandleServerConfig(w, httptest.NewRequest("GET", "/api/config", nil))
		var got map[string]bool
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["manga_mode"] != enabled {
			t.Fatal("frontend capability mismatch")
		}
	}
}

func TestOrdinaryModeRejectsThumbnailHandlers(t *testing.T) {
	h := handler.NewFileHandler(config.Config{}, slog.New(slog.DiscardHandler), nil, nil, nil)
	for _, call := range []http.HandlerFunc{h.HandleThumb, h.HandleThumbBatch} {
		w := httptest.NewRecorder()
		call(w, httptest.NewRequest("GET", "/thumb/test.png", nil))
		if w.Code != 404 {
			t.Fatalf("thumbnail handler enabled in ordinary mode: %d", w.Code)
		}
	}
}

func TestDeleteRejectsStorageRoot(t *testing.T) {
	root := t.TempDir()
	h := handler.NewFileHandler(config.Config{DirPath: root}, slog.New(slog.DiscardHandler), nil, nil, nil)
	for _, path := range []string{".", "./", "sub/.."} {
		w := httptest.NewRecorder()
		call := httptest.NewRequest("POST", "/delete", strings.NewReader(`{"path":"`+path+`"}`))
		h.HandleDelete(w, call)
		if w.Code != 400 {
			t.Fatalf("%q: got %d", path, w.Code)
		}
	}
}

func TestTrashOutsidePublicTree(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "files")
	if err := os.Mkdir(files, 0755); err != nil {
		t.Fatal(err)
	}
	for _, trash := range []string{files, filepath.Join(files, "trash"), root} {
		cfg := config.Config{DirPath: files, TrashDir: trash}
		if _, err := cfg.DeletionDir(); err == nil {
			t.Fatalf("unsafe trash accepted: %s", trash)
		}
	}
	cfg := config.Config{DirPath: files}
	got, err := cfg.DeletionDir()
	if err != nil || got != files+".bakawfs-trash" {
		t.Fatalf("default: %s %v", got, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(files, link); err != nil {
		t.Skip(err)
	}
	cfg.TrashDir = filepath.Join(link, "trash")
	if _, err := cfg.DeletionDir(); err == nil {
		t.Fatal("symlink into files accepted")
	}
}
