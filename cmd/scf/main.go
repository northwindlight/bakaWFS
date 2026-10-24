package main

import (
	"bakaWFS/internal/scf"
	"io"
	"log/slog"
	"os"

	"github.com/tencentyun/scf-go-lib/cloudfunction"
)

func main() {
	var output io.Writer = io.Discard
	if os.Getenv("BAKAWFS_LOG_ENABLED") == "true" {
		output = os.Stderr
	}
	logger := slog.New(slog.NewJSONHandler(output, nil))
	api := scf.NewAdapter(scf.NewBootstrap(os.Getenv, logger))
	cloudfunction.Start(api.Handle)
}
