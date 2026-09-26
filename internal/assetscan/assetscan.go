// Package assetscan finds the tile frontend references that strict tile
// asset gating (plans/tile-asset-auth.md, --tile-assets=tokens|origins)
// cannot credential, and rewrites the fixable ones — the detection half of
// the rollout (the tile report at GET /api/xbin/tile-assets, `bx doctor`)
// and `bx fix assets <tile>`.
//
// Under tokens mode only RELATIVE URLs carry the asset token (they resolve
// against the injected <base href="/c/~<tok>/…">); an absolute /c/ URL in an
// HTML attribute, a stylesheet or a document's own import map carries no
// credential and fails. Rewriting it relative is exact: the relative URL
// resolves to the same /c/ path in every mode (legacy, tokens — under the
// token prefix — and origins), so the codemod never changes what a tile
// loads, only whether it can. Absolute module imports of the tile's own
// files keep working (the injected import map remaps /c/<tile>/) and are
// reported as fixable information; other JavaScript strings naming /c/ are
// reported for a human, since only the author knows how they are used.
//
// The scanner reads through fsutil.OpenBeneath (no symlink leaves the tile,
// a FIFO or device is never opened for reading) and never follows a
// symlinked directory: it runs in xbind on files tile sandboxes write.
package assetscan

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/jsonc"
)

// Finding kinds.
const (
	KindHTMLAttr      = "html-attr"      // src/href/srcset/… attribute (HTML, SVG)
	KindImportMap     = "importmap"      // a /c/ value in the document's own import map
	KindCSSURL        = "css-url"        // url(…) / @import in CSS, <style>, style=""
	KindJSImport      = "js-import"      // import/export … from, import(…)
	KindJSString      = "js-string"      // any other JS string naming /c/…
	KindInjectFalse   = "inject-false"   // manifest inject:false — no injection at all
	KindBaseTag       = "base-tag"       // the document declares its own <base>
	KindSymlinkEscape = "symlink-escape" // a symlink leaving the tile
)

// Breaks values: which modes a finding fails under.
const (
	BreaksTokens = "tokens" // fails under --tile-assets=tokens (works under origins)
	BreaksStrict = "strict" // fails under both strict modes
)

// Finding is one reference.
type Finding struct {
	File   string `json:"file"` // tile-relative, slash-separated
	Line   int    `json:"line,omitempty"`
	Col    int    `json:"col,omitempty"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref,omitempty"`    // the URL as written
	Target string `json:"target,omitempty"` // the referenced component ("chrome" for root/shell)
	Breaks string `json:"breaks,omitempty"` // "" = works in every mode
	Fix    string `json:"fix,omitempty"`    // what bx fix assets writes instead ("" = manual)
	Note   string `json:"note,omitempty"`

	start, end int // byte span of Ref in the file (codemod)
}

// Report is one tile's scan.
type Report struct {
	Component   string    `json:"component"`
	InjectFalse bool      `json:"injectFalse,omitempty"`
	Files       int       `json:"files"`
	Findings    []Finding `json:"findings"`
	Truncated   bool      `json:"truncated,omitempty"`
}

// Breaking counts the findings that fail under mode ("tokens" or "origins").
func (r Report) Breaking(mode string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Breaks == BreaksStrict || (f.Breaks == BreaksTokens && mode == "tokens") {
			n++
		}
	}
	return n
}

// Options bound a scan.
type Options struct {
	// Owner maps a /c/-relative path ("apps/x/lib.js") to the component
	// owning it. nil: nearest directory holding xbin.json under Root, else
	// the first segment.
	Owner func(rel string) string
	// Root is the workspace root (for the default Owner); may be "".
	Root string
	// Limits (0 = defaults: 4000 files, 2 MiB per file, 64 MiB in all,
	// 500 findings).
	MaxFiles, MaxFileBytes, MaxTotalBytes, MaxFindings int
}

func (o *Options) defaults() {
	if o.MaxFiles == 0 {
		o.MaxFiles = 4000
	}
	if o.MaxFileBytes == 0 {
		o.MaxFileBytes = 2 << 20
	}
	if o.MaxTotalBytes == 0 {
		o.MaxTotalBytes = 64 << 20
	}
	if o.MaxFindings == 0 {
		o.MaxFindings = 500
	}
	if o.Owner == nil {
		root := o.Root
		o.Owner = func(rel string) string { return localOwner(root, rel) }
	}
}

// localOwner: the longest prefix of rel that is a directory with an
// xbin.json under root (the registry's rule, read from disk).
func localOwner(root, rel string) string {
	segs := strings.Split(strings.Trim(rel, "/"), "/")
	if root != "" {
		for i := len(segs); i > 0; i-- {
			p := strings.Join(segs[:i], "/")
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p), "xbin.json")); err == nil {
				return p
			}
		}
	}
	return segs[0]
}

func isChrome(comp string) bool { return comp == "root" || comp == "shell" }

var scanExt = map[string]string{".html": "html", ".htm": "html", ".svg": "html", ".css": "css", ".js": "js", ".mjs": "js"}

var skipDirs = map[string]bool{".git": true, ".xbin": true, "node_modules": true, "deps": true, "__pycache__": true}

// Scan reports tile's references. dir is the tile's directory, tile its
// component path.
func Scan(dir, tile string, opt Options) (Report, error) {
	opt.defaults()
	rep := Report{Component: tile, Findings: []Finding{}}
	total := 0
	if b, err := readBeneath(dir, "xbin.json", 1<<20); err == nil {
		var m struct {
			Inject *bool `json:"inject"`
		}
		if jsonc.Unmarshal(b, &m) == nil && m.Inject != nil && !*m.Inject {
			rep.InjectFalse = true
			rep.Findings = append(rep.Findings, Finding{File: "xbin.json", Kind: KindInjectFalse, Breaks: BreaksTokens,
				Note: `"inject": false serves documents byte-exact: no <base>, no asset token — under tokens mode none of the tile's files load; remove it, or run the workspace with --tile-assets=origins`})
		}
	}
	err := walk(dir, func(rel string, d fs.DirEntry) error {
		if rep.Files >= opt.MaxFiles || len(rep.Findings) >= opt.MaxFindings || total >= opt.MaxTotalBytes {
			rep.Truncated = true
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			f, err := fsutil.OpenBeneath(dir, rel)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fsutil.ErrNotRegular) {
					rep.Findings = append(rep.Findings, Finding{File: rel, Kind: KindSymlinkEscape, Breaks: BreaksStrict,
						Note: "a symlink leaving the tile — the strict modes answer 404 for it; copy the file into the tile"})
				}
				return nil
			}
			fi, _ := f.Stat()
			f.Close()
			if fi == nil || !fi.Mode().IsRegular() {
				return nil
			}
		}
		kind := scanExt[strings.ToLower(path.Ext(rel))]
		if kind == "" {
			return nil
		}
		b, err := readBeneath(dir, rel, opt.MaxFileBytes)
		if err != nil {
			return nil // unreadable or too large: not ours to judge
		}
		rep.Files++
		total += len(b)
		fs := scanFile(b, kind, tile, rel, opt.Owner)
		rep.Findings = append(rep.Findings, fs...)
		return nil
	})
	if len(rep.Findings) > opt.MaxFindings {
		rep.Findings, rep.Truncated = rep.Findings[:opt.MaxFindings], true
	}
	return rep, err
}

// walk visits the tile's regular files and symlinks (never descending into a
// symlinked directory, a skipped directory or a nested component).
func walk(dir string, fn func(rel string, d fs.DirEntry) error) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p == dir {
				return nil
			}
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "xbin.json")); err == nil {
				return filepath.SkipDir // a nested component is its own tile
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if d.Type().IsRegular() || d.Type()&fs.ModeSymlink != 0 {
			return fn(rel, d)
		}
		return nil
	})
}

func readBeneath(dir, rel string, max int) ([]byte, error) {
	f, err := fsutil.OpenBeneath(dir, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > int64(max) {
		return nil, errors.New("too large")
	}
	return io.ReadAll(io.LimitReader(f, int64(max)+1))
}

// --- per-file scanning ---

var (
	attrRe      = regexp.MustCompile(`(?i)(?:^|[\s"'/])((?:xlink:)?(?:src|href|srcset|imagesrcset|poster|data|action|formaction|background))\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	scriptRe    = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	styleRe     = regexp.MustCompile(`(?is)<style\b[^>]*>(.*?)</style\s*>`)
	styleAttrRe = regexp.MustCompile(`(?i)[\s"']style\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	typeAttrRe  = regexp.MustCompile(`(?i)\btype\s*=\s*["']?([\w/+.-]+)`)
	baseRe      = regexp.MustCompile(`(?i)<base\b[^>]*\bhref\s*=`)
	cssURLRe    = regexp.MustCompile(`(?i)url\(\s*(['"]?)(/c/[^'")\s]*)(['"]?)\s*\)|@import\s+(['"])(/c/[^'"]*)['"]`)
	jsImportRe  = regexp.MustCompile(`\bfrom\s*(['"])(/c/[^'"\n]*)['"]|\bimport\s*\(\s*(['"` + "`" + `])(/c/[^'"` + "`" + `\n]*)['"` + "`" + `]|\bimport\s*(['"])(/c/[^'"\n]*)['"]`)
	jsStringRe  = regexp.MustCompile(`(['"` + "`" + `])(/c/[^'"` + "`" + `\s]*)['"` + "`" + `]`)
	jsonValueRe = regexp.MustCompile(`:\s*"(/c/[^"]*)"`)
)

type scanner struct {
	src    []byte
	tile   string
	rel    string
	dirURL string // the URL directory relative refs resolve against
	owner  func(string) string
	out    []Finding
}

func scanFile(b []byte, kind, tile, rel string, owner func(string) string) []Finding {
	sc := &scanner{src: b, tile: tile, rel: rel, owner: owner,
		dirURL: "/c/" + path.Dir(path.Join(tile, rel)) + "/"}
	switch kind {
	case "css":
		sc.css(0, len(b), true)
	case "js":
		sc.js(0, len(b), sc.dirURL)
	default:
		sc.html()
	}
	sort.Slice(sc.out, func(i, j int) bool { return sc.out[i].start < sc.out[j].start })
	return sc.out
}

func (sc *scanner) html() {
	fixable := !baseRe.Match(sc.src) // a document with its own <base>: relative ≠ doc-relative
	if !fixable {
		loc := baseRe.FindIndex(sc.src)
		sc.add(loc[0], loc[0], KindBaseTag, "", "", "the document sets its own <base>; the injected asset <base> stands for it when it is a /c/ URL — references below are not rewritten automatically")
	}
	var skip [][2]int
	for _, m := range scriptRe.FindAllSubmatchIndex(sc.src, -1) {
		skip = append(skip, [2]int{m[4], m[5]})
		typ := ""
		if t := typeAttrRe.FindSubmatch(sc.src[m[2]:m[3]]); t != nil {
			typ = strings.ToLower(string(t[1]))
		}
		switch typ {
		case "importmap":
			for _, v := range jsonValueRe.FindAllSubmatchIndex(sc.src[m[4]:m[5]], -1) {
				s, e := m[4]+v[2], m[4]+v[3]
				// Import-map addresses must be absolute or start with /, ./ or
				// ../ — a bare "ui/" nulls the entry — so import semantics.
				sc.ref(s, e, KindImportMap, fixable, true)
			}
		case "", "module", "text/javascript", "application/javascript":
			if fixable {
				sc.js(m[4], m[5], sc.dirURL)
			} else {
				sc.js(m[4], m[5], "")
			}
		}
	}
	for _, m := range styleRe.FindAllSubmatchIndex(sc.src, -1) {
		skip = append(skip, [2]int{m[2], m[3]})
		sc.css(m[2], m[3], fixable)
	}
	inSkip := func(i int) bool {
		for _, s := range skip {
			if i >= s[0] && i < s[1] {
				return true
			}
		}
		return false
	}
	for _, m := range styleAttrRe.FindAllSubmatchIndex(sc.src, -1) {
		if inSkip(m[0]) {
			continue
		}
		s, e := m[2], m[3]
		if s < 0 {
			s, e = m[4], m[5]
		}
		sc.css(s, e, fixable)
	}
	for _, m := range attrRe.FindAllSubmatchIndex(sc.src, -1) {
		if inSkip(m[0]) {
			continue
		}
		s, e := -1, -1
		for g := 2; g <= 4; g++ {
			if m[2*g] >= 0 {
				s, e = m[2*g], m[2*g+1]
				break
			}
		}
		if s < 0 {
			continue
		}
		name := strings.ToLower(string(sc.src[m[2]:m[3]]))
		if strings.HasSuffix(name, "srcset") {
			sc.srcset(s, e, fixable)
			continue
		}
		v := sc.src[s:e]
		lead := len(v) - len(bytes.TrimLeft(v, " \t\n"))
		v = bytes.TrimSpace(v)
		if bytes.HasPrefix(v, []byte("/c/")) {
			sc.ref(s+lead, s+lead+len(v), KindHTMLAttr, fixable, false)
		}
	}
}

func (sc *scanner) srcset(s, e int, fixable bool) {
	i := s
	for i < e {
		for i < e && (sc.src[i] == ' ' || sc.src[i] == ',' || sc.src[i] == '\n' || sc.src[i] == '\t') {
			i++
		}
		j := i
		for j < e && sc.src[j] != ' ' && sc.src[j] != '\t' && sc.src[j] != '\n' && sc.src[j] != ',' {
			j++
		}
		if bytes.HasPrefix(sc.src[i:j], []byte("/c/")) {
			sc.ref(i, j, KindHTMLAttr, fixable, false)
		}
		for j < e && sc.src[j] != ',' {
			j++
		}
		i = j
	}
}

func (sc *scanner) css(s, e int, fixable bool) {
	for _, m := range cssURLRe.FindAllSubmatchIndex(sc.src[s:e], -1) {
		if m[4] >= 0 {
			sc.ref(s+m[4], s+m[5], KindCSSURL, fixable, false)
		} else {
			sc.ref(s+m[10], s+m[11], KindCSSURL, fixable, false)
		}
	}
}

// js scans [s,e): import specifiers (fixable against base when base != "")
// and any other string naming /c/ (manual).
func (sc *scanner) js(s, e int, base string) {
	taken := map[int]bool{}
	for _, m := range jsImportRe.FindAllSubmatchIndex(sc.src[s:e], -1) {
		for g := 2; g <= 6; g += 2 {
			if m[2*g] >= 0 {
				a, b := s+m[2*g], s+m[2*g+1]
				taken[a] = true
				saved := sc.dirURL
				if base != "" {
					sc.dirURL = base
				}
				sc.ref(a, b, KindJSImport, base != "", true)
				sc.dirURL = saved
			}
		}
	}
	for _, m := range jsStringRe.FindAllSubmatchIndex(sc.src[s:e], -1) {
		a, b := s+m[4], s+m[5]
		if taken[a] {
			continue
		}
		sc.ref(a, b, KindJSString, false, false)
	}
}

// ref records the /c/ reference at [s,e).
func (sc *scanner) ref(s, e int, kind string, fixable, isImport bool) {
	ref := string(sc.src[s:e])
	target := strings.TrimPrefix(ref, "/c/")
	if i := strings.IndexAny(target, "?#"); i >= 0 {
		target = target[:i]
	}
	comp := sc.owner(path.Clean("/" + target)[1:])
	f := Finding{Kind: kind, Ref: ref, Target: comp}
	self := comp == sc.tile
	switch {
	case isChrome(comp):
		f.Target, f.Breaks = "chrome", BreaksStrict
		f.Note = "workspace chrome is never served to tile frames under strict gating — vendor what you need into the tile or /vendor/"
		fixable = false
	case kind == KindJSImport && self:
		f.Note = "works in every mode (the injected import map remaps the tile's own /c/ prefix); relative is still cleaner"
	case kind == KindJSImport:
		f.Breaks = BreaksTokens
		f.Note = "another tile's module by absolute URL: under tokens mode it loads only relative or through the workspace import map"
	case kind == KindJSString:
		f.Breaks = BreaksTokens
		f.Note = "a /c/ URL in code: used as a DOM URL (img.src, fetch) it carries no credential under tokens mode — resolve it relative to document.baseURI or import.meta.url; to navigate, use xbin.url()"
	default:
		f.Breaks = BreaksTokens
	}
	if fixable {
		f.Fix = relURL(sc.dirURL, ref, isImport)
	}
	sc.add(s, e, f.Kind, f.Ref, f.Fix, f.Note)
	last := &sc.out[len(sc.out)-1]
	last.Target, last.Breaks = f.Target, f.Breaks
}

func (sc *scanner) add(s, e int, kind, ref, fix, note string) {
	line := 1 + bytes.Count(sc.src[:s], []byte("\n"))
	col := s - bytes.LastIndexByte(sc.src[:s], '\n')
	sc.out = append(sc.out, Finding{File: sc.rel, Line: line, Col: col, Kind: kind, Ref: ref, Fix: fix, Note: note, start: s, end: e})
}

// relURL rewrites the absolute URL ref (a /c/ path, maybe with ?query or
// #fragment) relative to the URL directory dir. Imports always start with
// ./ or ../ (a bare specifier would hit the import map instead).
func relURL(dir, ref string, isImport bool) string {
	p, suffix := ref, ""
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		p, suffix = ref[:i], ref[i:]
	}
	trailing := strings.HasSuffix(p, "/")
	p = path.Clean(p)
	if trailing && p != "/" {
		p += "/"
	}
	from := strings.Split(strings.Trim(dir, "/"), "/")
	to := strings.Split(strings.TrimPrefix(p, "/"), "/")
	i := 0
	for i < len(from) && i < len(to)-1 && from[i] == to[i] {
		i++
	}
	rel := strings.Repeat("../", len(from)-i) + strings.Join(to[i:], "/")
	switch {
	case rel == "":
		rel = "./"
	case !strings.HasPrefix(rel, "../") && (isImport || strings.Contains(strings.SplitN(rel, "/", 2)[0], ":")):
		rel = "./" + rel
	}
	return rel + suffix
}
