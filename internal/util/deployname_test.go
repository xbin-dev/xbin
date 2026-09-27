package util

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// covers P6 — deployment names follow the glossary grammar
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

// covers P6 T11 — TileKey is 11-contract §10's key of the new stores:
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

// covers P5 — the "unknown deployment" error carries 11-contract §1.14's
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
