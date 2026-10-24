package main

import (
	"bakaWFS/internal/catalog"
	"bakaWFS/internal/cloudfs"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	mode := flag.String("mode", "inventory", "setup, inventory, upload, verify, publish")
	root := flag.String("root", "", "read-only source library")
	state := flag.String("state", "dist/migration", "private resumable local state")
	bucket := flag.String("bucket", "bakawfs-library-northwind", "private library bucket")
	keys := flag.String("keys", "/etc/qiniu/keys.env", "existing credential source")
	cache := flag.String("thumb-cache", ".thumbcache", "existing read-only thumbnail cache")
	prefix := flag.String("catalog-prefix", "catalog/v1/", "directory snapshot namespace")
	workers := flag.Int("workers", 6, "concurrent network clients")
	yes := flag.Bool("yes", false, "authorize cloud mutation")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *workers < 1 || *workers > 64 {
		fatal(fmt.Errorf("workers must be 1..64"))
	}
	if err := os.MkdirAll(*state, 0700); err != nil {
		fatal(err)
	}
	if *mode == "source-check" {
		fatal(cloudfs.VerifySource(*state, *workers))
		fmt.Println("source inventory and every original byte match the initial manifest")
		return
	}
	if *mode == "inventory" {
		if *root == "" {
			fatal(fmt.Errorf("root is required"))
		}
		_, err := cloudfs.Inventory(*root, *state, *workers)
		fatal(err)
		return
	}
	if (*mode == "setup" || *mode == "upload" || *mode == "publish") && !*yes {
		fatal(fmt.Errorf("cloud mutations require --yes"))
	}
	ak, sk, err := cloudfs.ReadKeys(*keys)
	if err != nil {
		fatal(err)
	}
	k, err := cloudfs.NewKodo(ak, sk, *bucket)
	if err != nil {
		fatal(err)
	}
	if *mode == "setup" {
		fatal(k.EnsurePrivateBucket(ctx, true))
		fatal(k.ConfigureCORS(ctx))
		fmt.Println("private library bucket and CORS ready")
		return
	}
	m, err := cloudfs.LoadManifest(*state)
	if err != nil {
		fatal(err)
	}
	switch *mode {
	case "upload":
		fatal(k.ImportFiles(ctx, m, *state, *cache, *workers))
	case "verify":
		fatal(k.VerifyImport(ctx, m, *state, *workers))
	case "publish":
		data, err := os.ReadFile(filepath.Join(*state, "verification.json"))
		if err != nil {
			fatal(fmt.Errorf("remote verification is required before publishing"))
		}
		var check struct {
			Passed   bool   `json:"passed"`
			Files    int    `json:"source_files"`
			Bytes    int64  `json:"source_bytes"`
			Manifest string `json:"manifest_sha256"`
		}
		if json.Unmarshal(data, &check) != nil || !check.Passed || check.Files != len(m.Files) || check.Bytes != m.Bytes || check.Manifest != m.Digest() {
			fatal(fmt.Errorf("verification does not match manifest"))
		}
		sourceData, e := os.ReadFile(filepath.Join(*state, "source-verification.json"))
		var source struct {
			Passed   bool   `json:"passed"`
			Manifest string `json:"manifest_sha256"`
		}
		if e != nil || json.Unmarshal(sourceData, &source) != nil || !source.Passed || source.Manifest != m.Digest() {
			fatal(fmt.Errorf("original source verification is required before publishing"))
		}
		store, err := catalog.NewKodoStore(ak, sk, *bucket, *prefix)
		if err != nil {
			fatal(err)
		}
		fatal(catalog.New(store).Seed(ctx, m.Nodes()))
		fmt.Printf("published complete library: %d files, %d directories, %d bytes\n", len(m.Files), len(m.Directories), m.Bytes)
	default:
		fatal(fmt.Errorf("unknown migration mode"))
	}
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
