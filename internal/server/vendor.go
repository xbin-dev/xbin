package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// The theme's fonts and their licences (web/vendor/fonts/, D184): Go's
// builtin MIME table has neither extension, so without these the type of a
// font would come from the host's /etc/mime.types or shared-mime-info —
// application/octet-stream on a minimal server or container image.
func init() {
	for ext, typ := range map[string]string{".woff2": "font/woff2", ".txt": "text/plain; charset=utf-8"} {
		if err := mime.AddExtensionType(ext, typ); err != nil {
			panic(err)
		}
	}
}

// fontImmutable is how long a font asked for with its own version may be
// kept without asking again: its URL changes when its bytes do.
const fontImmutable = "public, max-age=31536000, immutable"

// handleVendor serves core elements (web/*) and vendored deps (web/vendor/*)
// under one /vendor/ prefix, so import maps and element imports have a single
// stable root.
//
// Every answer carries a strong ETag, its content's SHA-256, under
// Cache-Control: no-cache: a browser asks again on every load and an
// unchanged file answers 304, so its bytes travel once per change (they
// change on an upgrade, or as you edit under `make dev`). A font asked for
// with its version — ?v=, the first 8 hex digits of that hash, which
// theme.css writes in each url() — is immutable: a reload draws the theme's
// faces from the cache without a round trip, so text doesn't swap faces on
// every load (D184). Any other ?v= is answered like a request without one.
func (s *Server) handleVendor(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/vendor/")
	if name == "" || strings.Contains(name, "..") || topLevelPage(name) {
		http.NotFound(w, r)
		return
	}
	for _, p := range []string{name, "vendor/" + name} {
		b, err := fs.ReadFile(s.WebFS, p)
		if err != nil {
			continue
		}
		ct := mime.TypeByExtension(filepath.Ext(p))
		if ct == "" {
			ct = "application/octet-stream"
		}
		sum := s.vendorSum(p, b)
		h := w.Header()
		h.Set("Content-Type", ct)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("ETag", `"`+sum[:32]+`"`)
		// A sandboxed frame's request (Origin: null) is answered with CORS
		// headers (nullOriginCORS), the page's own without: every answer
		// says so, or a browser would hand the frame the page's cached copy
		// — an immutable font with no Access-Control-Allow-Origin.
		if h.Get("Vary") == "" {
			h.Set("Vary", "Origin")
		}
		if v := r.URL.Query().Get("v"); v != "" && strings.HasPrefix(p, "vendor/fonts/") && v == FontVersion(sum) {
			h.Set("Cache-Control", fontImmutable)
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
		return
	}
	http.NotFound(w, r)
}

// FontVersion is the ?v= theme.css writes after a font's URL: the first 8
// hex digits of its SHA-256 (hex-encoded sum).
func FontVersion(sum string) string { return sum[:8] }

// vendorSum is the hex SHA-256 of the file at p (whose bytes are b), kept
// per path while the file's size and modification time stand: the embedded
// tree never changes under a running xbind, and `make dev` serves web/
// from disk, where an edit moves both.
func (s *Server) vendorSum(p string, b []byte) string {
	var mod time.Time
	if st, err := fs.Stat(s.WebFS, p); err == nil {
		mod = st.ModTime()
	}
	if v, ok := s.vendorSums.Load(p); ok {
		if e := v.(vendorSumEntry); e.size == len(b) && e.mod.Equal(mod) {
			return e.sum
		}
	}
	h := sha256.Sum256(b)
	sum := hex.EncodeToString(h[:])
	s.vendorSums.Store(p, vendorSumEntry{size: len(b), mod: mod, sum: sum})
	return sum
}

type vendorSumEntry struct {
	size int
	mod  time.Time
	sum  string
}
