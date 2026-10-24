package main

import (
	"bakaWFS/internal/cloudfs"
	"context"
	"flag"
	"fmt"
	"os"
)

func main() {
	prefix := flag.String("validation-prefix", "", "owned validation namespace")
	production := flag.String("production-prefix", "catalog/v1/", "production reference authority")
	bucket := flag.String("bucket", "bakawfs-library-northwind", "owned private bucket")
	keys := flag.String("keys", "/etc/qiniu/keys.env", "private credentials")
	yes := flag.Bool("yes", false, "authorize owned test cleanup")
	flag.Parse()
	if !*yes {
		fmt.Fprintln(os.Stderr, "cleanup requires --yes")
		os.Exit(1)
	}
	ak, sk, e := cloudfs.ReadKeys(*keys)
	if e != nil {
		fail(e)
	}
	k, e := cloudfs.NewKodo(ak, sk, *bucket)
	if e != nil {
		fail(e)
	}
	n, e := k.CleanupValidation(context.Background(), *prefix, *production)
	if e != nil {
		fail(e)
	}
	fmt.Printf("validation objects and snapshots removed: %d\n", n)
}
func fail(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }
