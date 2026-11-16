module mcpserver

go 1.24

require github.com/xbin-dev/xbin/sdk v0.0.0

// Resolved by the workspace go.work under xbind (bx doctor checks this);
// this replace keeps the example buildable straight from the xbin repo too.
// Nothing else: the MCP wire is the standard library's JSON and HTTP, so the
// backend builds in its sandbox with no network.
replace github.com/xbin-dev/xbin/sdk => ../../sdk
