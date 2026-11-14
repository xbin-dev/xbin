//go:build !sealedfixture

package sealed

// table is the compiled-in table. Empty: the facility ships no component
// yet (D227); the workspace agent registers xbin/wsagent here.
func table() []Spec { return nil }

// fixtureTable says this build's table is the integration fixture's
// (table_fixture.go): never, in a build without that tag.
const fixtureTable = false
