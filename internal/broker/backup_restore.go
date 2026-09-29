package broker

// backup_restore.go — restoring archived data into a data namespace
// (08-data §11.4; 11-contract §1.8): a deployment archive, or a main
// archive's data, into the namespace of a deployment the tile has, checked
// whole before anything is written, and replacing each archived resource
// beyond main (main merges unless asked to replace, as POST /restore does);
// the deployment archives a main archive lists, after it; and the
// registration files a main archive carries, validated row by row.

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// listedRestore is what a main archive's restore did with the deployment
// archives it lists: the deployments restored, and the rest with why.
type listedRestore struct {
	Restored []string `json:"restored"`
	Skipped  []string `json:"skipped"`
}

// restoreListed restores each deployment archive a main archive of comp
// lists, by replace, into the deployment of its name when comp has one
// (08-data §11.4, §11.5 step 5): POST /restore's, or re-enabling an
// offloaded tile's, both an admin's. Every claimant of each namespace stops.
func (b *Broker) restoreListed(comp string, archives map[string]string) *listedRestore {
	out := &listedRestore{Restored: []string{}, Skipped: []string{}}
	for _, dep := range slices.Sorted(maps.Keys(archives)) {
		var err error
		switch {
		case dep == util.MainDeployment || !util.DeploymentNameOK(dep):
			err = errors.New("not a deployment's archive")
		case !b.hasDeployment(comp, dep):
			err = util.NoDeployment(comp, dep)
		default:
			_, _, _, err = b.restoreData(comp, dataRestore{from: dep, version: archives[dep], into: dep, confirmed: true,
				authorize: func(string) error { return nil }, stop: func(t, _ string) { b.StopBackendSafe(t) }})
		}
		if err != nil {
			slog.Warn("restore: a deployment archive the main archive lists isn't restored", "tile", comp, "deployment", dep, "err", err)
			out.Skipped = append(out.Skipped, dep+": "+err.Error())
			continue
		}
		out.Restored = append(out.Restored, dep)
	}
	return out
}

// dataRestore is one restore of an archive's data into a data namespace.
type dataRestore struct {
	from, version, into string // whose archive, which version ("" = the latest), the target deployment
	replace             bool   // main: empty each archived resource first; beyond main always
	confirmed           bool   // confirm:"erase-data" was sent (or the act is POST /restore's)
	dryRun              bool
	by                  string
	authorize           func(tile string) error
	stop                func(tile, dep string)
}

// restoreData restores r.version of r.from's archive of tile into the
// namespace (S, r.into), S the scope tile roots (08-data §11.4). Everything
// is checked before anything is written — the target, every claimant's
// authority, the hold, confirm, the archive's manifest — and then, under
// the namespace's hold with every claimant stopped, each archived resource
// the target declares is replaced (merged into main without r.replace). It
// answers the version restored, the archived resources the target doesn't
// declare, and the claimants.
func (b *Broker) restoreData(tile string, r dataRestore) (string, []string, []string, error) {
	c, err := b.dataTile(tile, r.into, false)
	if err != nil {
		return "", nil, nil, err
	}
	_, names := b.deploymentsOf(tile)
	id := nsOf(c.Path, r.into)
	r.replace = r.replace || !id.main()
	_, isRoot := b.Reg.Scopes()[tile]
	switch {
	case !isRoot:
		return "", nil, nil, notRoot(tile)
	case !util.DeploymentNameOK(r.from):
		return "", nil, nil, nsErr(http.StatusBadRequest, "", badDeploymentName)
	case r.version != "" && !archiveVersion.MatchString(r.version):
		return "", nil, nil, nsErr(http.StatusBadRequest, "", fmt.Sprintf("%q is not an archive version", r.version))
	case !slices.Contains(names, r.into):
		return "", nil, nil, nsErr(http.StatusConflict, deployments.KindState,
			fmt.Sprintf("%s has no deployment %q to restore into: its deployments are %s", tile, r.into, strings.Join(names, ", ")))
	case id.main() && !b.Reg.HoldsScopeKey(c.Path): // D118: main's keys only
		return "", nil, nil, nsErr(http.StatusConflict, deployments.KindPolicy,
			fmt.Sprintf("scope %s doesn't hold its resource data key %q — its data isn't restored", c.Path, scopeDataKey(c.Path)))
	}
	claimants := b.claimants(c.Path, r.into)
	level := map[bool]string{false: "terminal-level access", true: "a tile manager"}[id.main()]
	for _, t := range claimants {
		if t != tile {
			if err := judgeClaimant("restore", level, id, t, r.authorize); err != nil {
				return "", nil, nil, err
			}
		}
	}
	declared, err := b.declaredIn(c.Path, r.into)
	if err != nil {
		slog.Warn("restore: resources the target doesn't take", "tile", tile, "deployment", r.into, "err", err)
	}
	provider := b.archiveProvider(tile)
	switch act := b.busyAct(id); {
	case act != "":
		return "", nil, nil, nsBusy(r.into, act)
	case b.vaultSealed():
		return "", nil, nil, nsErr(http.StatusConflict, deployments.KindState, "vault sealed — unseal before restoring encrypted resources")
	case !r.confirmed && b.nsHasData(id, declared):
		return "", nil, nil, nsErr(http.StatusBadRequest, "", fmt.Sprintf("restoring into %s overwrites the data it holds: send confirm:%q to proceed",
			r.into, deployments.ConfirmEraseData))
	case provider == "":
		return "", nil, nil, noArchiver(tile)
	}
	key := archiveKey(tile, r.from)
	if r.version == "" {
		list, err := b.DeploymentBackups(tile, r.from)
		switch {
		case err != nil:
			return "", nil, nil, err
		case len(list.Versions) == 0:
			return "", nil, nil, nsErr(http.StatusNotFound, "", fmt.Sprintf("%s has no archive of %s's data", tile, r.from))
		}
		var v struct{ Version string }
		if json.Unmarshal(list.Versions[0], &v) != nil || !archiveVersion.MatchString(v.Version) {
			return "", nil, nil, nsErr(http.StatusBadGateway, "", "archiver: a version this xbind can't name")
		}
		r.version = v.Version
	}
	if r.dryRun {
		return r.version, []string{}, claimants, nil
	}
	code, body, err := b.archiveDo("GET", provider, "/archive/"+key+"/versions/"+r.version, nil)
	switch {
	case err != nil:
		return "", nil, nil, nsErr(http.StatusBadGateway, "", err.Error())
	case code == http.StatusNotFound:
		return "", nil, nil, nsErr(http.StatusNotFound, "", fmt.Sprintf("%s has no archive %s of %s's data", tile, r.version, r.from))
	case code >= 400:
		return "", nil, nil, nsErr(http.StatusBadGateway, "", fmt.Sprintf("archiver %s: %s", provider, firstLine(string(body))))
	}
	br, err := b.openArchive(body) // a sealed archive is authenticated whole first
	if err == nil && r.from == util.MainDeployment && br.M.Data != nil && !br.M.DataArchive() {
		// a split main archive: main's data is in the data archive it names
		if err = archivedDataOK(br.M, tile, r.from); err == nil {
			var gone error
			if br, gone, err = b.openDataArchive(provider, tile, br.M); gone != nil {
				err = gone
			}
		}
	}
	if err == nil {
		err = archivedDataOK(br.M, tile, r.from)
	}
	if err != nil {
		return "", nil, nil, nsErr(http.StatusConflict, deployments.KindState, "the archive can't be restored: "+err.Error()+"; nothing was restored")
	}
	release, err := b.holdNS(id, nsRestoring)
	if err != nil {
		return "", nil, nil, err
	}
	defer release()
	defer b.markBusy(id, nsRestoring, true)()
	for _, t := range claimants {
		if r.stop != nil {
			r.stop(t, r.into) // every claimant's deployment of that name addresses the namespace
		}
	}
	n := &nsRestorer{b: b, scope: c.Path, dep: r.into, replace: r.replace, declared: declared,
		dirs: map[string]string{}, trees: map[string]*destTree{}, skipped: map[string]bool{}}
	step, err := n.run(br)
	n.close()
	now := nowStamp(time.Now())
	if err != nil {
		_ = b.updateNS(id, true, func(m *nsMeta) {
			m.State, m.Failed, m.Step, m.Error, m.At, m.By = nsPartial, "restore", step, err.Error(), now, r.by
			m.History = append(m.History, nsEvent{Op: "partial", At: now, By: r.by, Error: err.Error()})
		})
		return "", nil, nil, fmt.Errorf("restoring %s's archive into %s failed at %s (its data is partial: restore or reset it again): %w", r.from, r.into, step, err)
	}
	skipped := slices.Sorted(maps.Keys(n.skipped))
	if err := b.updateNS(id, true, func(m *nsMeta) {
		*m = nsMeta{State: nsRestored, From: r.from, At: now, By: r.by, Skipped: skipped,
			History: append(m.History, nsEvent{Op: "restore", At: now, By: r.by})}
	}); err != nil {
		return "", nil, nil, err
	}
	if id.main() {
		b.MountEncrypted() // main's volumes a replace dropped mount again, as at provision
	}
	slog.Info("deployment data restored", "tile", tile, "from", r.from, "version", r.version, "into", r.into, "by", r.by, "skipped", skipped)
	return r.version, skipped, claimants, nil
}

// archivedDataOK checks an archive's manifest before anything is written
// (05-model §11; 06-security T11.5): it must be tile's, hold data of the
// scope tile roots, and be from's — a main archive (or the data archive a
// split one names) for main, else from's deployment archive at a schema
// this xbind reads.
func archivedDataOK(m backup.Manifest, tile, from string) error {
	switch {
	case m.Component != tile:
		return fmt.Errorf("it belongs to %s, not %s", m.Component, tile)
	case !m.ScopeRoot || m.Scope != tile:
		return errors.New("it holds no data of the scope " + tile + " roots")
	case from == util.MainDeployment && m.DeploymentArchive():
		return fmt.Errorf("it is deployment %q's, not the main archive", m.Deployment)
	case from != util.MainDeployment && m.DataArchive():
		return fmt.Errorf("it is main's data archive, not %s's deployment archive", from)
	case from != util.MainDeployment && (m.Schema != backup.SchemaDeployment || m.Deployment != from):
		return fmt.Errorf("it isn't %s's deployment archive (schema %d, deployment %q)", from, m.Schema, m.Deployment)
	}
	return nil
}

// nsHasData reports whether namespace id holds data a restore overwrites:
// beyond main anything besides ns.json; main's, a volume or a kv bucket of
// a resource declared.
func (b *Broker) nsHasData(id nsID, declared map[string]registry.Resource) bool {
	if !id.main() {
		dir, err := b.nsDir(id)
		ents, _ := os.ReadDir(dir) // walk-ok: data/ is xbind's own; no sandbox sees it
		return err == nil && slices.ContainsFunc(ents, func(e fs.DirEntry) bool { return e.Name() != nsMetaFile })
	}
	db, _ := b.scopeKV(id.scope, util.MainDeployment, false)
	has := false
	for name, res := range declared {
		k, err := b.resKeys(resTarget{Scope: id.scope, Name: name}, util.MainDeployment)
		switch {
		case err != nil:
		case fileBackedType(res.Type):
			has = has || b.resenc.Encrypted(k.DirKey, k.Name)
		case res.Type == "kv":
			_ = kvView(db, func(tx *bolt.Tx) error {
				bk := tx.Bucket([]byte(k.Bucket))
				has = has || bk != nil && bk.Stats().KeyN > 0
				return nil
			})
		}
	}
	return has
}

// nsRestorer writes one archive's data into one namespace: each archived
// resource the target declares with the same type, re-encrypted under the
// target's labels; with replace, each emptied first.
type nsRestorer struct {
	b          *Broker
	scope, dep string
	replace    bool
	declared   map[string]registry.Resource
	dirs       map[string]string    // a volume's mount, once readied
	trees      map[string]*destTree // … and the os.Root its files are written through
	skipped    map[string]bool
}

// close releases the volumes' roots.
func (n *nsRestorer) close() {
	for _, t := range n.trees {
		t.r.Close()
	}
}

// run reads the archive's data members; others are never a namespace's.
// It answers the step it was at.
func (n *nsRestorer) run(br *backup.Reader) (string, error) {
	for {
		name, rd, err := br.Next()
		switch {
		case err == io.EOF:
			return "the end", n.emptyUnwritten(br.M.Resources)
		case err != nil:
			return "reading the archive", err
		case name == backup.KVName:
			err = n.kv(rd)
		case strings.HasPrefix(name, backup.SQLitePrefix):
			err = n.file("sqlite", strings.TrimPrefix(name, backup.SQLitePrefix), br.Perm(), rd)
		case strings.HasPrefix(name, backup.FSPrefix):
			err = n.file("filesystem", strings.TrimPrefix(name, backup.FSPrefix), br.Perm(), rd)
		case strings.HasPrefix(name, backup.BlobPrefix):
			err = n.file("blob", strings.TrimPrefix(name, backup.BlobPrefix), br.Perm(), rd)
		}
		if err != nil {
			return name, err
		}
	}
}

// key is the target's keys of an archived resource, or false (skipped) when
// the target doesn't declare it with that type.
func (n *nsRestorer) key(name, typ string) (resKeys, bool) {
	k, err := n.b.resKeys(resTarget{Scope: n.scope, Name: name}, n.dep)
	if res, ok := n.declared[name]; !ok || res.Type != typ || err != nil || !registry.ValidResourceName(name) {
		n.skipped[name] = true
		return resKeys{}, false
	}
	return k, true
}

func (n *nsRestorer) kv(rd io.Reader) error {
	var dump map[string]map[string]string
	if err := json.NewDecoder(rd).Decode(&dump); err != nil {
		return err
	}
	keys := map[string]resKeys{}
	for name := range dump {
		if k, ok := n.key(name, "kv"); ok {
			keys[name] = k
		}
	}
	if len(keys) == 0 {
		return nil
	}
	db, err := n.b.scopeKV(n.scope, n.dep, true)
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		for name, k := range keys {
			if n.replace {
				if err := tx.DeleteBucket([]byte(k.Bucket)); err != nil && !errors.Is(err, bolt.ErrBucketNotFound) {
					return err
				}
			}
			bk, err := tx.CreateBucketIfNotExists([]byte(k.Bucket))
			if err != nil {
				return err
			}
			for key, v64 := range dump[name] {
				v, err := base64.StdEncoding.DecodeString(v64)
				if err != nil {
					return err
				}
				stored, err := n.b.encodeKV(k.KVLabel, v) // the target's label: plaintext in the tar
				if err != nil {
					return err
				}
				if err := bk.Put([]byte(key), stored); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// file writes one archived file of a volume, with the bits it was archived
// with, through an os.Root at the volume's top (restore.go's destTree): what
// a sandbox left in the volume — a symlink where a directory or the file
// goes — is replaced, never followed (WP-9). A fresh volume, or main merged
// as POST /restore does.
func (n *nsRestorer) file(typ, rest string, perm fs.FileMode, rd io.Reader) error {
	name, sub, _ := strings.Cut(rest, "/")
	t, ok := n.trees[name]
	if !ok {
		k, ok := n.key(name, typ)
		if !ok {
			return nil
		}
		dir, err := n.b.readyVolume(k, n.scope, typ, n.replace)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		r, err := os.OpenRoot(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		t = newDestTree(r)
		n.dirs[name], n.trees[name] = dir, t
	}
	rel := cleanRel(sub)
	if rel == "" {
		return nil // a volume's root is never a file
	}
	return t.write(rel, perm, rd)
}

// emptyUnwritten empties, to replace, each volume the archive lists with no
// file in it.
func (n *nsRestorer) emptyUnwritten(archived map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(archived)) {
		typ := archived[name]
		if _, done := n.dirs[name]; done || !fileBackedType(typ) && typ != "kv" {
			continue
		}
		k, ok := n.key(name, typ)
		if ok && n.replace && typ != "kv" {
			if err := n.b.dropVolume(k); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

// readyVolume mounts k's volume for a restore, initializing it when new;
// with replace it is unmounted (checked) and removed first, so it holds
// exactly what the archive does.
func (b *Broker) readyVolume(k resKeys, scope, typ string, replace bool) (string, error) {
	if replace {
		if err := b.dropVolume(k); err != nil {
			return "", err
		}
	}
	return b.resenc.Ensure(k.FSLabel, k.DirKey, k.Name, b.resSingleTenant(scope, typ))
}

// dropVolume removes k's volume, ciphertext and mount point, once it is
// unmounted: nothing while it is still mounted.
func (b *Broker) dropVolume(k resKeys) error {
	if err := b.unmountUnder(b.resenc.MountDir(k.DirKey, k.Name), k.DirKey); err != nil {
		return err
	}
	return errors.Join(os.RemoveAll(b.resenc.CipherDir(k.DirKey, k.Name)), os.RemoveAll(b.resenc.MountDir(k.DirKey, k.Name)))
}

// ---- a main archive's registration files ----

// restoreRegistrations merges the registration files a main archive carries
// into the deployments of tile that exist once its record is settled
// (08-data §11.4; 06-security T11.5). Each row is checked again as its
// route checks it — its shape, its resource's type, the tile's authority
// and the deployment's edge — and registers for tile and the file's
// deployment whatever it says; a row replaces the one of its name. Rows
// that fail, files of another schema or that this xbind doesn't read, and
// deployments tile doesn't have are left out, each with a warning.
func (b *Broker) restoreRegistrations(tile string, regs []regFile) {
	for _, r := range regs {
		err := errors.New("this xbind doesn't restore it")
		switch {
		case !b.hasDeployment(tile, r.dep):
			err = util.NoDeployment(tile, r.dep)
		case r.file == depCronFile && b.cron != nil:
			err = b.restoreDepCron(tile, r.dep, r.data)
		case r.file == depBusFile:
			err = b.restoreDepBus(tile, r.dep, r.data)
		case r.file == depBackupFile && b.cron != nil:
			err = b.restoreDepBackupSchedule(tile, r.dep, r.data)
		}
		if err != nil {
			slog.Warn("restore: a registration file is left out", "tile", tile, "deployment", r.dep, "file", r.file, "err", err)
		}
	}
}

// decodeRegFile decodes an archived registration file of this xbind's
// schema into doc.
func decodeRegFile(data []byte, doc any) error {
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	if head.Schema != depFileSchema {
		return fmt.Errorf("schema %d, which this xbind doesn't read", head.Schema)
	}
	return json.Unmarshal(data, doc)
}

// restoredRow checks one archived row on resource res, of type typ, at
// want: answering the resource's id.
func (b *Broker) restoredRow(tile, dep, res, typ, want string, check error) (string, error) {
	rt, r, ok := b.parseRes(res)
	switch {
	case check != nil:
		return "", check
	case !ok || r == nil || r.Type != typ:
		return "", fmt.Errorf("no such %s resource %s", typ, res)
	}
	return rt.String(), b.depResAllowed(tile, dep, rt, want)
}

func (b *Broker) restoreDepCron(tile, dep string, data []byte) error {
	var doc depCronDoc
	if err := decodeRegFile(data, &doc); err != nil {
		return err
	}
	var rows []depCronRow
	for _, row := range doc.Jobs {
		row.Role = cmp.Or(row.Role, "writer")
		res, err := b.restoredRow(tile, dep, row.Resource, "cron", "writer", row.check())
		if err != nil {
			slog.Warn("restore: a cron job is left out", "tile", tile, "deployment", dep, "job", row.Name, "err", err)
			continue
		}
		row.Resource = res
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	return b.cron.rewriteDep(tile, dep, func(cur []depCronRow) ([]depCronRow, error) {
		for _, row := range rows {
			cur = upsertRow(cur, row)
		}
		return cur, nil
	})
}

func (b *Broker) restoreDepBus(tile, dep string, data []byte) error {
	var doc depBusDoc
	if err := decodeRegFile(data, &doc); err != nil {
		return err
	}
	var rows []depBusRow
	for _, row := range doc.Subscriptions {
		row.Role = cmp.Or(row.Role, "writer")
		res, err := b.restoredRow(tile, dep, row.Resource, "bus", "reader", row.check())
		if err != nil {
			slog.Warn("restore: a bus subscription is left out", "tile", tile, "deployment", dep, "name", row.Name, "err", err)
			continue
		}
		row.Resource = res
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	return b.bus.rewriteDep(tile, dep, func(cur []depBusRow) ([]depBusRow, error) {
		for _, row := range rows {
			if !slices.ContainsFunc(cur, func(r depBusRow) bool { return r.Name == row.Name }) && len(cur) >= busSubsPerComp {
				slog.Warn("restore: a bus subscription over the limit is left out", "tile", tile, "deployment", dep, "name", row.Name)
				continue
			}
			cur = upsertRow(cur, row)
		}
		return cur, nil
	})
}
