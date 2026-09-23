// Package branding is the workspace's title and icon (D76): what an admin
// sets from the admin tile to replace the word "workspace" in the shell
// header and the browser tab, and xbin's mark as the favicon and logo — on
// the workspace page and on the sign-in / invite pages. It lives in a small
// xbind-owned JSON doc, data/branding.json, so it is readable while the vault
// is sealed (the sign-in page renders before anything is unsealed). The icon
// is stored as a size-capped data URI: it only ever lands in <img> and
// <link rel=icon>, never at a navigable URL, so an SVG's scripts are inert.
package branding

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

const (
	MaxTitle = 64        // runes
	MaxIcon  = 256 << 10 // decoded bytes
)

// Brand is the workspace's branding. The zero value is xbin's own.
type Brand struct {
	Title string `json:"title"`
	Icon  string `json:"icon"` // data:image/<type>;base64,… or ""
}

// Patch is a partial update: each present key is a whole-value replace
// ("" clears); an absent key leaves that setting alone.
type Patch struct {
	Title *string `json:"title"`
	Icon  *string `json:"icon"`
}

// Store persists the brand at one path.
type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store { return &Store{path: path} }

// Load reads the brand; a missing file is the zero brand.
func (s *Store) Load() (Brand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (Brand, error) {
	var b Brand
	bts, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(bts, &b); err != nil {
		return Brand{}, fmt.Errorf("%s: %w", s.path, err)
	}
	return b, nil
}

// Apply validates and applies a patch, persists, and returns the result.
func (s *Store) Apply(p Patch) (Brand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.loadLocked()
	if err != nil {
		return b, err
	}
	if p.Title != nil {
		t, err := ValidTitle(*p.Title)
		if err != nil {
			return b, err
		}
		b.Title = t
	}
	if p.Icon != nil {
		ic, err := ValidIcon(*p.Icon)
		if err != nil {
			return b, err
		}
		b.Icon = ic
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return b, err
	}
	bts, _ := json.MarshalIndent(b, "", "  ")
	if err := fsutil.WriteFileAtomic(s.path, bts, 0o644); err != nil {
		return b, err
	}
	return b, nil
}

// ValidTitle trims and bounds a title: at most MaxTitle runes, no control
// characters. "" clears.
func ValidTitle(t string) (string, error) {
	t = strings.TrimSpace(t)
	if n := len([]rune(t)); n > MaxTitle {
		return "", fmt.Errorf("the title is %d characters; the limit is %d", n, MaxTitle)
	}
	for _, r := range t {
		if unicode.IsControl(r) {
			return "", errors.New("the title must not contain control characters")
		}
	}
	return t, nil
}

// iconTypes are the media types an icon may be. A raster one must also
// sniff as itself (a "PNG" that isn't one is refused); an SVG must hold one.
var iconTypes = map[string]bool{"image/svg+xml": true, "image/png": true, "image/jpeg": true, "image/webp": true, "image/x-icon": true}

// ValidIcon checks a data-URI icon: an allowed media type, base64, at most
// MaxIcon decoded bytes, and bytes that are what the type says. "" clears.
func ValidIcon(u string) (string, error) {
	u = strings.TrimSpace(u)
	if u == "" {
		return "", nil
	}
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return "", errors.New("the icon must be a data: URI")
	}
	meta, b64, ok := strings.Cut(rest, ",")
	if !ok {
		return "", errors.New("the icon must be a data: URI")
	}
	typ, ok := strings.CutSuffix(meta, ";base64")
	if !ok || !iconTypes[typ] {
		return "", errors.New("the icon must be base64 image/svg+xml, image/png, image/jpeg, image/webp or image/x-icon")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", errors.New("the icon is not valid base64")
	}
	if len(raw) == 0 {
		return "", errors.New("the icon is empty")
	}
	if len(raw) > MaxIcon {
		return "", fmt.Errorf("the icon is %d KiB; the limit is %d KiB", len(raw)>>10, MaxIcon>>10)
	}
	if typ == "image/svg+xml" {
		if !strings.Contains(strings.ToLower(string(raw)), "<svg") {
			return "", errors.New("the icon says image/svg+xml but holds no <svg>")
		}
	} else if got := http.DetectContentType(raw); got != typ {
		return "", fmt.Errorf("the icon says %s but its bytes look like %s", typ, got)
	}
	return "data:" + typ + ";base64," + b64, nil
}
