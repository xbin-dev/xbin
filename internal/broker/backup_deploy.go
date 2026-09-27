package broker

// backup_deploy.go — a tile's deployment state in its backup and restore
// (08-data §11.1, §11.2, §11.4; 06-security T11, ledger L13).
//
// A tile with a deployment record gets two sections in its main archive,
// right after backup.json: the record, verbatim, and its checkpoint store's
// git data (packed-refs, refs, objects). Nothing else changes: the manifest
// keeps schema 1 and gains only its deployment section, the other members
// are today's, and POST /restore's body is today's. A tile without a record
// — one that opted out and kept its store included — gets today's archive,
// byte for byte (P5); an older xbind's restore skips the new sections and
// restores the tile in the zero state.
//
// A restore refuses an archive whose deployment section belongs to another
// tile before it writes anything. It stages the archived store in a
// directory of xbind's own: the objects only, never the archived config,
// hooks/, info/ or alternates, while the archived refs are read as data.
// Confined git (D78) then checks each ref names a commit among those
// objects, and fscks them. What passes goes to the deployments plane, which
// rebuilds the store from those objects, keeps the refs under the
// restore-only namespace refs/xbin/restored/ (so checkpoint GC keeps them,
// and no current ref moves), and installs the record only when the tile has
// none. All of that happens before the restore writes the tile's files.
//
// The main archive also carries the registration files of the tile's
// deployments beyond main, which a restore validates row by row and merges
// into the deployments that exist (backup_restore.go),
// and names in its manifest the deployment archives the same backup wrote.
// A deployment archive (schema 2, key .deployments.<TileKey>.<d>) holds one
// deployment's data namespace, walked beneath each volume (08-data §11.1–
// §11.3, §11.6).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	// restoredRefs is the restore-only ref namespace of a checkpoint store:
	// an archived refs/xbin/<rest> comes back as refs/xbin/restored/<rest>.
	restoredRefs = "refs/xbin/restored/"

	maxArchivedRecord = 1 << 20  // a record is a few hundred bytes (the plane reads no more)
	maxArchivedRef    = 4 << 10  // one loose ref: an object id and a newline
	maxPackedRefs     = 16 << 20 // packed-refs
	maxArchivedRefs   = 1 << 16  // refs one archive may bring back
	restoreCheckTime  = 10 * time.Minute
	maxRegFile        = 1 << 20 // one registration file
	maxArchivedRegs   = 256     // registration files one archive may bring back
)

// deploymentRestorer puts a validated deployment section back: the
// deployments plane's hook. record is the archived record, which
// deployments.ParseRecord accepted for tile (nil: none to install); objects
// is a bare repository staged from the archive — the archived objects and,
// as loose refs, the refs listed in refs, under their restore-only names;
// no config, hooks, info or alternates ("" when no store is archived); it
// is removed after the call. The plane rebuilds tile's store from objects
// in confine, creating each ref that is absent and moving none, then
// installs the record only when the tile has none (08-data §11.4).
type deploymentRestorer func(ctx context.Context, tile string, record []byte, objects string, refs map[string]string) error

// deploymentStateRestorer is the plane's restorer: nil until the
// deployments plane installs one (DeploymentHooks.RestoreDeploymentState,
// which boot wires to the plane). Until then a restore checks whose
// deployment section it reads, refuses another tile's, and leaves the rest
// out.
func (b *Broker) deploymentStateRestorer() deploymentRestorer { return b.RestoreDeploymentState }

// ---- backup ----

// deploymentBackup is what a tile's main archive adds for its deployment
// state; nil for a tile in the zero state.
type deploymentBackup struct {
	record []byte    // data/deployments/<TileKey>.json, verbatim
	store  string    // data/checkpoints/<TileKey>.git; "" when the tile has none
	regs   []regFile // the registration files of its deployments beyond main
}

// regFile is one registration file of a deployment beyond main.
type regFile struct {
	dep, file string
	data      []byte
}

// deploymentBackupFor reads tile's deployment state for its backup: the
// record file at the tile's key, when it names the tile and its current
// owner ref — the record the tile answers to (P29), whether or not it
// validates, which the restore checks — and the checkpoint store beside it.
// No record, or one that isn't the tile's: nil, today's archive (P5).
func (b *Broker) deploymentBackupFor(tile string) *deploymentBackup {
	root := b.Reg.Root
	data, err := readBeneathCapped(filepath.Join(root, "data", "deployments"), util.TileKey(tile)+".json", maxArchivedRecord)
	if err != nil {
		return nil
	}
	name, owner, ok := recordBinding(data)
	if !ok || name != tile || owner != b.ownerRef(tile) {
		return nil
	}
	db := &deploymentBackup{record: data}
	store := checkpointStoreDir(root, tile)
	if _, err := os.Lstat(filepath.Join(store, "HEAD")); err == nil {
		db.store = store
	}
	_, names := b.deploymentsOf(tile)
	for _, dep := range names {
		for _, file := range registrationFileNames {
			if dep == util.MainDeployment {
				break // main's registrations are the manifest's rows, as today
			}
			switch data, err := b.readDeploymentFile(tile, dep, file); {
			case err != nil:
			case len(data) > maxRegFile:
				slog.Warn("backup: a registration file too large to archive is left out", "tile", tile, "deployment", dep, "file", file)
			default:
				db.regs = append(db.regs, regFile{dep, file, data})
			}
		}
	}
	return db
}

// ownerRef is tile's current owner ref ("" = workspace-owned).
func (b *Broker) ownerRef(tile string) string {
	if b.Users == nil {
		return ""
	}
	return b.Users.Owner(tile)
}

// section fills m's deployment section, listing the deployment archives
// the same backup wrote. Nil-safe: a zero-state tile's manifest keeps none.
func (d *deploymentBackup) section(m *backup.Manifest, archives map[string]string) {
	if d != nil {
		m.Deployments = &backup.Deployments{Record: true, Checkpoints: d.store != "", Archives: archives}
	}
}

// write adds the record and the store's git data, right after the
// manifest. Refs go before objects: git writes objects before the refs that
// name them, so each archived ref's objects were in the store when the ref
// was read, and restore checks every ref against the archived objects
// anyway. Every directory and file is opened beneath the store (08-data
// §11.6). Nil-safe.
func (d *deploymentBackup) write(bw *backup.Writer) error {
	if d == nil {
		return nil
	}
	if err := bw.File(backup.RecordName, 0o600, d.record); err != nil {
		return err
	}
	if d.store != "" {
		if err := bw.TreeBeneath(backup.CheckpointsPrefix, d.store, storeRefsMember); err != nil {
			return err
		}
		if err := bw.TreeBeneath(backup.CheckpointsPrefix, d.store, storeObjectsMember); err != nil {
			return err
		}
	}
	for _, r := range d.regs {
		if err := bw.File(backup.RegistrationsPrefix+r.dep+"/"+r.file, 0o600, r.data); err != nil {
			return err
		}
	}
	return nil
}

var (
	looseObjectDir = regexp.MustCompile(`^objects/[0-9a-f]{2}$`)
	looseObject    = regexp.MustCompile(`^objects/[0-9a-f]{2}/(?:[0-9a-f]{38}|[0-9a-f]{62})$`)
	packFile       = regexp.MustCompile(`^objects/pack/pack-(?:[0-9a-f]{40}|[0-9a-f]{64})\.(?:pack|idx)$`)
)

// storeRefsMember keeps a store's refs: packed-refs and refs/…, never a
// lock file.
func storeRefsMember(rel string, dir bool) bool {
	if dir {
		return rel == "refs" || strings.HasPrefix(rel, "refs/")
	}
	return rel == "packed-refs" || strings.HasPrefix(rel, "refs/") && !strings.HasSuffix(rel, ".lock")
}

// storeObjectsMember keeps a store's objects: loose ones and packs with
// their indexes. The store's config, hooks, info, index, quarantine and
// objects/info/ — alternates included — are never archived: a restore
// never uses them.
func storeObjectsMember(rel string, dir bool) bool {
	if dir {
		return rel == "objects" || rel == "objects/pack" || looseObjectDir.MatchString(rel)
	}
	return looseObject.MatchString(rel) || packFile.MatchString(rel)
}

// ---- restore ----

// deploymentRestore gathers the deployment section of the archive a
// restore reads, and settles it — validates it and hands it to the plane —
// before the restore writes anything else. nil for an archive without a
// deployment section: its deployments/ entries, if any, are skipped as an
// older xbind skips them.
type deploymentRestore struct {
	root, tile string
	put        deploymentRestorer

	record  []byte
	stage   string            // the staged bare repository; "" until the first object
	packed  map[string]string // packed-refs: ref → object id
	loose   map[string]string // refs/… files, which win over packed-refs
	ignored []string          // archived store paths never restored: config, hooks/, info/, alternates, …
	late    int               // deployment entries after the first other member: never used
	settled bool
	regs    []regFile                         // registration files, put back once the record is settled
	putRegs func(tile string, regs []regFile) // the broker's registration restore; nil: left out
}

// newDeploymentRestore starts the restore of m's deployment section into
// tile. A section that belongs to another tile is refused here, before the
// restore writes anything (06-security T11.5).
func newDeploymentRestore(root, tile string, m backup.Manifest, put deploymentRestorer) (*deploymentRestore, error) {
	if m.Deployments == nil {
		return nil, nil
	}
	if m.Component != tile {
		return nil, fmt.Errorf("the archive holds the deployment state of %s, not %s: refused, nothing was restored", m.Component, tile)
	}
	return &deploymentRestore{root: root, tile: tile, put: put, packed: map[string]string{}, loose: map[string]string{}}, nil
}

// takes reports whether name is a deployment entry this restore reads.
// Nil-safe.
func (d *deploymentRestore) takes(name string) bool {
	return d != nil && strings.HasPrefix(name, backup.DeploymentsPrefix)
}

// entry reads one deployment entry of the archive. An entry after the
// first member of another kind is never used: xbind writes the deployment
// state right after the manifest, so it is settled before the tile's files
// are written.
func (d *deploymentRestore) entry(name string, r io.Reader) error {
	if d.settled {
		d.late++
		return nil
	}
	switch {
	case name == backup.RecordName:
		if d.record != nil {
			return errors.New("the archive holds two deployment records: refused, nothing was restored")
		}
		data, err := readCapped(r, maxArchivedRecord)
		if err != nil {
			return fmt.Errorf("the archive's deployment record: %w", err)
		}
		d.record = data
		return nil
	case strings.HasPrefix(name, backup.CheckpointsPrefix):
		return d.storeEntry(strings.TrimPrefix(name, backup.CheckpointsPrefix), r)
	case strings.HasPrefix(name, backup.RegistrationsPrefix):
		dep, file, _ := strings.Cut(strings.TrimPrefix(name, backup.RegistrationsPrefix), "/")
		if dep == util.MainDeployment || !util.DeploymentNameOK(dep) || !slices.Contains(registrationFileNames, file) ||
			len(d.regs) == maxArchivedRegs {
			break
		}
		data, err := readCapped(r, maxRegFile)
		if err != nil {
			return fmt.Errorf("the archive's registration file %s: %w", name, err)
		}
		d.regs = append(d.regs, regFile{dep, file, data})
		return nil
	}
	d.ignored = append(d.ignored, name) // sections this xbind doesn't know
	return nil
}

// storeEntry reads one entry of the archived store. Objects are staged;
// refs are read as data; anything else — config, hooks/, info/,
// objects/info/alternates, HEAD, the index — is never written anywhere.
func (d *deploymentRestore) storeEntry(rel string, r io.Reader) error {
	switch {
	case rel == "packed-refs":
		data, err := readCapped(r, maxPackedRefs)
		if err != nil {
			return fmt.Errorf("the archive's checkpoint store: packed-refs: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if id, ref, ok := strings.Cut(strings.TrimSpace(line), " "); ok && objectID(id) && archivedRefOK(ref) {
				d.packed[ref] = id
			}
		}
		return nil
	case strings.HasPrefix(rel, "refs/"):
		if !archivedRefOK(rel) {
			d.ignored = append(d.ignored, rel)
			return nil
		}
		data, err := readCapped(r, maxArchivedRef)
		if err != nil {
			return fmt.Errorf("the archive's checkpoint store: %s: %w", rel, err)
		}
		if id := strings.TrimSpace(string(data)); objectID(id) {
			d.loose[rel] = id
		} else {
			d.ignored = append(d.ignored, rel)
		}
		return nil
	case looseObject.MatchString(rel) || packFile.MatchString(rel):
		return d.stageObject(rel, r)
	}
	d.ignored = append(d.ignored, rel)
	return nil
}

// stageObject writes one archived object file into the staged repository,
// created on the first: .xbin/restore/<random>, xbind's own, holding only
// HEAD, objects/ and refs/. rel matched an object path, so it stays beneath
// the stage.
func (d *deploymentRestore) stageObject(rel string, r io.Reader) error {
	if d.stage == "" {
		stage := filepath.Join(d.root, ".xbin", "restore", util.RandomToken(8))
		for _, sub := range []string{"objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(stage, sub), 0o755); err != nil {
				return fmt.Errorf("staging the archive's checkpoint store: %w", err)
			}
		}
		d.stage = stage // close removes it from here on
		if err := os.WriteFile(filepath.Join(stage, "HEAD"), []byte("ref: refs/heads/restore\n"), 0o644); err != nil {
			return fmt.Errorf("staging the archive's checkpoint store: %w", err)
		}
	}
	p := filepath.Join(d.stage, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("staging the archive's checkpoint store: %w", err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return fmt.Errorf("staging the archive's checkpoint store: %w", err)
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("staging the archive's checkpoint store: %w", err)
	}
	return nil
}

// settle validates the gathered section and hands it to the plane, once,
// before the restore writes its first file. It refuses the restore when the
// record belongs to another tile, or the archived store's objects fail
// their checks. A record this xbind can't use otherwise (a newer schema, a
// broken invariant) isn't installed, and the rest goes on. Without a
// restorer only the record's tile is checked: nothing else would be used.
// Then the registration files go back, into the deployments that exist
// once the record is settled. Nil-safe.
func (d *deploymentRestore) settle() error {
	if d == nil || d.settled {
		return nil
	}
	if err := d.settleState(); err != nil {
		return err
	}
	if d.putRegs != nil && len(d.regs) > 0 {
		d.putRegs(d.tile, d.regs)
	}
	return nil
}

func (d *deploymentRestore) settleState() error {
	d.settled = true
	var record []byte
	if d.record != nil {
		if _, err := deployments.ParseRecord(d.record, d.tile); err != nil {
			if name, _, ok := recordBinding(d.record); ok && name != d.tile {
				return fmt.Errorf("the archive's deployment record belongs to %s, not %s: refused, nothing was restored", name, d.tile)
			}
			slog.Warn("restore: the archived deployment record can't be used and isn't installed", "tile", d.tile, "why", err)
		} else {
			record = d.record
		}
	}
	refs := d.refs()
	if len(d.ignored) > 0 {
		slog.Info("restore: archived deployment files left out", "tile", d.tile, "count", len(d.ignored), "first", d.ignored[0])
	}
	if d.stage == "" || len(refs) == 0 {
		refs = nil
	}
	if record == nil && refs == nil {
		return nil
	}
	if d.put == nil {
		slog.Warn("restore: this xbind can't put tile deployment state back yet; the record and checkpoint store are left out", "tile", d.tile)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), restoreCheckTime)
	defer cancel()
	objects := ""
	if refs != nil {
		kept, err := checkArchivedRefs(ctx, d.stage, refs)
		if err != nil {
			return fmt.Errorf("the archive's checkpoint store can't be restored: %w; refused, nothing was restored", err)
		}
		if len(kept) == 0 {
			slog.Warn("restore: no archived checkpoint ref names a commit among the archived objects", "tile", d.tile)
		} else {
			objects = d.stage
		}
		refs = kept
	}
	if record == nil && objects == "" {
		return nil
	}
	if err := d.put(ctx, d.tile, record, objects, refs); err != nil {
		return fmt.Errorf("restoring the deployment state of %s: %w; nothing else was restored", d.tile, err)
	}
	return nil
}

// close removes the staged repository. Nil-safe.
func (d *deploymentRestore) close() {
	if d == nil {
		return
	}
	if d.late > 0 {
		slog.Warn("restore: deployment entries after the tile's files were left out", "tile", d.tile, "count", d.late)
	}
	if d.stage != "" {
		_ = os.RemoveAll(d.stage)
		_ = os.Remove(filepath.Dir(d.stage)) // .xbin/restore, only when empty
	}
}

// refs are the archived refs, loose over packed, by their restore-only
// names: at most maxArchivedRefs of them, the first by name.
func (d *deploymentRestore) refs() map[string]string {
	all := map[string]string{}
	for ref, id := range d.packed {
		all[ref] = id
	}
	for ref, id := range d.loose {
		all[ref] = id
	}
	names := make([]string, 0, len(all))
	for ref := range all {
		names = append(names, ref)
	}
	sort.Strings(names)
	out := map[string]string{}
	for _, ref := range names {
		if len(out) == maxArchivedRefs {
			slog.Warn("restore: the archive's checkpoint store has too many refs; the rest are left out", "tile", d.tile, "kept", maxArchivedRefs)
			break
		}
		out[restoredName(ref)] = all[ref]
	}
	return out
}

// archivedRef is the store's own ref layout (07-runtime §2.1; 11-contract
// §10.3), and the same under the restore-only namespace: a retention root
// or a git view named by its tree id, or a deployment's deploy log.
var archivedRef = regexp.MustCompile(`^refs/xbin/(?:restored/)?(?:(?:checkpoints|views)/(?:[0-9a-f]{40}|[0-9a-f]{64})|log/([^/]+))$`)

// archivedRefOK reports whether an archived ref is one of the store's.
// Every other ref is left out.
func archivedRefOK(ref string) bool {
	m := archivedRef.FindStringSubmatch(ref)
	return m != nil && (m[1] == "" || util.DeploymentNameOK(m[1]))
}

// restoredName is an archived ref's restore-only name.
func restoredName(ref string) string {
	rest := strings.TrimPrefix(ref, "refs/xbin/")
	return restoredRefs + strings.TrimPrefix(rest, "restored/")
}

// checkArchivedRefs keeps the refs that name commits among the staged
// objects — a retention root's commit must also carry the tree its name
// says — writes them into the stage as loose refs, and fscks the staged
// objects from them (06-security L13). Both runs are confined git over the
// stage alone, bound read-only; ids reach git on stdin, never argv. A
// failing fsck refuses the whole store: which ref it broke can't be told.
func checkArchivedRefs(ctx context.Context, stage string, refs map[string]string) (map[string]string, error) {
	names := make([]string, 0, len(refs))
	for ref := range refs {
		names = append(names, ref)
	}
	sort.Strings(names)
	var in bytes.Buffer
	for _, ref := range names {
		in.WriteString(refs[ref] + "\n")
		if strings.HasPrefix(ref, restoredRefs+"checkpoints/") {
			in.WriteString(refs[ref] + "^{tree}\n")
		}
	}
	cmd := confine.Cmd{Dir: stage, ReadOnlyDir: true, Stdin: &in, Timeout: restoreCheckTime, Env: []string{"GIT_NO_REPLACE_OBJECTS=1"}}
	out, err := confine.GitBytes(ctx, cmd, "--git-dir="+stage, "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, fmt.Errorf("checking its refs: %w", err)
	}
	lines := bufio.NewScanner(bytes.NewReader(out))
	lines.Buffer(make([]byte, 64<<10), 64<<10)
	next := func() (id, typ string) {
		if !lines.Scan() {
			return "", ""
		}
		id, typ, _ = strings.Cut(lines.Text(), " ")
		return id, typ
	}
	kept := map[string]string{}
	for _, ref := range names {
		id, typ := next()
		ok := id == refs[ref] && typ == "commit"
		if tree := strings.TrimPrefix(ref, restoredRefs+"checkpoints/"); tree != ref {
			tid, ttyp := next()
			ok = ok && tid == tree && ttyp == "tree"
		}
		if ok {
			kept[ref] = refs[ref]
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}
	for ref, id := range kept {
		p := filepath.Join(stage, filepath.FromSlash(ref))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(id+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	cmd = confine.Cmd{Dir: stage, ReadOnlyDir: true, Timeout: restoreCheckTime, Env: []string{"GIT_NO_REPLACE_OBJECTS=1"}}
	args := append(append([]string(nil), fsckContentPolicy...), "--git-dir="+stage, "fsck", "--strict", "--no-dangling", "--no-reflogs", "--no-progress")
	if _, err := confine.GitBytes(ctx, cmd, args...); err != nil {
		return nil, fmt.Errorf("fsck: %w", err)
	}
	return kept, nil
}

// fsckContentPolicy turns off fsck's .gitmodules checks, which guard a
// checkout that recurses into submodules — nothing a checkpoint store does
// — and would refuse a tile that merely holds an odd .gitmodules file. The
// checks of every object's integrity and of the refs' connectivity stay.
// Each id is git's since 2.17, so an older host git doesn't refuse the flag.
var fsckContentPolicy = []string{
	"-c", "fsck.gitmodulesBlob=ignore", "-c", "fsck.gitmodulesLarge=ignore", "-c", "fsck.gitmodulesName=ignore",
	"-c", "fsck.gitmodulesPath=ignore", "-c", "fsck.gitmodulesSymlink=ignore", "-c", "fsck.gitmodulesUrl=ignore",
}

// ---- deployment archives ----

// archiveKey is deployment dep of tile's archive key (08-data §11.3):
// main's is today's CompKey; any other's is .deployments.<TileKey>.<d>,
// which no CompKey equals (none starts with "."), split by its dots (neither
// part holds one), and one path segment, so the archiver never lists two
// keys together.
func archiveKey(tile, dep string) string {
	if dep == util.MainDeployment {
		return backupKey(tile)
	}
	return ".deployments." + util.TileKey(tile) + "." + dep
}

// writeDeploymentArchive streams deployment dep's data archive of the scope
// root tile c: a schema-2 manifest naming dep, then the data of every
// resource dep's namespace declares, as a main archive lays data out. It
// holds nothing else: no source, terminal layer, registrations or vault.
func (b *Broker) writeDeploymentArchive(bw *backup.Writer, c *registry.Component, dep string) error {
	if b.vaultSealed() {
		return fmt.Errorf("vault sealed — unseal before backing up encrypted resources")
	}
	declared, err := b.declaredIn(c.Path, dep)
	if err != nil {
		slog.Warn("backup: resources left out of a deployment archive", "tile", c.Path, "deployment", dep, "err", err)
	}
	m := backup.Manifest{Schema: backup.SchemaDeployment, Component: c.Path, Deployment: dep, Scope: c.Path, ScopeRoot: true,
		Resources: map[string]string{}, XBinVersion: b.Version, Created: time.Now().UTC().Format(time.RFC3339),
		Includes: []string{"data"}}
	for name, res := range declared {
		m.Resources[name] = res.Type
	}
	if err := bw.Manifest(m); err != nil {
		return err
	}
	kv := map[string]map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		k, err := b.resKeys(resTarget{Scope: c.Path, Name: name}, dep)
		if err != nil {
			return err
		}
		switch typ := declared[name].Type; {
		case typ == "kv":
			if kv[name], err = b.dumpNSKV(k); err != nil {
				return fmt.Errorf("%s's %s: %w", dep, name, err)
			}
		case fileBackedType(typ) && b.resenc.Encrypted(k.DirKey, k.Name): // a volume never written holds nothing
			if !b.ensureVolume(k, c.Path, typ) {
				return fmt.Errorf("%s's %s can't be mounted to be archived", dep, name)
			}
			prefix := map[string]string{"sqlite": backup.SQLitePrefix, "filesystem": backup.FSPrefix, "blob": backup.BlobPrefix}[typ]
			if err := bw.TreeBeneath(prefix+name+"/", b.resMount(k, false), nil); err != nil { // never follows a link out (§11.6)
				return err
			}
		}
	}
	if len(kv) == 0 {
		return nil
	}
	j, _ := json.Marshal(kv)
	return bw.File(backup.KVName, 0o644, j)
}

// dumpNSKV reads one kv resource of a namespace beyond main as dumpKV does,
// but a value that doesn't decode fails the archive instead of leaving it
// out.
func (b *Broker) dumpNSKV(k resKeys) (map[string]string, error) {
	out := map[string]string{}
	db, err := b.kvDB(k, false)
	if err != nil {
		return nil, err
	}
	return out, kvView(db, func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(k.Bucket))
		if bk == nil {
			return nil
		}
		return bk.ForEach(func(key, v []byte) error {
			pv, err := b.decodeKV(k.KVLabel, append([]byte(nil), v...))
			if err != nil {
				return err
			}
			out[string(key)] = base64.StdEncoding.EncodeToString(pv)
			return nil
		})
	})
}

// ---- small helpers ----

// checkpointStoreDir is tile's checkpoint store (11-contract §10.3).
func checkpointStoreDir(root, tile string) string {
	return filepath.Join(root, "data", "checkpoints", util.TileKey(tile)+".git")
}

// recordBinding reads the tile path and owner ref a record names, by their
// exact keys, as the deployments plane reads them.
func recordBinding(data []byte) (tile, owner string, ok bool) {
	var head map[string]json.RawMessage
	if json.Unmarshal(data, &head) != nil {
		return "", "", false
	}
	if json.Unmarshal(head["tile"], &tile) != nil || tile == "" {
		return "", "", false
	}
	if raw, has := head["owner"]; has && json.Unmarshal(raw, &owner) != nil {
		return "", "", false
	}
	return tile, owner, true
}

// objectID reports whether s is a full git object id: 40 (SHA-1) or 64
// (SHA-256) lowercase hex digits.
func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

var errTooLarge = errors.New("too large")

// readCapped reads r whole, refusing more than limit bytes.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("over %d bytes: %w", limit, errTooLarge)
	}
	return data, nil
}

// readBeneathCapped reads a small xbind-owned file beneath dir, never
// following it out of dir and never blocking on a FIFO.
func readBeneathCapped(dir, name string, limit int64) ([]byte, error) {
	f, err := fsutil.OpenBeneath(dir, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fs.ErrNotExist
	}
	return readCapped(f, limit)
}
