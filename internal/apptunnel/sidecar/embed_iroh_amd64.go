//go:build xbin_iroh && linux && amd64

package sidecar

import _ "embed"

// embedded is the gzipped xbin-iroh `make build` puts here (gitignored).
//
//go:embed embedded/xbin-iroh.linux-amd64.gz
var embedded []byte
