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
	domain := flag.String("domain", "https://manga-data.northwind.site", "private data CDN")
	bucket := flag.String("bucket", "bakawfs-library-northwind", "private source bucket")
	keys := flag.String("keys", "/etc/qiniu/keys.env", "private credentials")
	cert := flag.String("certificate", "", "existing owned wildcard certificate ID")
	reg := flag.String("registration", "", "existing ICP registration")
	testKey := flag.String("test-key", "", "existing immutable object for origin validation")
	create := flag.Bool("create", false, "create only when absent")
	auth := flag.Bool("auth", false, "enable built-in private Kodo authentication")
	headers := flag.Bool("headers", false, "set browser CORS response headers")
	yes := flag.Bool("yes", false, "authorize CDN changes")
	flag.Parse()
	if (*create || *headers || *auth) && !*yes {
		fmt.Fprintln(os.Stderr, "CDN mutations require --yes")
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
	data, e := k.DataCDN(context.Background(), *domain, *cert, *reg, *testKey, *create)
	if e != nil {
		fail(e)
	}
	if *auth {
		if e = k.ConfigureDataAuth(context.Background(), *domain); e != nil {
			fail(e)
		}
	}
	if *headers {
		if e = k.ConfigureDataHeaders(context.Background(), *domain); e != nil {
			fail(e)
		}
	}
	result := map[string]any{}
	for _, key := range []string{"name", "cname", "operatingState", "operatingStateDesc", "protocol", "qiniuPrivate", "https"} {
		result[key] = data[key]
	}
	if b, ok := data["bsauth"].(map[string]any); ok {
		result["private_auth"] = map[string]any{"enable": b["enable"], "isQiniuPrivate": b["isQiniuPrivate"]}
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
func fail(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }
