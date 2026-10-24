package main

import (
	"bakaWFS/internal/cloudfs"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	dir := flag.String("dir", "dist/cloud-ui", "hashed UI directory")
	keys := flag.String("keys", "/etc/qiniu/keys.env", "private credential source")
	bucket := flag.String("bucket", "bakacloud", "static website bucket")
	domain := flag.String("domain", "https://cloud.northwind.site", "existing CDN domain")
	backup := flag.String("backup", "dist/cloud-ui-previous-index.html", "local rollback entry")
	cacheOnly := flag.Bool("cache-only", false, "configure entry cache without publishing UI")
	inspect := flag.Bool("inspect-cdn", false, "read existing CDN cache settings")
	yes := flag.Bool("yes", false, "authorize public cutover")
	flag.Parse()
	if !*yes && !*inspect {
		fmt.Fprintln(os.Stderr, "publishing requires --yes")
		os.Exit(1)
	}
	ak, sk, e := cloudfs.ReadKeys(*keys)
	if e == nil {
		var k *cloudfs.Kodo
		k, e = cloudfs.NewKodo(ak, sk, *bucket)
		if e == nil {
			if *inspect {
				var result map[string]any
				result, e = k.InspectStaticCDN(context.Background(), *domain)
				if e == nil {
					json.NewEncoder(os.Stdout).Encode(result)
				}
			} else {
				e = k.ConfigureStaticCDN(context.Background(), *domain, *backup+".cdn.json")
				if e == nil && *cacheOnly {
					e = k.SetEntryCache(context.Background())
				}
				if e == nil && !*cacheOnly {
					e = k.PublishUI(context.Background(), *dir, *domain, *backup)
				}
			}
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
