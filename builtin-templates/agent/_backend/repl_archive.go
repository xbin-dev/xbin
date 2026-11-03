// repl_archive.go — zip, tar and gzip in the sandbox, natively: the model
// lists, reads and extracts an uploaded archive (or bytes it holds) without
// hand-writing an inflate in JS. A source is a session path or a
// Uint8Array/ArrayBuffer; results are Uint8Arrays, strings, or session files
// (extract, through the same blob-aware writer files.write uses). Pure over
// their input — no host filesystem: an entry name only ever becomes a
// session key, which normReplPath vets.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
)

// maxArchiveEntryBytes bounds one inflated entry (or gzip stream) read into
// the VM — a zip bomb ends here as an error, not an OOM. Extracting to a
// session file is bounded by maxBinaryFileBytes (the same), and the run's
// quotas bound an extraction's whole: maxReplFiles files, maxBinaryRunBytes.
const maxArchiveEntryBytes = 128 << 20

func errEntryTooLarge(max int) error {
	return fmt.Errorf("archive entry too large (over %s inflated)", humanBytes(max))
}

func (s *replSession) installArchive(vm *goja.Runtime) error {
	z := vm.NewObject()
	_ = z.Set("list", s.jsZipList)
	_ = z.Set("read", func(c goja.FunctionCall) goja.Value { return s.bytesVal(s.zipEntry("zip.read", c)) })
	_ = z.Set("readText", func(c goja.FunctionCall) goja.Value { return s.textVal("zip.readText", s.zipEntry("zip.readText", c)) })
	_ = z.Set("extract", s.jsZipExtract)
	_ = z.Set("create", s.jsZipCreate)
	if err := vm.Set("zip", z); err != nil {
		return err
	}
	t := vm.NewObject()
	_ = t.Set("list", s.jsTarList)
	_ = t.Set("read", func(c goja.FunctionCall) goja.Value { return s.bytesVal(s.tarEntry("tar.read", c)) })
	_ = t.Set("readText", func(c goja.FunctionCall) goja.Value { return s.textVal("tar.readText", s.tarEntry("tar.readText", c)) })
	_ = t.Set("extract", s.jsTarExtract)
	if err := vm.Set("tar", t); err != nil {
		return err
	}
	g := vm.NewObject()
	_ = g.Set("decompress", func(c goja.FunctionCall) goja.Value {
		b, err := gunzip(s.archiveSrc("gzip.decompress", c.Argument(0)))
		if err != nil {
			s.throw(fmt.Errorf("gzip.decompress: %w", err))
		}
		return s.bytesVal(b)
	})
	_ = g.Set("compress", func(c goja.FunctionCall) goja.Value {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(s.bytesArg("gzip.compress", c.Argument(0), true))
		_ = w.Close()
		return s.bytesVal(buf.Bytes())
	})
	return vm.Set("gzip", g)
}

// archiveSrc is an archive argument: a session path (a string), or bytes.
func (s *replSession) archiveSrc(what string, v goja.Value) []byte {
	if b, ok := s.asBytes(v); ok {
		return b
	}
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		s.throw(fmt.Errorf("%s: expected a session file path or a Uint8Array", what))
	}
	p, err := normReplPath(v.String())
	if err != nil {
		s.throw(fmt.Errorf("%s: %w", what, err))
	}
	f, err := s.db.replFile(s.runID, p)
	if err != nil {
		s.throw(fmt.Errorf("%s: %w", what, err))
	}
	b, err := s.fileBytes(f)
	if err != nil {
		s.throw(fmt.Errorf("%s: %w", what, err))
	}
	return b
}

func (s *replSession) textVal(what string, b []byte) goja.Value {
	if !utf8.Valid(b) {
		s.throw(fmt.Errorf("%s: the entry is not UTF-8 text — read its bytes instead", what))
	}
	return s.vm.ToValue(string(trimBOM(b)))
}

func readCapped(r io.Reader, max int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, errEntryTooLarge(max)
	}
	return b, nil
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readCapped(r, maxArchiveEntryBytes)
}

func isGzip(b []byte) bool { return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b }

// --- zip ------------------------------------------------------------------------

func (s *replSession) openZip(what string, v goja.Value) *zip.Reader {
	b := s.archiveSrc(what, v)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		hint := ""
		if isGzip(b) {
			hint = " (it is gzip — tar.* reads a .tar.gz, gzip.decompress a .gz)"
		}
		s.throw(fmt.Errorf("%s: not a zip archive: %v%s", what, err, hint))
	}
	return zr
}

func (s *replSession) jsZipList(c goja.FunctionCall) goja.Value {
	zr := s.openZip("zip.list", c.Argument(0))
	out := make([]any, 0, len(zr.File))
	for _, f := range zr.File {
		e := map[string]any{"name": f.Name, "size": int64(f.UncompressedSize64),
			"compressed": int64(f.CompressedSize64), "dir": f.FileInfo().IsDir()}
		if !f.Modified.IsZero() {
			e["modified"] = f.Modified.UTC().Format(time.RFC3339)
		}
		out = append(out, e)
	}
	return s.vm.ToValue(out)
}

func zipFind(zr *zip.Reader, name string) *zip.File {
	name = strings.TrimPrefix(name, "./")
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func readZipFile(f *zip.File, max int) ([]byte, error) {
	if f.UncompressedSize64 > uint64(max) {
		return nil, errEntryTooLarge(max)
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readCapped(r, max) // the header's size is a claim; the cap is not
}

func (s *replSession) zipEntry(what string, c goja.FunctionCall) []byte {
	zr := s.openZip(what, c.Argument(0))
	name := c.Argument(1).String()
	f := zipFind(zr, name)
	if f == nil {
		s.throw(fmt.Errorf("%s: no entry %q in the archive (zip.list shows the names)", what, name))
	}
	b, err := readZipFile(f, maxArchiveEntryBytes)
	if err != nil {
		s.throw(fmt.Errorf("%s: %s: %w", what, name, err))
	}
	return b
}

// zip.create({name: string|Uint8Array, …}) → the archive's bytes.
func (s *replSession) jsZipCreate(c goja.FunctionCall) goja.Value {
	o, ok := c.Argument(0).(*goja.Object)
	if !ok || o == nil {
		s.throw(fmt.Errorf("zip.create: expected an object of {name: string or Uint8Array}"))
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, k := range o.Keys() {
		data := s.bytesArg("zip.create: "+k, o.Get(k), true)
		fw, err := w.CreateHeader(&zip.FileHeader{Name: k, Method: zip.Deflate, Modified: s.nowFn()})
		if err != nil {
			s.throw(fmt.Errorf("zip.create: %s: %w", k, err))
		}
		_, _ = fw.Write(data)
	}
	if err := w.Close(); err != nil {
		s.throw(fmt.Errorf("zip.create: %w", err))
	}
	return s.bytesVal(buf.Bytes())
}

// --- extract (zip and tar) ------------------------------------------------------

type extractOpts struct {
	to   string
	only []string
}

func (s *replSession) extractOptsArg(what string, v goja.Value) extractOpts {
	var o extractOpts
	obj, ok := v.(*goja.Object)
	if !ok || obj == nil {
		return o
	}
	if t := obj.Get("to"); t != nil && !goja.IsUndefined(t) && !goja.IsNull(t) {
		o.to = strings.TrimPrefix(strings.TrimSpace(t.String()), "./")
		if o.to != "" && !strings.HasSuffix(o.to, "/") {
			o.to += "/"
		}
	}
	if on := obj.Get("only"); on != nil && !goja.IsUndefined(on) && !goja.IsNull(on) {
		var list []string
		if err := s.vm.ExportTo(on, &list); err != nil {
			list = []string{on.String()}
		}
		o.only = list
	}
	return o
}

// wants reports whether an entry is selected: an exact name, a directory
// prefix ("docs/"), or a glob ("*.md", "src/*/flake.lock").
func (o extractOpts) wants(name string) bool {
	if len(o.only) == 0 {
		return true
	}
	for _, p := range o.only {
		p = strings.TrimPrefix(p, "./")
		if p == name || (strings.HasSuffix(p, "/") && strings.HasPrefix(name, p)) {
			return true
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
		if !strings.Contains(p, "/") { // a bare glob matches the base name anywhere
			if ok, _ := path.Match(p, path.Base(name)); ok {
				return true
			}
		}
	}
	return false
}

var unsafeKeyRE = regexp.MustCompile(`[^A-Za-z0-9._/-]`)

// entryKey turns an archive entry name into a session key: odd characters
// become _, and a name that climbs out (../, /abs) is refused — the key is
// not a host path, but it must mean what it says.
func entryKey(to, name string) (string, error) {
	n := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(n, "/") {
		return "", fmt.Errorf("absolute entry name %q", name)
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return "", fmt.Errorf("entry name %q climbs out of the archive", name)
		}
	}
	n = unsafeKeyRE.ReplaceAllString(path.Clean(n), "_")
	return normReplPath(to + n)
}

type extracted struct {
	path, from string
	bytes      int
	binary     bool
}

// extractAll writes the selected entries as session files. next yields
// entries in order (nil name: done); the first failure stops it, naming what
// was already written.
func (s *replSession) extractAll(what string, o extractOpts, next func() (name string, dir bool, read func() ([]byte, error), err error)) goja.Value {
	var done []extracted
	fail := func(err error) {
		msg := fmt.Sprintf("%s: %v", what, err)
		if len(done) > 0 {
			msg += fmt.Sprintf(" — %d file(s) were extracted before this (files.list shows them)", len(done))
		}
		s.throw(errors.New(msg))
	}
	for {
		name, dir, read, err := next()
		if err != nil {
			fail(err)
		}
		if name == "" {
			break
		}
		if dir || !o.wants(name) {
			continue
		}
		key, err := entryKey(o.to, name)
		if err != nil {
			fail(err)
		}
		data, err := read()
		if err != nil {
			fail(fmt.Errorf("%s: %w", name, err))
		}
		declared := mimeFor(key)
		if declared == "" && utf8.Valid(data) {
			declared = "text/plain"
		}
		f, _, _, err := s.io.putFileData(s.ioCtx(), s.runID, key, data, declared, toolSource(s.ioCtx(), "js_eval"))
		if err != nil {
			fail(fmt.Errorf("%s: %w", key, err))
		}
		done = append(done, extracted{path: f.Path, from: name, bytes: f.Bytes, binary: f.Binary && !isTextMime(f.Mime)})
	}
	out := make([]any, 0, len(done))
	for _, e := range done {
		out = append(out, map[string]any{"path": e.path, "from": e.from, "bytes": e.bytes, "binary": e.binary})
	}
	return s.vm.ToValue(out)
}

func (s *replSession) jsZipExtract(c goja.FunctionCall) goja.Value {
	if s.io == nil {
		s.throw(fmt.Errorf("zip.extract: this sandbox cannot write session files"))
	}
	zr := s.openZip("zip.extract", c.Argument(0))
	o := s.extractOptsArg("zip.extract", c.Argument(1))
	i := 0
	return s.extractAll("zip.extract", o, func() (string, bool, func() ([]byte, error), error) {
		if i >= len(zr.File) {
			return "", false, nil, nil
		}
		f := zr.File[i]
		i++
		return f.Name, f.FileInfo().IsDir(), func() ([]byte, error) { return readZipFile(f, maxBinaryFileBytes) }, nil
	})
}

// --- tar (plain or gzipped) -------------------------------------------------------

func (s *replSession) openTar(what string, v goja.Value) *tar.Reader {
	b := s.archiveSrc(what, v)
	if isGzip(b) {
		var err error
		if b, err = gunzip(b); err != nil {
			s.throw(fmt.Errorf("%s: %w", what, err))
		}
	}
	return tar.NewReader(bytes.NewReader(b))
}

func tarIsFile(h *tar.Header) bool { return h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA }

func (s *replSession) jsTarList(c goja.FunctionCall) goja.Value {
	tr := s.openTar("tar.list", c.Argument(0))
	var out []any
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.throw(fmt.Errorf("tar.list: %w", err))
		}
		e := map[string]any{"name": h.Name, "size": h.Size, "dir": h.Typeflag == tar.TypeDir}
		if !tarIsFile(h) && h.Typeflag != tar.TypeDir {
			e["type"] = string(h.Typeflag)
			if h.Linkname != "" {
				e["link"] = h.Linkname
			}
		}
		if !h.ModTime.IsZero() {
			e["modified"] = h.ModTime.UTC().Format(time.RFC3339)
		}
		out = append(out, e)
	}
	if out == nil {
		out = []any{}
	}
	return s.vm.ToValue(out)
}

func (s *replSession) tarEntry(what string, c goja.FunctionCall) []byte {
	tr := s.openTar(what, c.Argument(0))
	name := strings.TrimPrefix(c.Argument(1).String(), "./")
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.throw(fmt.Errorf("%s: %w", what, err))
		}
		if strings.TrimPrefix(h.Name, "./") != name || !tarIsFile(h) {
			continue
		}
		b, err := readCapped(tr, maxArchiveEntryBytes)
		if err != nil {
			s.throw(fmt.Errorf("%s: %s: %w", what, name, err))
		}
		return b
	}
	s.throw(fmt.Errorf("%s: no file %q in the archive (tar.list shows the names)", what, name))
	return nil
}

func (s *replSession) jsTarExtract(c goja.FunctionCall) goja.Value {
	if s.io == nil {
		s.throw(fmt.Errorf("tar.extract: this sandbox cannot write session files"))
	}
	tr := s.openTar("tar.extract", c.Argument(0))
	o := s.extractOptsArg("tar.extract", c.Argument(1))
	return s.extractAll("tar.extract", o, func() (string, bool, func() ([]byte, error), error) {
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return "", false, nil, nil
			}
			if err != nil {
				return "", false, nil, err
			}
			if !tarIsFile(h) { // directories, links, devices: nothing to write
				continue
			}
			return strings.TrimPrefix(h.Name, "./"), false, func() ([]byte, error) { return readCapped(tr, maxBinaryFileBytes) }, nil
		}
	})
}
