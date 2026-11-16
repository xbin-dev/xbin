//go:build !linux

package broker

// dirIdentity: xbind serves tiles on Linux; elsewhere a directory has no
// identity this code reads, and a waiting line is held to its tiles' paths
// and owners alone.
func dirIdentity(string) string { return "" }
