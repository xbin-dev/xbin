//go:build !sealedfixture

package sealed

// table is the compiled-in table. Empty: the facility ships no component
// yet (D227); the workspace agent registers xbin/wsagent here.
func table() []Spec { return nil }
