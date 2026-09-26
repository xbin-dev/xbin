module ptytest

go 1.24

require github.com/xbin-dev/xbin/sdk v0.0.0

// Resolved by the workspace go.work under xbind; this replace keeps the tile
// buildable straight from the xbin repo too.
replace github.com/xbin-dev/xbin/sdk => ../../../../sdk
