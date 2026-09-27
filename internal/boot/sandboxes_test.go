package boot

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/sbx"
)

func TestSessionWhat(t *testing.T) {
	for _, c := range []struct {
		e    sbx.Entry
		want string
	}{
		{sbx.Entry{Kind: sbx.Terminal, User: "alice", Tile: "apps/x", Mode: sbx.VM}, "a VM terminal of alice on apps/x"},
		{sbx.Entry{Kind: sbx.Agent, Tile: "apps/x"}, "an agent session on apps/x"},
		{sbx.Entry{ID: "tile:apps~x-1a2b3c4d:build", Kind: sbx.Tile, Tile: "apps/x", Mode: sbx.Namespace}, `the tile sandbox "build" of apps/x`},
		{sbx.Entry{ID: "tile+pr-7:apps~x-1a2b3c4d:db", Kind: sbx.Tile, Tile: "apps/x", Mode: sbx.VM}, `the VM tile sandbox "db" of apps/x`},
	} {
		if got := sessionWhat(c.e); got != c.want {
			t.Errorf("sessionWhat(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}
