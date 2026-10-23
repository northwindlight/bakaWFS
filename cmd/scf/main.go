package main

import (
	"bakaWFS/internal/scf"
	"log/slog"
	"os"

	"github.com/tencentyun/scf-go-lib/cloudfunction"
)

func main() {
	api := scf.NewAdapter(scf.NewBootstrap(os.Getenv, slog.Default()))
	cloudfunction.Start(api.Handle)
}
