package broker

// backup_partition_restore.go — restoring a person's partition from its
// archive, and the restores a partition mode switch guards
// (plans/partitions/11-backup-encryption.md §4 "Restore rules"; PD-25,
// PD-43, PD-56):
//
//	GET  /api/xbin/partitions/backups?tile=<t>[&user=<id>][&partitionId=u-…]
//	POST /api/xbin/partitions/restore {tile, user?, partitionId?, version?, confirm, to?, dryRun?}
//
// A partition archive restores only into the partition of the person it
// is — the same tile, the same user id — and only while the tile is
// partitioned now (409 otherwise). The same incarnation (the uid the
// archive names is the person's now) restores after a typed confirmation,
// by the person in their own session or by an admin. An archive of an
// earlier holder of the id (deleted and recreated since) restores only by
// an admin who names the id again (to), audited, and the person is told.
// Never into another id. Only a sealed archive restores into a partition,
// sealed under that partition's own backup key: an archiver can neither
// forge one nor serve one person's archive as another's.
//
// The restore replaces the partition: its namespace's data (each resource
// the archive holds, its volumes emptied first), its vault and its
// registrations; its record stays the person's current one. Its instance
// is stopped first and its namespace held meanwhile, so nothing starts or
// writes into it until the restore is done.
//
// Main archives never restore into partitions (they hold none of them,
// S15), and an archive made before the tile's last partition mode switch
// that deleted data restores only after a typed confirmation naming the
// switch (preSwitchRestore) — into the global instance's namespace, at
// today's keys, as every main archive does.

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

const partitionBackupDocs = "/docs/partitions.md"

func (b *Broker) registerPartitionBackups(srv *server.Server) {
	srv.RegisterAPI("GET /partitions/backups", b.apiPartitionBackups)
	srv.RegisterAPI("POST /partitions/restore", b.apiPartitionRestore)
}

// partitionBackupActor is the person p acts as on a partition's backups: a
// person's own session, app or device, the root token, or the admin tile's
// frame under their login (AdminFrameDriver). No tile's backend, frame,
// terminal or agent session — a person's partition's code included — lists
// or restores a partition, and view-as never does. ok false: the refusal
// is written.
func (b *Broker) partitionBackupActor(w http.ResponseWriter, p auth.Principal) (auth.Principal, bool) {
	switch {
	case p.Impersonator != "":
	case p.Component == "" && (p.Owner || p.UserID != ""):
		return p, true
	case p.Component != "":
		if d, ok := b.AdminFrameDriver(p); ok {
			return d, true
		}
	}
	server.WriteError(w, http.StatusForbidden, "a partition's backups are a person's own act — the person in their own session (bx, the shell), or an admin; no tile's backend, frame, terminal or agent lists or restores them", partitionBackupDocs)
	return p, false
}

// partitionBackupTarget is whose partition of which tile a request names,
// judged: the person (the caller unless an admin names another), their
// current partition id and uid, the archive's partition id, and the
// deployment people's partitions run in (the primary).
type partitionBackupTarget struct {
	c                   *registry.Component
	dep                 string
	user, uid, pkeyNow  string
	pkeyFrom            string
	admin               bool
	by                  string
	partitioned, isRoot bool
}

// A listing (mint false) never mints the person's uid: without one they
// have no partition now, and only an earlier holder's (partitionId) lists.
func (b *Broker) partitionBackupTargetOf(w http.ResponseWriter, actor auth.Principal, tile, user, pid string, mint bool) (partitionBackupTarget, bool) {
	t := partitionBackupTarget{admin: b.IsAdmin(actor), by: deciderName(actor)}
	tile = strings.Trim(tile, "/")
	user = cmp.Or(user, actor.UserID)
	c, ok := b.Reg.Component(tile)
	switch {
	case tile == "":
		server.WriteError(w, http.StatusBadRequest, "need tile", "/docs/protocol.md")
		return t, false
	case user == "":
		server.WriteError(w, http.StatusBadRequest, "name the person whose partition it is (user)", "/docs/protocol.md")
		return t, false
	case !t.admin && user != actor.UserID:
		server.WriteError(w, http.StatusForbidden, "a person lists and restores only their own partition's backups; an admin may any person's", partitionBackupDocs)
		return t, false
	case !ok || !t.admin && !actor.CanReadTile(tile): // a person who can't read it isn't told it exists
		server.WriteError(w, http.StatusNotFound, "no such tile: "+tile)
		return t, false
	case b.Users == nil:
		server.WriteError(w, http.StatusConflict, "this workspace has no people: no partitions", partitionBackupDocs)
		return t, false
	}
	if _, ok := b.Users.Get(user); !ok {
		server.WriteError(w, http.StatusNotFound, "no such person: "+user)
		return t, false
	}
	pkey, uid := "", partitionUIDSeam(b, user)
	if uid != "" {
		pkey = util.PartitionKey(user, uid)
	} else if mint {
		var err error
		if pkey, uid, err = b.partitionKeyOf(user); err != nil {
			server.WriteError(w, http.StatusConflict, user+"'s partition: "+err.Error(), partitionBackupDocs)
			return t, false
		}
	}
	if pid != "" && !pkeyOK(pid) {
		server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a partition id (u- and 32 hex digits)", pid), partitionBackupDocs)
		return t, false
	}
	spec, on := c.Partitioned()
	t.c, t.dep, t.user, t.uid, t.pkeyNow, t.pkeyFrom = c, cmp.Or(b.primaryOf(tile), util.MainDeployment), user, uid, pkey, cmp.Or(pid, pkey)
	t.partitioned, t.isRoot = on && spec.User, c.Scope == c.Path
	return t, true
}

// apiPartitionBackups is GET /partitions/backups: the archiver's versions
// of a person's partition's archive of the tile (the person's own, or any
// for an admin).
func (b *Broker) apiPartitionBackups(w http.ResponseWriter, r *http.Request) {
	actor, ok := b.partitionBackupActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	q := r.URL.Query()
	t, ok := b.partitionBackupTargetOf(w, actor, q.Get("tile"), q.Get("user"), q.Get("partitionId"), false)
	if !ok {
		return
	}
	out := map[string]any{"tile": t.c.Path, "partition": string(util.UserPartition(t.user)), "partitionId": t.pkeyFrom,
		"deployment": t.dep, "partitioned": t.partitioned, "versions": []any{}, "archiver": b.archiveProvider(t.c.Path)}
	provider := b.archiveProvider(t.c.Path)
	if provider == "" || t.pkeyFrom == "" { // no archiver, or a person with no partition yet
		server.WriteJSON(w, http.StatusOK, out)
		return
	}
	code, body, err := b.archiveDo("GET", provider, "/archive/"+partitionArchiveKey(t.c.Path, t.dep, t.pkeyFrom)+"/versions", nil)
	var list struct {
		Versions []json.RawMessage `json:"versions"`
	}
	if err != nil || code >= 400 || json.Unmarshal(body, &list) != nil {
		server.WriteError(w, http.StatusBadGateway, "archiver: "+firstLine(string(body)))
		return
	}
	if list.Versions != nil {
		out["versions"] = list.Versions
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// partitionRestoreBody is POST /partitions/restore's body.
type partitionRestoreBody struct {
	Tile        string `json:"tile"`
	User        string `json:"user,omitempty"`        // whose partition: default the caller
	PartitionID string `json:"partitionId,omitempty"` // the archive's partition id: default the person's current one
	Version     string `json:"version,omitempty"`     // default the latest
	Confirm     string `json:"confirm,omitempty"`     // "<tile> user:<id>", typed
	To          string `json:"to,omitempty"`          // an earlier holder's archive: the id again (admin)
	DryRun      bool   `json:"dryRun,omitempty"`
}

// apiPartitionRestore is POST /partitions/restore.
func (b *Broker) apiPartitionRestore(w http.ResponseWriter, r *http.Request) {
	actor, ok := b.partitionBackupActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	var body partitionRestoreBody
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {tile, user?, partitionId?, version?, confirm, to?, dryRun?}: "+err.Error(), "/docs/protocol.md")
		return
	}
	t, ok := b.partitionBackupTargetOf(w, actor, body.Tile, body.User, body.PartitionID, !body.DryRun)
	if !ok {
		return
	}
	part := string(util.UserPartition(t.user))
	switch {
	case !t.partitioned:
		server.WriteError(w, http.StatusConflict, t.c.Path+" isn't partitioned now: a person's partition archive restores only into a tile that keeps people's partitions", partitionBackupDocs)
		return
	case body.To != "" && body.To != t.user:
		server.WriteError(w, http.StatusBadRequest, "a partition archive restores only into the partition of the person it is ("+part+"), never into another id", partitionBackupDocs)
		return
	case body.Version != "" && !archiveVersion.MatchString(body.Version):
		server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("%q is not an archive version", body.Version))
		return
	case t.pkeyFrom == "": // a dry run for a person with no partition yet
		server.WriteError(w, http.StatusNotFound, t.user+" has no partition of "+t.c.Path+" yet, and names no earlier holder's (partitionId): no backup of it", partitionBackupDocs)
		return
	}
	provider := b.archiveProvider(t.c.Path)
	if provider == "" {
		server.WriteError(w, http.StatusBadGateway, noArchiver(t.c.Path).Error())
		return
	}
	key := partitionArchiveKey(t.c.Path, t.dep, t.pkeyFrom)
	version := body.Version
	if version == "" {
		v, status, err := b.latestVersion(provider, key)
		if err != nil {
			server.WriteError(w, status, err.Error(), partitionBackupDocs)
			return
		}
		version = v
	}
	raw, err := b.fetchArchive(provider, key, version)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errArchiveGone) {
			status = http.StatusNotFound
		}
		server.WriteError(w, status, err.Error(), partitionBackupDocs)
		return
	}
	m, err := b.openPartitionArchive(raw, t)
	if err != nil {
		server.WriteError(w, http.StatusConflict, "the archive can't be restored: "+err.Error()+"; nothing was restored", partitionBackupDocs)
		return
	}
	earlier := m.Partition.UID != t.uid // the id was deleted and recreated since this backup
	want := t.c.Path + " " + part
	switch {
	case m.Partition.User != t.user:
		server.WriteError(w, http.StatusForbidden, "this archive is user:"+m.Partition.User+"'s partition: it restores only into theirs, never into another id", partitionBackupDocs)
		return
	case earlier && !t.admin:
		server.WriteError(w, http.StatusForbidden, "this archive is of an earlier holder of the id "+t.user+" (deleted and recreated since): only an admin restores it, naming the id again (to)", partitionBackupDocs)
		return
	case earlier && body.To != t.user:
		server.WriteError(w, http.StatusBadRequest, "this archive is of an earlier holder of the id "+t.user+" (deleted and recreated since): restore it into "+part+" only with to: \""+t.user+"\" — audited, and the person is told", partitionBackupDocs)
		return
	case !body.DryRun && body.Confirm != want:
		server.WriteJSON(w, http.StatusBadRequest, map[string]any{"docs": partitionBackupDocs, "confirm": want,
			"error": "restoring replaces " + part + "'s partition of " + t.c.Path + " — its data, vault and registrations — with this backup's: confirm by typing \"" + want + "\""})
		return
	}
	out := map[string]any{"tile": t.c.Path, "partition": part, "partitionId": t.pkeyNow, "from": t.pkeyFrom, "version": version,
		"resources": slices.Sorted(maps.Keys(m.Resources)), "earlierHolder": earlier}
	if body.DryRun {
		out["dryRun"] = true
		server.WriteJSON(w, http.StatusOK, out)
		return
	}
	done, err := b.restorePartition(t, raw)
	if err != nil {
		var se statusErr
		if errors.As(err, &se) {
			server.WriteError(w, se.code, se.msg, partitionBackupDocs)
			return
		}
		server.WriteError(w, http.StatusInternalServerError, err.Error(), partitionBackupDocs)
		return
	}
	b.afterPartitionRestore(t, version, earlier)
	out["ok"] = true
	for k, v := range done.answer() {
		out[k] = v
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// latestVersion is the newest version the archiver lists under key.
func (b *Broker) latestVersion(provider, key string) (string, int, error) {
	code, body, err := b.archiveDo("GET", provider, "/archive/"+key+"/versions", nil)
	var list struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	switch {
	case err != nil || code >= 400 || json.Unmarshal(body, &list) != nil:
		return "", http.StatusBadGateway, fmt.Errorf("archiver: %s", firstLine(string(body)))
	case len(list.Versions) == 0:
		return "", http.StatusNotFound, errors.New("no backup of this partition")
	case !archiveVersion.MatchString(list.Versions[0].Version):
		return "", http.StatusBadGateway, errors.New("archiver: a version this xbind can't name")
	}
	return list.Versions[0].Version, 0, nil
}

// openPartitionArchive authenticates a partition archive whole and checks
// it is t's: sealed (a partition's archive is never plain), under the
// backup key of t's archive's own subject — so neither a forged archive
// nor another partition's (or tile's) served under its key restores — and
// a partition manifest naming the tile, the deployment and a person whose
// id and uid hash to its partition id. An erased key refuses with when and
// why; an unknown one names the import.
func (b *Broker) openPartitionArchive(raw []byte, t partitionBackupTarget) (backup.Manifest, error) {
	h, sealed, err := backup.ReadSealHeader(bytes.NewReader(raw))
	switch {
	case err != nil:
		return backup.Manifest{}, err
	case !sealed:
		return backup.Manifest{}, errors.New("it isn't sealed, and a person's partition archive always is: it isn't xbind's")
	}
	br, err := b.openArchive(raw)
	if err != nil {
		return backup.Manifest{}, err
	}
	k, err := b.backupKeys().read(h.Subkey)
	if err != nil || k.Subject != partitionBackupSubject(t.c.Path, t.dep, t.pkeyFrom) {
		return backup.Manifest{}, errors.New("it isn't sealed under this partition's backup key: another partition's or tile's")
	}
	m := br.M
	switch pr := m.Partition; {
	case !m.PartitionArchive() || pr == nil:
		return m, errors.New("it isn't a partition archive")
	case m.Component != t.c.Path:
		return m, fmt.Errorf("it is %s's, not %s's", m.Component, t.c.Path)
	case pr.ID != t.pkeyFrom || pr.Deployment != t.dep || util.PartitionKey(pr.User, pr.UID) != pr.ID:
		return m, errors.New("it names another partition than its key")
	}
	return m, nil
}

// partitionRestored is what a partition restore wrote.
type partitionRestored struct {
	data          bool
	skipped       []string // archived resources the tile doesn't keep per person now
	vault         bool
	vaultSkipped  string
	registrations []string
}

func (d partitionRestored) answer() map[string]any {
	out := map[string]any{"data": d.data, "vault": d.vault, "registrations": append([]string{}, d.registrations...)}
	if len(d.skipped) > 0 {
		out["skipped"] = d.skipped
	}
	if d.vaultSkipped != "" {
		out["vaultSkipped"] = d.vaultSkipped
	}
	return out
}

// archivedRecords are a partition archive's records, read (and checked)
// before anything is written.
type archivedRecords struct {
	vault *partVaultDoc
	regs  map[string][]byte // registration file → its bytes
}

// partitionArchiveRecords reads raw's partition/ members: the vault file
// and the registration files, each checked to be the partition's.
func (b *Broker) partitionArchiveRecords(raw []byte, t partitionBackupTarget) (archivedRecords, error) {
	out := archivedRecords{regs: map[string][]byte{}}
	br, err := b.openArchive(raw)
	if err != nil {
		return out, err
	}
	part := string(util.UserPartition(t.user))
	for {
		name, rd, err := br.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		file, isReg := strings.CutPrefix(name, backup.PartRegsPrefix)
		if name != backup.PartVaultName && (!isReg || !slices.Contains(partRegistrationFiles[:], file)) {
			continue
		}
		data, err := readCapped(rd, maxPartitionRecord)
		if err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		if !isReg {
			var doc partVaultDoc
			if err := json.Unmarshal(data, &doc); err != nil || doc.Tile != t.c.Path || doc.Deployment != t.dep || doc.Partition != part {
				return out, errors.New("its vault file isn't this partition's")
			}
			out.vault = &doc
			continue
		}
		var head partRegDoc
		if err := json.Unmarshal(data, &head); err != nil || head.Schema != depFileSchema || head.Tile != t.c.Path || head.Partition != part {
			return out, fmt.Errorf("its %s isn't this partition's (or of a schema this xbind doesn't read)", file)
		}
		out.regs[file] = data
	}
}

// restorePartition replaces t's person's partition with the archive raw
// (checked by openPartitionArchive): its instance stopped and its namespace
// held first; then the data, the vault and the registrations. The records
// are read and checked before anything is written.
func (b *Broker) restorePartition(t partitionBackupTarget, raw []byte) (partitionRestored, error) {
	var done partitionRestored
	pt := partTarget{tile: t.c.Path, dep: t.dep, pkey: t.pkeyNow, part: util.UserPartition(t.user), user: t.user, uid: t.uid}
	switch {
	case b.vaultSealed():
		return done, statusErr{http.StatusConflict, "vault sealed — unseal before restoring a partition"}
	}
	if err := b.partPausedErr(pt); err != nil {
		return done, err
	}
	recs, err := b.partitionArchiveRecords(raw, t)
	if err != nil {
		return done, statusErr{http.StatusConflict, "the archive can't be restored: " + err.Error() + "; nothing was restored"}
	}
	// the tile's backup lock: no backup archives the partition half
	// restored, and no erase of its key runs meanwhile (the sweep's order:
	// the backup lock, then the namespace's hold)
	defer b.holdBackups(t.c.Path)()
	if err := b.notePartition(pt, false); err != nil { // whose it is: the person's current record
		return done, err
	}
	id := partNS(t.c.Scope, t.dep, t.pkeyNow)
	declared := map[string]registry.Resource{}
	if t.isRoot {
		declared = b.partitionResources(t.c.Scope, t.dep)
		if len(declared) > 0 {
			if err := b.notePartitionNS(id, nsPartition{User: t.user, UID: t.uid}); err != nil {
				return done, err
			}
		}
		release, err := b.holdNS(id, nsRestoring) // nothing starts in it or writes it meanwhile
		if err != nil {
			return done, statusErr{http.StatusConflict, err.Error()}
		}
		defer release()
	}
	b.stopPartitionInstance(t.c.Path, t.dep, string(pt.part))
	if t.isRoot && len(declared) > 0 {
		if err := b.restorePartitionData(t, id, raw, declared, &done); err != nil {
			return done, err
		}
	}
	if err := b.restorePartitionVault(pt, recs.vault, &done); err != nil {
		return done, err
	}
	if err := b.restorePartitionRegs(pt, recs.regs, &done); err != nil {
		return done, err
	}
	return done, nil
}

// restorePartitionData writes the archive's data into the partition's
// namespace id, replacing each resource it holds (nsRestorer, in the
// partition's keys); a failure leaves the namespace partial, as a failed
// restore of any namespace does.
func (b *Broker) restorePartitionData(t partitionBackupTarget, id nsID, raw []byte, declared map[string]registry.Resource, done *partitionRestored) error {
	br, err := b.openArchive(raw)
	if err != nil {
		return err
	}
	defer b.markBusy(id, nsRestoring, true)()
	n := &nsRestorer{b: b, scope: t.c.Scope, dep: t.dep, pkey: t.pkeyNow, replace: true, declared: declared,
		dirs: map[string]string{}, trees: map[string]*destTree{}, skipped: map[string]bool{}}
	step, err := n.run(br)
	n.close()
	now := nowStamp(time.Now())
	if err != nil {
		_ = b.updateNS(id, true, func(m *nsMeta) {
			m.State, m.Failed, m.Step, m.Error, m.At, m.By = nsPartial, "restore", step, err.Error(), now, t.by
			m.History = append(m.History, nsEvent{Op: "partial", At: now, By: t.by, Error: err.Error()})
		})
		return fmt.Errorf("restoring %s's partition failed at %s (its data is partial: restore it again): %w", t.user, step, err)
	}
	done.data, done.skipped = true, slices.Sorted(maps.Keys(n.skipped))
	return b.updateNS(id, true, func(m *nsMeta) {
		m.State, m.From, m.At, m.By, m.Skipped, m.Failed, m.Step, m.Error = nsRestored, "backup", now, t.by, done.skipped, "", "", ""
		m.History = append(m.History, nsEvent{Op: "restore", At: now, By: t.by})
	})
}

// restorePartitionVault replaces the partition's vault with the archive's
// (none: emptied), re-sealed under this vault. A vault file another
// workspace sealed (its archive's keys imported) can't be opened here: it
// is left out, and the answer says so.
func (b *Broker) restorePartitionVault(pt partTarget, doc *partVaultDoc, done *partitionRestored) error {
	vals := map[string]string{}
	if doc != nil {
		m, err := b.openPartVault(*doc)
		if err != nil {
			done.vaultSkipped = "the archive's vault was sealed by another vault (another workspace's?): its keys aren't restored"
			slog.Warn("partition restore: the archived vault can't be opened here; left out", "tile", pt.tile, "partition", pt.pkey, "err", err)
			return nil
		}
		vals = m
	}
	if err := b.partVaultWrite(pt, vals); err != nil {
		return err
	}
	done.vault = doc != nil
	return nil
}

// restorePartitionRegs replaces the partition's registrations with the
// archive's, under every lock their writes take: the rows the broker holds
// dropped, the files rewritten as the partition's now (its current
// partition id), and its jobs and subscriptions held again — each row
// checked as a registration is, and never more than a partition may hold.
func (b *Broker) restorePartitionRegs(pt partTarget, regs map[string][]byte, done *partitionRestored) error {
	unlock := b.lockPartFiles()
	defer unlock()
	if err := b.partPausedErr(pt); err != nil {
		return err
	}
	b.dropPartRows(pt.tile, pt.dep, pt.pkey)
	var cronRows []depCronRow
	var busRows []depBusRow
	for _, file := range partRegistrationFiles {
		var doc any
		empty := true
		switch data := regs[file]; file {
		case depCronFile:
			var d partCronDoc
			_ = json.Unmarshal(data, &d)
			for _, row := range d.Jobs {
				if len(cronRows) < partCronCap && row.check() == nil && partScheduleOK(row.Schedule) == nil {
					cronRows = append(cronRows, row)
				}
			}
			doc, empty = partCronDoc{pt.head(), cronRows}, len(cronRows) == 0
		case depBusFile:
			var d partBusDoc
			_ = json.Unmarshal(data, &d)
			for _, row := range d.Subscriptions {
				if len(busRows) < partBusCap && row.check() == nil {
					busRows = append(busRows, row)
				}
			}
			doc, empty = partBusDoc{pt.head(), busRows}, len(busRows) == 0
		case depIfaceFile:
			var d partIfaceDoc
			_ = json.Unmarshal(data, &d)
			doc, empty = partIfaceDoc{pt.head(), d.Instances}, len(d.Instances) == 0
		case depIngressFile:
			var d partIngressDoc
			_ = json.Unmarshal(data, &d)
			doc, empty = partIngressDoc{pt.head(), d.Hosts}, len(d.Hosts) == 0
		}
		if err := b.writePartFile(pt, file, doc, empty); err != nil {
			return err
		}
		if !empty {
			done.registrations = append(done.registrations, file)
		}
	}
	if b.cron != nil && len(cronRows) > 0 {
		b.cron.setPart(pt, cronRows)
	}
	if b.bus != nil && len(busRows) > 0 {
		b.bus.setPartSubs(pt, busRows)
	}
	return nil
}

// afterPartitionRestore records the restore in the tile's history and the
// audit log; an admin's restore of another person's partition tells them.
func (b *Broker) afterPartitionRestore(t partitionBackupTarget, version string, earlier bool) {
	reason := "from " + t.pkeyFrom + " version " + version
	if earlier {
		reason += " (an earlier holder of the id: to " + t.user + ")"
	}
	b.noteTileHistory(t.c.Path, modeHistory{Op: modeOpPartitionRestore, By: t.by, Reason: reason, Partition: t.pkeyNow})
	slog.Info("partition restored from its backup", "tile", t.c.Path, "partition", "user:"+t.user, "id", t.pkeyNow,
		"from", t.pkeyFrom, "version", version, "earlierHolder", earlier, "by", t.by)
	if t.by == t.user {
		return
	}
	when := time.Now().UTC().Format("2006-01-02 15:04 UTC")
	text := fmt.Sprintf("your partition of %s was restored from a backup by %s at %s", t.c.Path, t.by, when)
	partitionNotice(b, t.user, t.c.Path, text)
	b.pushPerson(t.user, "tile.partition-restored", "Your data in "+t.c.Path+" was restored from a backup",
		fmt.Sprintf("By %s at %s.", t.by, when), "c/"+t.c.Path+"/", "")
}

// ---- restores a partition mode switch guards ----

// preSwitchError refuses an archive made before its tile's last partition
// mode switch that deleted data, until confirm names that switch.
type preSwitchError struct {
	tile, created string
	sw            modeHistory
}

func (e preSwitchError) date() string { return e.sw.At.UTC().Format("2006-01-02") }

func (e preSwitchError) Error() string {
	return fmt.Sprintf("this backup (made %s) is older than %s's partition mode switch on %s (%s → %s): restoring it brings back data the switch deleted, into the global instance's namespace — confirm with %q (bx restore %s --confirm %s)",
		e.created, e.tile, e.date(), registry.SpecOf(e.sw.From), registry.SpecOf(e.sw.To), e.date(), e.tile, e.date())
}

// answer is the switch as POST /restore's 409 names it.
func (e preSwitchError) answer() map[string]any {
	return map[string]any{"at": e.sw.At.UTC().Format(time.RFC3339), "from": registry.SpecOf(e.sw.From), "to": registry.SpecOf(e.sw.To),
		"confirm": e.date()}
}

// lastDeletingSwitch is tile's last partition mode switch that deleted data
// (not one that only added "global").
func (b *Broker) lastDeletingSwitch(tile string) (modeHistory, bool) {
	rec := b.modeRecordOf(tile)
	if rec == nil {
		return modeHistory{}, false
	}
	for i := len(rec.History) - 1; i >= 0; i-- {
		h := rec.History[i]
		if h.Op == modeOpSwitch && wipeKindOf(registry.SpecOf(h.From), registry.SpecOf(h.To)) != wipeNone {
			return h, true
		}
	}
	return modeHistory{}, false
}

// preSwitchRestore refuses an archive of tile's data in deployment dep made
// before the tile's last partition mode switch that deleted it (11 §4),
// unless confirm names the switch (its date): the restore brings back what
// the switch deleted — a plaintext archive's data (a sealed one's was
// erased with its key) and the registrations the archive lists — into the
// global instance's namespace, at today's keys; never into a person's
// partition, which no such archive holds. Removing "global" deleted main's
// data only, so a deployment's archive from before it restores as ever.
// An archive made within the switch's second counts as older.
func (b *Broker) preSwitchRestore(tile string, m backup.Manifest, dep string, confirm string) error {
	sw, ok := b.lastDeletingSwitch(tile)
	if !ok {
		return nil
	}
	if cmp.Or(dep, util.MainDeployment) != util.MainDeployment && wipeKindOf(registry.SpecOf(sw.From), registry.SpecOf(sw.To)) != wipeEverything {
		return nil
	}
	created, err := time.Parse(time.RFC3339, m.Created)
	if err == nil && !created.Before(sw.At.Truncate(time.Second).Add(time.Second)) {
		return nil // made after the switch
	}
	e := preSwitchError{tile: tile, created: m.Created, sw: sw}
	if confirm == e.date() {
		slog.Warn("restore: a backup older than the tile's partition mode switch, confirmed", "tile", tile, "created", m.Created, "switch", sw.At)
		return nil
	}
	return e
}
