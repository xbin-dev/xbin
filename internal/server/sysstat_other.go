//go:build !linux

package server

// hostStats is Linux-only; other platforms report nothing (the shell's
// status footer just omits the gauges).
func hostStats(string) map[string]any { return map[string]any{} }
