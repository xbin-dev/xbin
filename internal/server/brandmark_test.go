package server

import (
	"encoding/base64"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/branding"
)

// covers D183 — xbind's pages draw xbin's mark and wordmark from the
// masters: the b's, the exponent's and the word's paths are
// /vendor/logo.svg's (lockup A, which TestProductMarksAreTheMasters holds
// to the master), the word sits where lockup A sets it, inside the drawing,
// and the default favicon is /vendor/favicon.svg.
func TestBrandMarkIsTheMasters(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	logo := read("logo.svg")
	ds := regexp.MustCompile(` d="([^"]+)"`).FindAllStringSubmatch(logo, -1)
	want := []string{markBPath, markXPath, wordPath}
	if len(ds) != len(want) {
		t.Fatalf("logo.svg has %d paths, want the b, the exponent and the word", len(ds))
	}
	for i, d := range ds {
		if d[1] != want[i] {
			t.Errorf("path %d is not logo.svg's:\n got %.80s…\nwant %.80s…", i, want[i], d[1])
		}
		if !strings.Contains(brandLockup, `d="`+want[i]+`"`) {
			t.Errorf("the lockup doesn't draw path %d", i)
		}
	}
	if !strings.Contains(brandWordmark, `d="`+wordPath+`"`) {
		t.Error("the wordmark doesn't draw the word")
	}

	// lockup A places the tile (1024 units) and the word in its own units;
	// in the tile's, the word's origin and scale are the lockup's
	num := func(s string) float64 {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	tile := regexp.MustCompile(`<g transform="translate\(([\d.]+) ([\d.]+)\) scale\(([\d.]+)\)">`).FindStringSubmatch(logo)
	word := regexp.MustCompile(`class="w"[^>]*transform="translate\(([\d.]+) ([\d.]+)\) scale\(([\d.]+)\)"`).FindStringSubmatch(logo)
	if tile == nil || word == nil {
		t.Fatal("logo.svg: no tile or word transform")
	}
	ts := num(tile[3])
	x, y, s := (num(word[1])-num(tile[1]))/ts, (num(word[2])-num(tile[2]))/ts, num(word[3])/ts
	got := regexp.MustCompile(`class="m-w" fill="currentColor" transform="translate\(([\d.]+) ([\d.]+)\) scale\(([\d.]+)\)"`).FindStringSubmatch(brandLockup)
	if got == nil {
		t.Fatal("the lockup's word has no transform")
	}
	for i, w := range []float64{x, y, s} {
		if g := num(got[i+1]); math.Abs(g-w) > 1e-3*math.Max(1, w) {
			t.Errorf("the word's transform, value %d: %v, lockup A's %v", i, g, w)
		}
	}
	vb := regexp.MustCompile(`<svg class="lockup" viewBox="0 0 ([\d.]+) 1024"`).FindStringSubmatch(brandLockup)
	if vb == nil || x+1952.2*s > num(vb[1]) || num(vb[1]) > x+1952.2*s+2 {
		t.Errorf("the lockup's viewBox should end at the word's last edge (%.1f): %v", x+1952.2*s, vb)
	}

	if fav := read("favicon.svg"); defaultIconURI != "data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString([]byte(fav)) {
		t.Error("defaultIconURI is not web/favicon.svg: re-encode it (base64 -w0 web/favicon.svg)")
	}
}

// covers D183, D76 — the logo block: xbin's lockup with no branding; a
// workspace's title (after its icon, where the page loads images) replaces
// it; an icon without a title stands beside xbin's word, never its tile.
func TestBrandLogo(t *testing.T) {
	const png = "data:image/png;base64,AAAA"
	img := `<img class="mark" src="` + png + `" alt="">`
	for _, c := range []struct {
		b        branding.Brand
		withIcon bool
		want     string
	}{
		{branding.Brand{}, true, brandLockup},
		{branding.Brand{}, false, brandLockup},
		{branding.Brand{Title: "Acme"}, true, `<span class="name">Acme</span>`},
		{branding.Brand{Title: "Acme", Icon: png}, true, img + `<span class="name">Acme</span>`},
		{branding.Brand{Title: "Acme", Icon: png}, false, `<span class="name">Acme</span>`},
		{branding.Brand{Icon: png}, true, img + brandWordmark},
		{branding.Brand{Icon: png}, false, brandWordmark},
	} {
		if got := brandLogo(c.b, c.withIcon); got != c.want {
			t.Errorf("brandLogo(%+v, %v) = %.120s, want %.120s", c.b, c.withIcon, got, c.want)
		}
	}
}
