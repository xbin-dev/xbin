//go:build !xbin_iroh || !linux || !(amd64 || arm64)

package sidecar

// embedded is empty: this xbind was built without the sidecar (`make build`
// embeds it with -tags xbin_iroh); XBIN_IROH can still name one.
var embedded []byte
