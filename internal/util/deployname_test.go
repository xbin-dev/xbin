package util

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// covers D127c — deployment names follow the glossary grammar
// ^[a-z][a-z0-9-]{0,23}$, so a tile ref "<tile>+<name>" splits one way only,
// and main is a name like any other.
func TestDeploymentNameOK(t *testing.T) {
	for _, s := range []string{
		"main", "dev", "a", "d-1", "feature-x", "x0", "a-",
		strings.Repeat("a", 24), "a" + strings.Repeat("0", 23),
	} {
		if !DeploymentNameOK(s) {
			t.Errorf("DeploymentNameOK(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"", "Main", "DEV", "1dev", "-dev", "dev+x", "dev/x", "dev x", "dev.x",
		"dev_x", "é", "dév", "dev\x00", strings.Repeat("a", 25),
	} {
		if DeploymentNameOK(s) {
			t.Errorf("DeploymentNameOK(%q) = true, want false", s)
		}
	}
}

// covers D127c T11 — TileKey is 11-contract §10's key of the new stores:
// lowercase-hex(SHA-256("xbin-tile-key-v1" ‖ 0x00 ‖ path)[:16]). The
// expected values are hand-maintained, computed once with sha256sum (printf
// 'xbin-tile-key-v1\0%s' "$path" | sha256sum), never by the code under test;
// changing one orphans every store keyed by it.
func TestTileKey(t *testing.T) {
	for _, tc := range []struct{ path, key string }{
		{"apps/crm", "5d01689965d746c344a7f12d46b8a671"},
		{"apps/x", "628e34e47e8e631c2fde937e0fbf1205"},
		{"apps/crm+dev", "69462d2f6fd611cfbb01be19319cd68d"},
		{"tiles/a-very-long-tile-name-past-24", "fdfea614b845b691bf469308291e9e95"},
		{"", "59f5e7835e696fe4509340d90833a248"},
	} {
		if got := TileKey(tc.path); got != tc.key {
			t.Errorf("TileKey(%q) = %q, want %q", tc.path, got, tc.key)
		}
	}
	// Paths whose CompKey readable parts coincide ("/" and "~" map alike,
	// long paths are cut at 24) keep distinct TileKeys.
	for _, pair := range [][2]string{
		{"apps/x", "apps~x"},
		{"tiles/a-very-long-tile-name-one", "tiles/a-very-long-tile-name-two"},
	} {
		if TileKey(pair[0]) == TileKey(pair[1]) {
			t.Errorf("TileKey(%s) == TileKey(%s)", pair[0], pair[1])
		}
	}
}

// covers D119c — the "unknown deployment" error carries 11-contract §1.14's
// text and matches ErrNoDeployment, however it is wrapped.
func TestNoDeployment(t *testing.T) {
	err := NoDeployment("apps/crm", "dev")
	if got, want := err.Error(), `apps/crm has no deployment "dev"`; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	if !errors.Is(err, ErrNoDeployment) || !errors.Is(fmt.Errorf("proxy: %w", err), ErrNoDeployment) {
		t.Error("NoDeployment doesn't match ErrNoDeployment")
	}
	if errors.Is(errors.New(`apps/crm has no deployment "dev"`), ErrNoDeployment) {
		t.Error("a look-alike error matches ErrNoDeployment")
	}
}

// covers D127j — no new tile name holds '+', in any segment; the refusal names
// the path.
func TestPlusNameRefusal(t *testing.T) {
	for _, p := range []string{"apps/x", "notes", "apps/c-d", "apps/x:y"} {
		if msg := PlusNameRefusal(p); msg != "" {
			t.Errorf("PlusNameRefusal(%q) = %q, want none", p, msg)
		}
	}
	for _, p := range []string{"apps/x+dev", "a+b", "apps+x/y", "/apps/c++/", "x+"} {
		if msg := PlusNameRefusal(p); !strings.Contains(msg, "can't create "+strings.Trim(p, "/")+": '+' isn't allowed in tile names") {
			t.Errorf("PlusNameRefusal(%q) = %q", p, msg)
		}
	}
}

// covers D127j — a query parameter naming a tile is its path: a '+' that names
// no tile, or the space an unescaped '+' decoded to, reads as a qualified
// ref; a tile whose own name holds '+' passes.
func TestQueryTileQualified(t *testing.T) {
	tiles := map[string]bool{"apps/x": true, "legacy/a+b": true, "my app": true}
	isTile := func(s string) bool { return tiles[s] }
	for v, want := range map[string]bool{
		"apps/x": false, "legacy/a+b": false, "my app": false, "apps/y": false,
		"apps/x+dev": true, "apps/y+dev": true, "apps/c++": true, "legacy/a+b+dev": true,
		"apps/x dev": true, "apps/x Dev": false, "apps/y dev": false, "some thing": false,
	} {
		if got := QueryTileQualified(v, isTile); got != want {
			t.Errorf("QueryTileQualified(%q) = %v, want %v", v, got, want)
		}
	}
}
