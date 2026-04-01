//go:build linux || darwin || freebsd

package main

import (
	"io"
	"os"
)

func setupOutput() io.Writer {
	return os.Stdout
}
