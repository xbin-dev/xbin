package main

import xbin "github.com/xbin-dev/xbin/sdk"

// The `xbin` backend is the SDK itself: *xbin.Sandboxes is a Fleet and
// *xbin.Sandbox a Box (backend.go), and xbinBox only answers the ids the
// runtime's grammar can't hold. A change to either side that breaks this
// fails to compile here.
var (
	_ Fleet   = (*xbin.Sandboxes)(nil)
	_ Box     = (*xbin.Sandbox)(nil)
	_ Box     = xbinBox{}
	_ Backend = xbinBackend{}
)
