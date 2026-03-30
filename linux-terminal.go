//go:build linux

package main

import (
	"io"
	"os"
)

func setupOutput() io.Writer {
	return os.Stdout
}
