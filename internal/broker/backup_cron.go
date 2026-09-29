package broker

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/robfig/cron/v3"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Scheduled component backups (plans/lifecycle.md LC-5). Owner-registered jobs on
// the existing cron engine that fire a xbind backup (not an element endpoint)
// and prune to a retention count. Persisted in data/backup-schedule.json.
//
// Tile deployments (08-data §11): a backup of a tile whose primary isn't main
// also archives the primary's data, first, in a deployment archive the main
// archive lists; the tile's schedule prunes both keys. A deployment beyond
// main may have a schedule of its own, in its registration directory, with
// its own retention. Below are also the per-deployment backup acts the
// deployments plane calls once it has judged the request (11-contract §1.8)
// — back up, list, restore into a namespace, schedule — and the restore of
// a main archive's registration files.

type backupSchedule struct {
	Component string `json:"component"`
	Schedule  string `json:"schedule"`  // 5-field cron or "@every 24h"
	Retention int    `json:"retention"` // versions to keep after each run (0 = keep all)
}

func (cr *cronRunner) backupPath() string {
	return filepath.Join(cr.b.Reg.Root, "data", "backup-schedule.json")
}

func (cr *cronRunner) loadBackups() {
	bts, err := os.ReadFile(cr.backupPath())
	if err != nil {
		return
	}
	var scheds []backupSchedule
	if json.Unmarshal(bts, &scheds) != nil {
		return
	}
	for _, s := range scheds {
		if err := cr.addBackup(s); err != nil {
			slog.Warn("backup schedule: dropping persisted entry", "component", s.Component, "err", err)
		}
	}
}

func (cr *cronRunner) persistBackups() {
	cr.mu.Lock()
	scheds := make([]backupSchedule, 0, len(cr.backups))
	for _, s := range cr.backups {
		scheds = append(scheds, s)
	}
	cr.mu.Unlock()
	sort.Slice(scheds, func(i, j int) bool { return scheds[i].Component < scheds[j].Component })
	bts, _ := json.MarshalIndent(scheds, "", "  ")
	_ = fsutil.WriteFileAtomicIn(cr.backupPath(), bts, 0o644)
}

func (cr *cronRunner) addBackup(s backupSchedule) error {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if old, ok := cr.backupEntries[s.Component]; ok {
		cr.sched.Remove(old)
	}
	id, err := cr.sched.AddFunc(s.Schedule, func() { cr.b.runScheduledBackup(s) })
	if err != nil {
		return err
	}
	cr.backupEntries[s.Component] = id
	cr.backups[s.Component] = s
	return nil
}

func (cr *cronRunner) removeBackup(comp string) bool {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	id, ok := cr.backupEntries[comp]
	if !ok {
		return false
	}
	cr.sched.Remove(id)
	delete(cr.backupEntries, comp)
	delete(cr.backups, comp)
	return true
}

// runScheduledBackup performs a backup and then prunes to the retention count:
// main's archive, and the primary's deployment archive when it wrote one; a
// sealed workspace's data archives go with the main archives that name
// them (pruneData). All of it under the tile's backup lock.
func (b *Broker) runScheduledBackup(s backupSchedule) {
	defer b.holdBackups(s.Component)()
	_, archives, err := b.backupTileHeld(s.Component, false)
	if err != nil {
		slog.Warn("scheduled backup failed", "component", s.Component, "err", err)
		return
	}
	if s.Retention > 0 {
		b.pruneVersions(s.Component, s.Retention)
		b.pruneData(s.Component)
		for dep := range archives {
			b.pruneKey(s.Component, archiveKey(s.Component, dep), s.Retention)
		}
	}
}

// pruneVersions deletes a component's oldest archived versions beyond keep.
func (b *Broker) pruneVersions(comp string, keep int) { b.pruneKey(comp, backupKey(comp), keep) }

// pruneKey deletes the oldest versions beyond keep under one archive key of
// comp's archiver: one key, so no other key's versions ever go.
func (b *Broker) pruneKey(comp, key string, keep int) {
	provider := b.archiveProvider(comp)
	if provider == "" {
		return
	}
	code, body, err := b.archiveDo("GET", provider, "/archive/"+key+"/versions", nil)
	if err != nil || code >= 400 {
		return
	}
	var out struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	if json.Unmarshal(body, &out) != nil {
		return
	}
	// The archiver returns versions newest-first, so anything past keep is old.
	for i := keep; i < len(out.Versions); i++ {
		_, _, _ = b.archiveDo("DELETE", provider, "/archive/"+key+"/versions/"+out.Versions[i].Version, nil)
	}
}

// --- API --------------------------------------------------------------------

func (b *Broker) apiBackupScheduleList(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	b.cron.mu.Lock()
	out := make([]backupSchedule, 0, len(b.cron.backups))
	for _, s := range b.cron.backups {
		out = append(out, s)
	}
	b.cron.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Component < out[j].Component })
	server.WriteJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (b *Broker) apiBackupScheduleSet(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var s backupSchedule
	if err := server.DecodeJSON(r, &s); err != nil || s.Component == "" || s.Schedule == "" {
		server.WriteError(w, http.StatusBadRequest, "need {component, schedule, retention?}")
		return
	}
	if _, ok := b.Reg.Component(s.Component); !ok {
		server.WriteError(w, http.StatusNotFound, "no such component")
		return
	}
	if err := b.cron.addBackup(s); err != nil {
		server.WriteError(w, http.StatusBadRequest, "bad schedule: "+err.Error())
		return
	}
	b.cron.persistBackups()
	server.WriteOK(w)
}

func (b *Broker) apiBackupScheduleDelete(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	comp := r.URL.Query().Get("component")
	if !b.cron.removeBackup(comp) {
		server.WriteError(w, http.StatusNotFound, "no schedule for that component")
		return
	}
	b.cron.persistBackups()
	server.WriteOK(w)
}

// ---- the deployment archives of a tile's backup (08-data §11.1, §11.5) ----

// mainDeclared is scope sm of root tile with main's declarations: the
// registry's while main is the primary (every tile without a record), else
// main's own code's (D127n), so a backup never applies another deployment's
// declarations to main's keys. The registry's when main's code can't be
// read.
func (b *Broker) mainDeclared(root string, sm *registry.ScopeManifest) *registry.ScopeManifest {
	if b.primaryOf(root) == util.MainDeployment {
		return sm
	}
	res, err := b.declaredFrom(root, sm, util.MainDeployment)
	if err != nil || sm == nil {
		slog.Warn("backup: main's declarations can't be read; the registry's are archived", "tile", root, "err", err)
		return sm
	}
	v := *sm
	v.Resources = res
	return &v
}

// archivedDeployments are the deployments of root tile c whose data a
// backup archives beside its main archive: the primary when it isn't main,
// and with every (an offload) each other deployment beyond main whose
// namespace exists. A tile that doesn't root its scope has none: its data
// is in no archive, as today.
func (b *Broker) archivedDeployments(c *registry.Component, every bool) []string {
	if _, isRoot := b.Reg.Scopes()[c.Path]; !isRoot {
		return nil
	}
	primary, names := b.deploymentsOf(c.Path)
	var out []string
	for _, dep := range names {
		if dep != util.MainDeployment && (dep == primary || every && b.nsExists(nsOf(c.Path, dep))) {
			out = append(out, dep)
		}
	}
	return out
}

func (b *Broker) nsExists(id nsID) bool {
	dir, err := b.nsDir(id)
	if err == nil {
		_, err = os.Lstat(dir)
	}
	return err == nil
}

// putDeploymentArchives writes archivedDeployments' archives, answering
// their versions by name (nil: none).
func (b *Broker) putDeploymentArchives(c *registry.Component, provider string, every bool) (map[string]string, error) {
	var out map[string]string
	for _, dep := range b.archivedDeployments(c, every) {
		v, err := b.putDeploymentArchive(c, provider, dep)
		if err != nil {
			return nil, fmt.Errorf("archiving %s's data: %w", dep, err)
		}
		if out == nil {
			out = map[string]string{}
		}
		out[dep] = v
	}
	return out, nil
}

// putDeploymentArchive archives deployment dep's data of root tile c under
// dep's own key.
func (b *Broker) putDeploymentArchive(c *registry.Component, provider, dep string) (string, error) {
	return b.putArchive(provider, archiveKey(c.Path, dep), deploymentSeal(c.Path, dep), func(bw *backup.Writer) error { return b.writeDeploymentArchive(bw, c, dep) })
}

// gateNamespaces holds, exclusively, the write gate of every namespace
// beyond main an offload of comp archives, so no API write lands between
// its archive and its removal; the caller releases them.
func (b *Broker) gateNamespaces(comp string) (release func()) {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return func() {}
	}
	var held []*sync.RWMutex
	for _, dep := range b.archivedDeployments(c, true) {
		g := b.nsTab().gate(nsOf(comp, dep))
		g.Lock()
		held = append(held, g)
	}
	return func() {
		for _, g := range held {
			g.Unlock()
		}
	}
}

// dropNamespaceKV removes the kv file of each namespace beyond main an
// offload of comp archived, once every PUT is confirmed: the archive holds
// it whole. File volumes stay: a volume's walk can't yet tell what its
// archive would lose (08-data §9.4).
func (b *Broker) dropNamespaceKV(comp string, archived map[string]string) error {
	var errs []error
	for dep := range archived {
		k, err := nsOf(comp, dep).keys()
		if err != nil {
			continue
		}
		if b.kv != nil {
			if err := b.kv.closeNamespace(k.NS); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := os.Remove(filepath.Join(b.Reg.Root, filepath.FromSlash(nsKVFile(k.NS)))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---- the per-deployment backup acts (11-contract §1.8) ----
//
// The plane judges each request (an admin, in a person's own session)
// before it calls these; their errors are *deployments.Error.

const (
	badDeploymentName  = `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`
	depBackupFile      = "backup-schedule.json"
	depBackupRetention = 3 // versions a deployment's schedule keeps unless told (08-data §11.3)
)

// archiveVersion is an archive version as it may reach an archiver URL.
var archiveVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// dataTile resolves the tile and deployment of a per-deployment backup act:
// beyond main, only a tile that roots its scope has data to archive.
func (b *Broker) dataTile(tile, dep string, exists bool) (*registry.Component, error) {
	c, ok := b.Reg.Component(tile)
	switch {
	case !ok:
		return nil, nsErr(http.StatusNotFound, "", "no such tile: "+tile)
	case !util.DeploymentNameOK(dep):
		return nil, nsErr(http.StatusBadRequest, "", badDeploymentName)
	case exists && !b.hasDeployment(tile, dep):
		return nil, nsErr(http.StatusNotFound, "", util.NoDeployment(tile, dep).Error())
	}
	if _, isRoot := b.Reg.Scopes()[tile]; !isRoot && dep != util.MainDeployment {
		return nil, notRoot(tile)
	}
	return c, nil
}

// notRoot is the 409 of a tile whose data its scope root holds.
func notRoot(tile string) error {
	return nsErr(http.StatusConflict, deployments.KindPolicy,
		tile+" doesn't root its scope: its data is backed up and restored with its scope root")
}

// noArchiver is the 502 of a tile without an @archive binding, today's text.
func noArchiver(tile string) error {
	return nsErr(http.StatusBadGateway, "", fmt.Sprintf("no archiver bound — set one: bx bind %q %s=<archiver> (or bind '*' for a default)", tile, archiveSlot))
}

// BackupDeploymentData is POST /deployments/backup: deployment dep's data
// archived now under its own key. main's is the tile's main archive, as
// POST /backup writes it.
func (b *Broker) BackupDeploymentData(tile, dep string, dryRun bool) (deployments.BackupAnswer, error) {
	c, err := b.dataTile(tile, dep, true)
	if err != nil {
		return deployments.BackupAnswer{}, err
	}
	provider := b.archiveProvider(tile)
	switch act := b.busyAct(nsOf(c.Scope, dep)); {
	case provider == "":
		return deployments.BackupAnswer{}, noArchiver(tile)
	case act != "" && dep != util.MainDeployment:
		return deployments.BackupAnswer{}, nsBusy(dep, act)
	case dryRun:
		return deployments.BackupAnswer{OK: "true", Deployment: dep}, nil
	}
	var v string
	if dep == util.MainDeployment {
		v, err = b.doBackup(tile)
	} else {
		release := b.holdBackups(tile)
		v, err = b.putDeploymentArchive(c, provider, dep)
		release()
	}
	if err != nil {
		return deployments.BackupAnswer{}, nsErr(http.StatusBadGateway, "", err.Error())
	}
	return deployments.BackupAnswer{OK: "true", Deployment: dep, Version: v}, nil
}

// DeploymentBackupList is GET /deployments/backups' answer: the archiver's
// versions of dep's key, newest first; none without an archiver.
type DeploymentBackupList struct {
	Deployment string            `json:"deployment"`
	Versions   []json.RawMessage `json:"versions"`
	Archiver   string            `json:"archiver"`
}

// DeploymentBackups lists deployment dep's archived versions. dep need not
// exist: removing a deployment never deletes its archives.
func (b *Broker) DeploymentBackups(tile, dep string) (DeploymentBackupList, error) {
	if _, err := b.dataTile(tile, dep, false); err != nil {
		return DeploymentBackupList{}, err
	}
	out := DeploymentBackupList{Deployment: dep, Versions: []json.RawMessage{}, Archiver: b.archiveProvider(tile)}
	if out.Archiver == "" {
		return out, nil
	}
	code, body, err := b.archiveDo("GET", out.Archiver, "/archive/"+archiveKey(tile, dep)+"/versions", nil)
	var list struct {
		Versions []json.RawMessage `json:"versions"`
	}
	if err != nil || code >= 400 || json.Unmarshal(body, &list) != nil {
		return DeploymentBackupList{}, nsErr(http.StatusBadGateway, "", "archiver: "+firstLine(string(body)))
	}
	if list.Versions != nil {
		out.Versions = list.Versions
	}
	return out, nil
}

// RestoreDeploymentData is POST /deployments/restore: req.Deployment's
// archive ("" is the primary; main: the tile's main archive, its data only)
// restored into req.Into (default the archive's own deployment), never the
// work tree. authorize judges every other claimant of the target namespace
// at the reset level (D127t) and stop stops each claimant's deployment
// addressing it. It also answers those claimants: a dry run's stops.
func (b *Broker) RestoreDeploymentData(tile, by string, req deployments.RestoreRequest,
	authorize func(tile string) error, stop func(tile, dep string)) (deployments.RestoreAnswer, []string, error) {
	from := cmp.Or(req.Deployment, b.primaryOf(tile))
	r := dataRestore{from: from, version: req.Version, into: cmp.Or(req.Into, from), replace: req.Replace != nil && *req.Replace,
		confirmed: req.Confirm == deployments.ConfirmEraseData, dryRun: req.DryRun, by: by, authorize: authorize, stop: stop}
	v, skipped, claimants, err := b.restoreData(tile, r)
	if err != nil {
		return deployments.RestoreAnswer{}, nil, err
	}
	return deployments.RestoreAnswer{OK: "true", Deployment: from, Into: r.into, Restored: v, Skipped: skipped}, claimants, nil
}

// ---- a deployment's own backup schedule ----

// depBackupDoc is backup-schedule.json in a deployment's registration
// directory: today's row without component, which the file names.
type depBackupDoc struct {
	Schema    int    `json:"schema"`
	Schedule  string `json:"schedule"`
	Retention int    `json:"retention"` // versions kept after each run; 0 keeps all
}

// depBackupEntries are the scheduled backups of deployments beyond main,
// per cron runner (whose struct stays as it is): entry ids by depKey.
var depBackupEntries sync.Map // *cronRunner → *depBackupTab

type depBackupTab struct {
	mu      sync.Mutex
	entries map[string]cron.EntryID
}

// scheduleDepBackup (re)schedules deployment dep of tile's backups on
// schedule; "" unschedules them.
func (cr *cronRunner) scheduleDepBackup(tile, dep, schedule string) error {
	v, _ := depBackupEntries.LoadOrStore(cr, &depBackupTab{entries: map[string]cron.EntryID{}})
	t := v.(*depBackupTab)
	t.mu.Lock()
	defer t.mu.Unlock()
	key := depKey(tile, dep, depBackupFile)
	if id, ok := t.entries[key]; ok {
		cr.sched.Remove(id)
		delete(t.entries, key)
	}
	if schedule == "" {
		return nil
	}
	id, err := cr.sched.AddFunc(schedule, func() { cr.b.runDeploymentBackup(tile, dep) })
	if err == nil {
		t.entries[key] = id
	}
	return err
}

// depBackupSchedule reads deployment dep of tile's schedule: false without
// one, or for a deployment tile no longer has.
func (b *Broker) depBackupSchedule(tile, dep string) (depBackupDoc, bool) {
	var doc depBackupDoc
	if dep == util.MainDeployment || !b.hasDeployment(tile, dep) || b.readDepFile(tile, dep, depBackupFile, &doc) != nil {
		return doc, false
	}
	return doc, doc.Schedule != ""
}

// runDeploymentBackup is a tick of deployment dep's schedule: its archive
// written, then pruned to its retention. Nothing while it is the primary
// (the tile's backups archive the primary, and its schedule prunes that
// key), while its tile isn't enabled or a data act holds its namespace; a
// schedule whose file or deployment is gone unschedules itself.
func (b *Broker) runDeploymentBackup(tile, dep string) {
	doc, ok := b.depBackupSchedule(tile, dep)
	c, found := b.Reg.Component(tile)
	switch {
	case !ok || !found:
		_ = b.cron.scheduleDepBackup(tile, dep, "")
		return
	case b.isPrimary(tile, dep), b.Reg.LifecycleState(tile) != registry.StateEnabled, b.busyAct(nsOf(c.Scope, dep)) != "":
		return
	}
	provider := b.archiveProvider(tile)
	if provider == "" {
		slog.Warn("scheduled deployment backup: no archiver bound", "tile", tile, "deployment", dep)
		return
	}
	defer b.holdBackups(tile)()
	if _, err := b.putDeploymentArchive(c, provider, dep); err != nil {
		slog.Warn("scheduled deployment backup failed", "tile", tile, "deployment", dep, "err", err)
		return
	}
	if doc.Retention > 0 {
		b.pruneKey(tile, archiveKey(tile, dep), doc.Retention)
	}
}

// LoadDeploymentBackupSchedules schedules every deployment's own backups
// from its file. Boot calls it once the deployments plane's answers are
// installed; a file this xbind can't read leaves its schedule off.
func (b *Broker) LoadDeploymentBackupSchedules() {
	for tile, deps := range b.beyondMain() {
		for _, dep := range deps {
			if doc, ok := b.depBackupSchedule(tile, dep); ok {
				if err := b.cron.scheduleDepBackup(tile, dep, doc.Schedule); err != nil {
					slog.Warn("deployment backup schedule not loaded", "tile", tile, "deployment", dep, "err", err)
				}
			}
		}
	}
}

// DeploymentBackupSchedule is Deployment.backup (11-contract §1.1): a
// non-primary deployment's schedule, nil without one.
func (b *Broker) DeploymentBackupSchedule(tile, dep string) *deployments.BackupSchedule {
	doc, ok := b.depBackupSchedule(tile, dep)
	if !ok || b.isPrimary(tile, dep) {
		return nil
	}
	return &deployments.BackupSchedule{Schedule: doc.Schedule, Retention: doc.Retention}
}

// SetDeploymentBackupSchedule is POST /deployments/backup-schedule:
// schedule "" removes deployment dep's schedule; retention (default 3)
// counts the versions each run keeps. main's backups are the tile's, so its
// schedule is today's (POST /backup-schedule's row).
func (b *Broker) SetDeploymentBackupSchedule(tile, dep string, schedule *string, retention *int, dryRun bool) error {
	if _, err := b.dataTile(tile, dep, true); err != nil {
		return err
	}
	keep := depBackupRetention
	switch {
	case schedule == nil:
		return nsErr(http.StatusBadRequest, "", `bad request body: send schedule, a cron expression or "" to remove it`)
	case retention != nil && *retention < 0:
		return nsErr(http.StatusBadRequest, "", "retention counts the versions each backup keeps: 0 (all) or more")
	case retention != nil:
		keep = *retention
	}
	if _, err := cron.ParseStandard(*schedule); *schedule != "" && err != nil {
		return nsErr(http.StatusBadRequest, "", "bad schedule: "+err.Error())
	}
	switch {
	case dryRun:
		return nil
	case dep == util.MainDeployment && *schedule == "":
		b.cron.removeBackup(tile)
	case dep == util.MainDeployment:
		if err := b.cron.addBackup(backupSchedule{Component: tile, Schedule: *schedule, Retention: keep}); err != nil {
			return nsErr(http.StatusBadRequest, "", "bad schedule: "+err.Error())
		}
	default:
		doc := depBackupDoc{Schema: depFileSchema, Schedule: *schedule, Retention: keep}
		if err := b.writeDepFile(tile, dep, depBackupFile, doc, *schedule == ""); err != nil {
			return err
		}
		return b.cron.scheduleDepBackup(tile, dep, *schedule)
	}
	b.cron.persistBackups()
	return nil
}

// restoreDepBackupSchedule puts an archived schedule back (restore-
// Registrations), checked as the route checks it.
func (b *Broker) restoreDepBackupSchedule(tile, dep string, data []byte) error {
	var doc depBackupDoc
	if err := decodeRegFile(data, &doc); err != nil {
		return err
	}
	if _, err := cron.ParseStandard(doc.Schedule); err != nil || doc.Retention < 0 {
		return fmt.Errorf("schedule %q, retention %d: not a schedule this xbind runs", doc.Schedule, doc.Retention)
	}
	if err := b.writeDepFile(tile, dep, depBackupFile, doc, false); err != nil {
		return err
	}
	return b.cron.scheduleDepBackup(tile, dep, doc.Schedule)
}
