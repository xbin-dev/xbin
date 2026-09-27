// Package backup defines xbin's self-describing component archive format
// (plans/lifecycle.md). A backup is a tar whose first entry, backup.json, fully
// describes the component — so given *only* the archive (no live workspace, no
// local metadata) restore can reconstruct the component's files and data. Vault
// is deliberately excluded (a bespoke secret backup comes later).
//
// Layout (all logical paths, mapped to real storage on restore):
//
//	backup.json                      the Manifest
//	source/…                         the component's source subtree
//	data/kv.json                     kv resources: {resource: {key: base64(value)}}
//	data/sqlite/<name>.sqlite        each sqlite resource, checkpointed
//	data/blob/<name>/…               each blob resource's files
//	term/…                           the component's terminal dev layer
package backup

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// Schema is bumped when the layout changes incompatibly.
const Schema = 1

// Logical path prefixes inside the tar.
const (
	ManifestName = "backup.json"
	SourcePrefix = "source/"
	DataPrefix   = "data/"
	KVName       = "data/kv.json"
	SQLitePrefix = "data/sqlite/"
	BlobPrefix   = "data/blob/"
	FSPrefix     = "data/fs/" // filesystem resources (a rw directory)
	TermPrefix   = "term/"
)

// Manifest is the self-describing header. Everything needed to place the tar's
// files back without consulting local state lives here.
type Manifest struct {
	Schema      int               `json:"schema"`
	Component   string            `json:"component"` // path = identity + restore target
	Scope       string            `json:"scope"`     // scope path ("" = workspace scope)
	ScopeRoot   bool              `json:"scopeRoot"` // component roots its scope → data included
	Resources   map[string]string `json:"resources,omitempty"`
	XBinVersion string            `json:"xbinVersion"`
	Created     string            `json:"created"` // RFC3339
	Includes    []string          `json:"includes"`
	CronJobs    []json.RawMessage `json:"cronJobs,omitempty"`
	BusSubs     []json.RawMessage `json:"busSubscriptions,omitempty"`
	WithVault   bool              `json:"withVault,omitempty"`
}

func (m Manifest) Has(part string) bool {
	for _, p := range m.Includes {
		if p == part {
			return true
		}
	}
	return false
}

// Writer builds a backup tar.
type Writer struct{ tw *tar.Writer }

func NewWriter(w io.Writer) *Writer { return &Writer{tw: tar.NewWriter(w)} }

// Manifest writes backup.json. Call it first.
func (w *Writer) Manifest(m Manifest) error {
	m.Schema = Schema
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return w.File(ManifestName, 0o644, b)
}

// File writes one in-memory entry.
func (w *Writer) File(name string, mode int64, data []byte) error {
	if err := w.tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := w.tw.Write(data)
	return err
}

// Stream writes one entry of known size from a reader (for large files).
func (w *Writer) Stream(name string, mode, size int64, r io.Reader) error {
	if err := w.tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := io.Copy(w.tw, r)
	return err
}

// Tree walks an on-disk directory and writes its regular files under prefix.
// skip(rel) drops a relative path (and, for a dir, its subtree). Missing dir is
// not an error (nothing to add). osDir itself is trusted (it may be reached
// through symlinks); see TreeIn for one that is not.
//
// What the walk meets below osDir was typically written by a sandbox, and
// xbind reads it: nothing is followed. Each directory is listed and each
// entry opened relative to its parent's fd with O_NOFOLLOW, so a symlink —
// even one swapped in for a file or a directory the walk already listed — is
// skipped, never read through. Symlinks, sockets and FIFOs are skipped as
// they always were; the order (lexical, depth-first) and the bytes are what
// a filepath.WalkDir walk wrote.
func (w *Writer) Tree(prefix, osDir string, skip func(rel string) bool) error {
	return w.TreeIn(prefix, osDir, "", skip)
}

// TreeIn is Tree(root/sub) for a sub-directory that is itself untrusted — a
// tile's source dir, which may sit in another tile's writable tree: sub must
// be reached from root without any symlink (fsutil.OpenIn), else the walk
// fails with fsutil.ErrEscapes rather than archive wherever the link points.
func (w *Writer) TreeIn(prefix, root, sub string, skip func(rel string) bool) error {
	d, err := fsutil.OpenIn(root, sub, "")
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR), errors.Is(err, fsutil.ErrNotRegular):
		return nil // nothing to add
	case err != nil:
		return err
	}
	defer d.Close()
	if fi, err := d.Stat(); err != nil {
		return err
	} else if !fi.IsDir() {
		return nil
	}
	return w.walk(prefix, "", d, skip)
}

// walk writes dir's subtree (rel is its path below the walk's root).
func (w *Writer) walk(prefix, rel string, dir *os.File, skip func(string) bool) error {
	ents, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, e := range ents {
		r := e.Name()
		if rel != "" {
			r = rel + "/" + r
		}
		if skip != nil && skip(r) {
			continue // a skipped directory drops its whole subtree
		}
		t := e.Type()
		if !t.IsDir() && !t.IsRegular() {
			continue // skip symlinks/sockets — restore recreates them (env) or ignores
		}
		f, err := openNoFollow(dir, e.Name(), t.IsDir())
		if errors.Is(err, errGone) {
			continue // swapped (for a link, a file, nothing) since the listing
		}
		if err != nil {
			return err
		}
		if t.IsDir() {
			err = w.walk(prefix, r, f, skip)
		} else {
			err = w.streamFile(prefix+r, f)
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) streamFile(name string, f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return w.Stream(name, int64(fi.Mode().Perm()), fi.Size(), f)
}

// errGone marks an entry that is no longer what the listing said: a symlink,
// something else, or nothing at all. The walk skips it.
var errGone = errors.New("entry changed since the listing")

func (w *Writer) Close() error { return w.tw.Close() }

// Reader reads a backup tar. The Manifest is parsed up front; Next yields the
// remaining entries in order.
type Reader struct {
	tr  *tar.Reader
	hdr *tar.Header // the entry Next returned last
	M   Manifest
}

func NewReader(r io.Reader) (*Reader, error) {
	tr := tar.NewReader(r)
	h, err := tr.Next()
	if err != nil {
		return nil, err
	}
	if h.Name != ManifestName {
		return nil, errors.New("backup: first entry is not " + ManifestName)
	}
	var m Manifest
	if err := json.NewDecoder(tr).Decode(&m); err != nil {
		return nil, err
	}
	if m.Schema > Schema {
		return nil, errors.New("backup: archive schema newer than this xbin — upgrade to restore")
	}
	return &Reader{tr: tr, M: m}, nil
}

// Next returns the next entry's name and a reader valid until the following
// Next. io.EOF signals the end.
func (r *Reader) Next() (string, io.Reader, error) {
	h, err := r.tr.Next()
	if err != nil {
		return "", nil, err
	}
	r.hdr = h
	return h.Name, r.tr, nil
}

// Perm is the permission bits the entry Next returned last was archived with
// (Tree records each file's): its mode & 0o777 — never setuid, setgid or
// sticky, whatever the archive says. 0 before the first Next.
func (r *Reader) Perm() fs.FileMode {
	if r.hdr == nil {
		return 0
	}
	return fs.FileMode(r.hdr.Mode) & fs.ModePerm
}

// SafeJoin joins a tar entry name onto a base dir, guaranteed to stay within
// base: the name is rooted at "/" and cleaned first, so any ".." is neutralised
// (a hostile "../../etc/passwd" clamps to base/etc/passwd, never escaping base).
func SafeJoin(base, name string) string {
	return filepath.Join(base, filepath.FromSlash(path.Clean("/"+name)))
}
