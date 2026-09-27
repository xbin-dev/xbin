package main

import xbin "github.com/xbin-dev/xbin/sdk"

// The `xbin` backend is the SDK itself: *xbin.Sandboxes is a Fleet and
// *xbin.Sandbox a Box, with no translation (backend.go). A change to either
// side that breaks this fails to compile here.
var (
	_ Fleet   = (*xbin.Sandboxes)(nil)
	_ Box     = (*xbin.Sandbox)(nil)
	_ Backend = xbinBackend{}
)
