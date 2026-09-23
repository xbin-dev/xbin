package branding

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

// a 1×1 transparent PNG
const png1x1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func TestStoreApplyAndLoad(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "data", "branding.json"))
	b, err := s.Load()
	if err != nil || b.Title != "" || b.Icon != "" {
		t.Fatalf("a missing file is the zero brand: %+v %v", b, err)
	}
	title := "  Acme Ops "
	if b, err = s.Apply(Patch{Title: &title}); err != nil || b.Title != "Acme Ops" {
		t.Fatalf("title (trimmed): %+v %v", b, err)
	}
	icon := "data:image/png;base64," + png1x1
	if b, err = s.Apply(Patch{Icon: &icon}); err != nil || b.Icon != icon || b.Title != "Acme Ops" {
		t.Fatalf("an absent key leaves the other setting alone: %+v %v", b, err)
	}
	if b, err = New(s.path).Load(); err != nil || b.Title != "Acme Ops" || b.Icon != icon {
		t.Fatalf("persisted: %+v %v", b, err)
	}
	bad := "not an icon"
	if _, err := s.Apply(Patch{Icon: &bad}); err == nil {
		t.Fatal("a bad patch is refused")
	}
	if b, _ = s.Load(); b.Icon != icon {
		t.Fatal("a refused patch changes nothing")
	}
	empty := ""
	if b, err = s.Apply(Patch{Title: &empty, Icon: &empty}); err != nil || b.Title != "" || b.Icon != "" {
		t.Fatalf("empty clears: %+v %v", b, err)
	}
}

func TestValidTitle(t *testing.T) {
	if _, err := ValidTitle(strings.Repeat("x", MaxTitle+1)); err == nil {
		t.Fatal("over the length cap")
	}
	if _, err := ValidTitle("a\x00b"); err == nil {
		t.Fatal("control characters")
	}
	if got, err := ValidTitle(strings.Repeat("é", MaxTitle)); err != nil || len([]rune(got)) != MaxTitle {
		t.Fatal("the cap counts runes, not bytes")
	}
}

func TestValidIcon(t *testing.T) {
	ok := "data:image/png;base64," + png1x1
	if got, err := ValidIcon(ok); err != nil || got != ok {
		t.Fatalf("a real PNG: %v", err)
	}
	svg := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	if _, err := ValidIcon(svg); err != nil {
		t.Fatalf("svg: %v", err)
	}
	bad := map[string]string{
		"not a data uri":      "https://example.com/i.png",
		"type not allowed":    "data:image/gif;base64," + png1x1,
		"not base64":          "data:image/png;base64,***",
		"bytes are not a png": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>")),
		"svg without <svg>":   "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<html/>")),
		"too big":             "data:image/png;base64," + base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxIcon)...)),
	}
	for name, u := range bad {
		if _, err := ValidIcon(u); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got, err := ValidIcon("  "); err != nil || got != "" {
		t.Fatal("blank clears")
	}
}
