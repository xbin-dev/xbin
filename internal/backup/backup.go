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
//
// A tile with a deployment record adds its deployment state, right after
// backup.json (see Deployments):
//
//	deployments/record.json          the deployment record, verbatim
//	deployments/checkpoints/…        its checkpoint store's git data: packed-refs, refs/…, objects/…
//	deployments/registrations/<d>/…  deployment d's registration files (d is never main)
//
// That is a main archive: schema 1, whatever the tile has — or, in a
// workspace with a vault barrier, schema 3 (SchemaSplit): the same without
// its data/ members, which a data archive of their own holds (Kind "data",
// named by the main manifest's Data), each archive sealed (seal.go). A deployment
// archive holds one deployment's data namespace beyond main and nothing
// else — no source, no terminal layer, no registrations in its manifest —
// under the same data/ layout, with Deployment naming it and schema 2, so an
// older xbind refuses it instead of restoring it into main's keys.
//
// A partition archive (Kind "partition", schema 3, always sealed) holds one
// person's partition of a tile (plans/partitions/11-backup-encryption.md
// §2): its namespace's data under the same data/ layout — when the tile
// roots its scope — and its own records under partition/, named by the
// manifest's Partition:
//
//	partition/partition.json         the partition's record, verbatim
//	partition/ns.json                its namespace's identity and history
//	partition/vault.json             its vault file, its values still sealed by the vault
//	partition/registrations/<file>   its registration files (cron, bus, …)
//
// It is restored only into that person's partition of the tile, never as a
// tile or into a data namespace.
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

// Schema is a main archive's schema, which stays 1: an older xbind restores
// a main archive of any tile, skipping the sections it doesn't know.
const Schema = 1

// SchemaDeployment is a deployment archive's schema. An older reader refuses
// it ("upgrade to restore") rather than put a deployment's data into main's
// keys.
const SchemaDeployment = 2

// SchemaSplit is the schema of a sealed workspace's main archive, whose
// scope data went to a data archive of its own (Data names it), and of that
// data archive (Kind "data"): each object is sealed under one subkey, so
// erasing the data's leaves the source restorable
// (plans/partitions/11-backup-encryption.md §4). An older reader refuses
// both. A workspace without a vault barrier keeps writing schema 1, its
// data inline.
const SchemaSplit = 3

// MaxSchema is the newest schema this xbind reads.
const MaxSchema = SchemaSplit

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

// A tile's deployment state, in the main archive of a tile with a
// deployment record only. Every entry is a regular file, so an older
// xbind's restore — which has no arm for the prefix — skips them, and
// restores the archive as a tile in the zero state.
const (
	DeploymentsPrefix   = "deployments/"
	RecordName          = "deployments/record.json"    // the tile's deployment record, verbatim
	CheckpointsPrefix   = "deployments/checkpoints/"   // its checkpoint store, by its path in the bare repository
	RegistrationsPrefix = "deployments/registrations/" // <deployment>/<file>: a deployment's registration files
)

// A partition archive's own members (Kind "partition").
const (
	PartitionPrefix = "partition/"
	PartRecordName  = "partition/partition.json"
	PartNSName      = "partition/ns.json"
	PartVaultName   = "partition/vault.json"
	PartRegsPrefix  = "partition/registrations/"
)

// Manifest is the self-describing header. Everything needed to place the tar's
// files back without consulting local state lives here.
type Manifest struct {
	Schema      int               `json:"schema"`
	Component   string            `json:"component"`            // path = identity + restore target
	Deployment  string            `json:"deployment,omitempty"` // a deployment archive's: whose namespace data/ holds (absent: main)
	Scope       string            `json:"scope"`                // scope path ("" = workspace scope)
	ScopeRoot   bool              `json:"scopeRoot"`            // component roots its scope → data included
	Resources   map[string]string `json:"resources,omitempty"`
	XBinVersion string            `json:"xbinVersion"`
	Created     string            `json:"created"` // RFC3339
	Includes    []string          `json:"includes"`
	CronJobs    []json.RawMessage `json:"cronJobs,omitempty"`
	BusSubs     []json.RawMessage `json:"busSubscriptions,omitempty"`
	WithVault   bool              `json:"withVault,omitempty"`
	// Sandboxes are the tile sandbox definitions of a manager tile (D120):
	// definitions only, never their state (plans/tile-sandbox-runtime.md
	// §9). A restore merges them by uid. Absent for a tile with none, so
	// its archive is what it always was, byte for byte.
	Sandboxes []json.RawMessage `json:"sandboxes,omitempty"`
	// Deployments is present only in the main archive of a tile with a
	// deployment record, so a tile without one — one that opted out and
	// kept its checkpoint store included — gets today's manifest.
	Deployments *Deployments `json:"deployments,omitempty"`
	// Data, in a schema-3 main archive, names the data archive the same
	// backup wrote the scope's main data into: restored with this one.
	Data *DataRef `json:"data,omitempty"`
	// Kind is "data" for a data archive (schema 3): the main namespace's
	// data of the scope Component roots, and nothing else. Absent
	// otherwise.
	Kind string `json:"kind,omitempty"`
	// BackupID is a random id one backup's schema-3 main archive and the
	// data archive it names share: a restore pairs them only when the ids
	// match, so no data archive of another backup of the tile is ever
	// restored with this one. Absent otherwise.
	BackupID string `json:"backupId,omitempty"`
	// Partition, in a partition archive (Kind "partition"), names whose
	// partition of Component it holds. Absent otherwise.
	Partition *PartitionRef `json:"partition,omitempty"`
}

// PartitionRef is whose partition a partition archive holds: the partition
// id (u-<32 hex>, the person's user id and uid hashed), the person, the uid
// they had when it was written, and the deployment the partition ran in.
type PartitionRef struct {
	ID         string `json:"id"`
	User       string `json:"user"`
	UID        string `json:"uid"`
	Deployment string `json:"deployment"`
}

// DataRef names a data archive: its archiver key and version, and the
// subkey it is sealed under — so a restore can tell a data archive whose
// key was erased (and whose versions the archiver then deleted) from one
// that is missing.
type DataRef struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	Subkey  string `json:"subkey,omitempty"`
}

// Deployments is the manifest's deployment section: what of the tile's
// deployment state the archive holds. It belongs to the manifest's
// Component, and a restore into any other tile refuses the archive before
// it writes anything.
type Deployments struct {
	Record      bool `json:"record"`      // RecordName is in the archive
	Checkpoints bool `json:"checkpoints"` // so is the checkpoint store, under CheckpointsPrefix
	// Archives lists the deployment archives the same backup wrote, by
	// deployment name: their versions, restored with this one.
	Archives map[string]string `json:"archives,omitempty"`
}

// DeploymentArchive reports whether m is a deployment archive's: one
// deployment's data, restored only into a data namespace, never as a tile.
func (m Manifest) DeploymentArchive() bool {
	return m.Schema == SchemaDeployment || m.Deployment != ""
}

// DataArchive reports whether m is a data archive's (schema 3): the main
// namespace's data, restored with its main archive or into main's
// namespace, never as a tile.
func (m Manifest) DataArchive() bool { return m.Kind == KindData }

// PartitionArchive reports whether m is a partition archive's: one person's
// partition, restored only into that person's partition, never as a tile or
// into a data namespace.
func (m Manifest) PartitionArchive() bool { return m.Kind == KindPartition || m.Partition != nil }

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

// Manifest writes backup.json. Call it first. The caller sets the schema
// (SchemaDeployment for a deployment archive); unset is a main archive's.
func (w *Writer) Manifest(m Manifest) error {
	if m.Schema == 0 {
		m.Schema = Schema
	}
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

// TreeBeneath writes the regular files beneath dir under prefix, as Tree
// does, but never follows a symlink out of dir, and never opens a FIFO or a
// device: each directory and file is opened through fsutil.OpenBeneath, and
// symlinks and special files are skipped by their own type. An entry
// swapped for a symlink between the listing and the open is read from
// inside dir or skipped; one that vanishes is skipped. Entries go in name
// order; keep(rel, isDir) selects them (nil keeps all), and a directory it
// drops is not descended into. A missing dir adds nothing.
func (w *Writer) TreeBeneath(prefix, dir string, keep func(rel string, isDir bool) bool) error {
	return w.treeBeneath(prefix, dir, "", keep)
}

func (w *Writer) treeBeneath(prefix, dir, rel string, keep func(string, bool) bool) error {
	d, err := fsutil.OpenBeneath(dir, rel)
	if err != nil {
		if gone(err) {
			return nil
		}
		return err
	}
	fi, err := d.Stat()
	if err != nil || !fi.IsDir() {
		d.Close()
		return err
	}
	ents, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, e := range ents {
		child := path.Join(rel, e.Name())
		switch t := e.Type(); {
		case t.IsDir():
			if keep == nil || keep(child, true) {
				if err := w.treeBeneath(prefix, dir, child, keep); err != nil {
					return err
				}
			}
		case t.IsRegular():
			if keep == nil || keep(child, false) {
				if err := w.fileBeneath(prefix, dir, child); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (w *Writer) fileBeneath(prefix, dir, rel string) error {
	f, err := fsutil.OpenBeneath(dir, rel)
	if err != nil {
		if gone(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil // a directory swapped in mid-walk
	}
	// the size the header promises, whatever the file does meanwhile
	return w.Stream(prefix+rel, int64(fi.Mode().Perm()), fi.Size(), io.LimitReader(f, fi.Size()))
}

// gone reports an entry TreeBeneath skips: it vanished, left dir (or
// became a symlink loop), or isn't a regular file or a directory.
func gone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fsutil.ErrEscapes) || errors.Is(err, fsutil.ErrNotRegular) ||
		errors.Is(err, syscall.ELOOP)
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
	if m.Schema > MaxSchema {
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
