//go:build sealedfixture

package sealed

// table_fixture.go — the table of an xbind built with -tags sealedfixture:
// two fixture components embedded as a release embeds its own, so the
// integration suite (test/isolated's sealed tests) drives the facility
// through a real binary: the release build (`xbind sealed-build`), a
// production boot under --isolate, people's partitions. No release is
// built with this tag (`make build` names its tags; TestTableIsEmpty
// fails a default build that has a row the facility wasn't shipped with).

import (
	"embed"
	"io/fs"
)

//go:embed all:testdata/fixture/app
var fixtureFS embed.FS

func table() []Spec {
	src, err := fs.Sub(fixtureFS, "testdata/fixture/app")
	if err != nil {
		panic(err)
	}
	return []Spec{
		{Name: "probe", Title: "Fixture probe", Source: src, Authored: "apps/fixture", Slots: []string{"llm"}},
		{Name: "people", Title: "Fixture people", Source: src, Authored: "apps/fixture", Partition: []string{"user", "global"}},
	}
}
