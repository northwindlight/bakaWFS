package main

import (
	"bakaWFS/internal/scf"

	"github.com/tencentyun/scf-go-lib/cloudfunction"
)

func main() {
	probe := scf.NewAdapter(scf.NewProbe())
	cloudfunction.Start(probe.Handle)
}
