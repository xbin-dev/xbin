package broker

// partitionregs.go — a person's partition's registrations
// (plans/partitions/03 §D; PD-20, PD-21, C5, S15): its cron jobs, bus push
// subscriptions, interface instances and ingress hosts, kept in its own
// directory (partitionrecords.go), data/partitions/<TileKey>/<dep>/<pkey>/
// {cron,bus-subscriptions,iface-instances,ingress-hosts}.json, schema 1,
// the deployment files' row shapes plus the tile and the partition the file
// belongs to, which every load checks. The global instance's stay in
// today's stores; an older xbind never reads these, so it never fires a
// person's job as the tile's. A backup of the tile carries only global's
// rows: these never join today's maps (S15).
//
// Who registers. A principal of the tile acting in a person's partition —
// its backend, its person's frames, terminals and agent sessions; the
// partition gate stamped the partition — registers in that partition only,
// always its primary's (PD-17). Admins list and delete global's rows only;
// `?partition=` on a partitioned tile's route is refused (400) for
// everyone (partitionvault.go).
//
// Firing (PD-20). A partition's registrations fire while the tile runs
// people's partitions (not paused), is enabled, and the partition's person
// exists with the same uid (the directory's pkey is theirs now), is enabled
// and can read the tile — asked at every tick, publish and delivery, never
// cached: a person who regains access resumes without registering again.
// Deliveries carry Principal{Component: xbin/cron | xbin/bus, Partition:
// the registration's}, which Route sends to that partition as a background
// start.
//
//   - Cron: at most partCronCap jobs a partition, none more often than once
//     a minute. A tick the runner defers (its background admission,
//     503 partition start deferred) is retried with jitter until the next
//     tick is due; one still undelivered is counted (MissedTicks).
//   - Bus: at most partBusCap subscriptions a partition. An event of the
//     partition's own namespace — its scope's own bus, stamped with it —
//     reaches its subscriptions and may start it (its publisher is the
//     partition, which runs); an event of a shared bus, of an unpartitioned
//     scope's bus or of another tile's scope reaches it only while the
//     partition runs, never cold-starting it; one skipped is counted
//     (dormantDrops). Global's events never reach a person's partition.
//   - Interface instances and ingress hosts: stored and answered with
//     success, dormant: they never route — the public surface and #instance
//     bindings are the global instance's (PD-21).

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// A person's partition's limits (C5): the global instance keeps today's.
const (
	partCronCap         = 16
	partBusCap          = 16
	partCronMinInterval = time.Minute
)

// partRegistrationFiles are the registration files of a partition's
// directory.
var partRegistrationFiles = [...]string{depCronFile, depBusFile, depIfaceFile, depIngressFile}

// partRegDoc is what every partition registration file starts with.
type partRegDoc struct {
	Schema    int    `json:"schema"`
	Tile      string `json:"tile"`
	Partition string `json:"partition"`
}

type partCronDoc struct {
	partRegDoc
	Jobs []depCronRow `json:"jobs"`
}

type partBusDoc struct {
	partRegDoc
	Subscriptions []depBusRow `json:"subscriptions"`
}

type partIfaceDoc struct {
	partRegDoc
	Instances map[string]string `json:"instances"`
}

type partIngressDoc struct {
	partRegDoc
	Hosts []string `json:"hosts"`
}

func (t partTarget) head() partRegDoc {
	return partRegDoc{Schema: depFileSchema, Tile: t.tile, Partition: string(t.part)}
}

// partPrefix keys every registration of one partition in memory; partKey
// one of them (03 §D: the deployment's key gains the pkey). Tile paths hold
// no NUL.
func partPrefix(tile, dep, pkey string) string {
	return tile + "\x00" + cmp.Or(dep, util.MainDeployment) + "\x00p\x00" + pkey + "\x00"
}
func partKey(t partTarget, name string) string { return partPrefix(t.tile, t.dep, t.pkey) + name }

// readPartFile decodes t's registration file into doc; none leaves doc
// empty. A file of another schema, tile or partition is refused.
func (b *Broker) readPartFile(t partTarget, file string, doc any) error {
	dir, err := t.dir(b)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, file)) // walk-ok: data/partitions is xbind's own
	if errNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var head partRegDoc
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s: %s's %s: %w", t.tile, t.part, file, err)
	}
	switch {
	case head.Schema != depFileSchema:
		return statusErr{http.StatusConflict, fmt.Sprintf("%s: %s's %s has schema %d, which this xbind doesn't read", t.tile, t.part, file, head.Schema)}
	case head.Tile != t.tile || t.part != "" && head.Partition != string(t.part):
		return fmt.Errorf("%s: the partition file %s names another partition: refused", t.tile, file)
	}
	return json.Unmarshal(data, doc)
}

// writePartFile writes doc as t's registration file, or removes it when it
// holds nothing. The partition's record is made first.
func (b *Broker) writePartFile(t partTarget, file string, doc any, empty bool) error {
	dir, err := t.dir(b)
	if err != nil {
		return err
	}
	if empty {
		if err := os.Remove(filepath.Join(dir, file)); err != nil && !errNotExist(err) {
			return err
		}
		return nil
	}
	if err := b.notePartition(t, false); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileIn(dir, file, append(data, '\n'))
}

// partRegFires is the liveness gate of t's registrations (PD-20), asked at
// every tick, publish and delivery.
func (b *Broker) partRegFires(t partTarget) bool {
	id, ok := t.part.User()
	if !ok || b.Reg.LifecycleState(t.tile) != registry.StateEnabled {
		return false
	}
	c, found := b.Reg.Component(t.tile)
	if !found {
		return false
	}
	if spec, on := c.Partitioned(); !on || !spec.User || !b.isPrimary(t.tile, t.dep) || b.partitionPaused(t.tile, t.dep) {
		return false
	}
	if b.personLive(id, t.tile) != nil {
		return false
	}
	uid := b.storedPartitionUID(id)
	return uid != "" && util.PartitionKey(id, uid) == t.pkey
}

// partOf is the registration target a principal of tile acting in a
// person's partition reaches, or its refusal (403) answered; ok false for
// every other principal (today's path) and after a refusal (answered).
func (b *Broker) partOf(w http.ResponseWriter, p auth.Principal, tile string) (t partTarget, isPart, ok bool) {
	if tile == "" || p.Component != tile || !p.Partition.IsUser() {
		return partTarget{}, false, true
	}
	t, err := b.partTargetOf(p, tile)
	if err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/partitions.md")
		return partTarget{}, true, false
	}
	return t, true, true
}

// ---- cron ----

// partJob is one scheduled job of a person's partition.
type partJob struct {
	t      partTarget
	job    cronJob // Component is the tile
	entry  cron.EntryID
	missed int64 // ticks the runner kept deferring until the next was due
}

// cronRetryDelay is how long a deferred tick waits before it is tried
// again, with left until the next tick; 0 gives up (the tick is missed).
var cronRetryDelay = func(left time.Duration) time.Duration {
	d := 10*time.Second + rand.N(20*time.Second)
	if d >= left {
		d = left / 2
	}
	if d < time.Second {
		return 0
	}
	return d
}

// partScheduleOK refuses a schedule a person's partition may not have: one
// firing more often than once a minute (C5).
func partScheduleOK(spec string) error {
	s, err := cron.ParseStandard(spec)
	if err != nil {
		return fmt.Errorf("bad schedule %q: %w", spec, err)
	}
	if d, ok := s.(cron.ConstantDelaySchedule); ok && d.Delay < partCronMinInterval {
		return fmt.Errorf("a person's partition schedules at most once a minute (%q is more often)", spec)
	}
	return nil
}

// setPart makes rows exactly t's scheduled jobs.
func (cr *cronRunner) setPart(t partTarget, rows []depCronRow) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	prefix := partKey(t, "")
	for key, pj := range cr.part {
		if strings.HasPrefix(key, prefix) {
			cr.sched.Remove(pj.entry)
			delete(cr.part, key)
		}
	}
	for _, row := range rows {
		row.Role = cmp.Or(row.Role, "writer")
		if err := row.check(); err != nil {
			slog.Warn("cron: dropping a partition's job", "tile", t.tile, "partition", t.part, "job", row.Name, "err", err)
			continue
		}
		pj := &partJob{t: t, job: row.job(t.tile)}
		key := partKey(t, row.Name)
		id, err := cr.sched.AddFunc(row.Schedule, func() { cr.firePart(key, pj) })
		if err != nil {
			continue
		}
		pj.entry = id
		cr.part[key] = pj
	}
}

// rewritePart changes t's cron.json and schedules what it then holds.
func (cr *cronRunner) rewritePart(t partTarget, change func([]depCronRow) ([]depCronRow, error)) error {
	cr.fileMu.Lock()
	defer cr.fileMu.Unlock()
	var doc partCronDoc
	if err := cr.b.readPartFile(t, depCronFile, &doc); err != nil {
		return err
	}
	rows, err := change(doc.Jobs)
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].Name < rows[k].Name })
	if err := cr.b.writePartFile(t, depCronFile, partCronDoc{t.head(), rows}, len(rows) == 0); err != nil {
		return err
	}
	cr.setPart(t, rows)
	return nil
}

// firePart is a tick of a partition's job: delivered as its partition while
// it fires (PD-20), retried while the runner defers the start.
func (cr *cronRunner) firePart(key string, pj *partJob) {
	cr.mu.Lock()
	cur, dispatch := cr.part[key], cr.dispatch
	cr.mu.Unlock()
	if cur != pj || dispatch == nil || !cr.b.partRegFires(pj.t) {
		return
	}
	var next time.Time
	if s, err := cron.ParseStandard(pj.job.Schedule); err == nil {
		next = s.Next(time.Now())
	}
	cr.deliverPart(key, pj, dispatch, next)
}

// partCronPrincipal is the identity a tick of pj carries.
func partCronPrincipal(pj *partJob) auth.Principal {
	p := cronPrincipal(pj.t.dep, pj.job.Role)
	p.Partition = pj.t.part
	return p
}

// partDeferred reports the proxy's answer to a delivery whose partition
// start the runner deferred.
func partDeferred(code int, body string) bool {
	return code == http.StatusServiceUnavailable && strings.Contains(body, runner.ErrPartitionDeferred.Error())
}

// deliverPart delivers one tick of pj, trying a deferred one again with
// jitter until next (the following tick) is due.
func (cr *cronRunner) deliverPart(key string, pj *partJob, dispatch func(auth.Principal, string, string) (int, string), next time.Time) {
	code, body := dispatch(partCronPrincipal(pj), pj.job.Component, pj.job.Path)
	if !partDeferred(code, body) {
		if code >= 400 {
			slog.Warn("cron job failed", "job", pj.job.Name, "component", pj.job.Component, "partition", pj.t.part,
				"status", code, "body", firstLine(body))
		}
		return
	}
	left := time.Until(next)
	delay := cronRetryDelay(left)
	if delay <= 0 || delay >= left {
		cr.mu.Lock()
		pj.missed++
		cr.mu.Unlock()
		slog.Warn("cron: a partition's tick missed: its start stayed deferred", "job", pj.job.Name, "component", pj.job.Component, "partition", pj.t.part)
		return
	}
	time.AfterFunc(delay, func() {
		cr.mu.Lock()
		cur, dispatch := cr.part[key], cr.dispatch
		cr.mu.Unlock()
		if cur == pj && dispatch != nil && cr.b.partRegFires(pj.t) {
			cr.deliverPart(key, pj, dispatch, next)
		}
	})
}

// partRows lists t's jobs.
func (cr *cronRunner) partRows(t partTarget) []cronJob {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	var out []cronJob
	prefix := partKey(t, "")
	for key, pj := range cr.part {
		if strings.HasPrefix(key, prefix) {
			out = append(out, pj.job)
		}
	}
	return out
}

// partCronList is GET /cron/jobs for a person's partition: its own jobs.
func (b *Broker) partCronList(w http.ResponseWriter, t partTarget) {
	dormant := !b.partRegFires(t)
	rows := []cronJobView{}
	for _, j := range b.cron.partRows(t) {
		rows = append(rows, cronJobView{cronJob: j, Dormant: dormant})
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].Name < rows[k].Name })
	server.WriteJSON(w, http.StatusOK, map[string]any{"jobs": rows})
}

// putPartCron is PUT /cron/jobs for a person's partition: stored in its
// cron.json, at most partCronCap, none more often than once a minute.
func (b *Broker) putPartCron(w http.ResponseWriter, r *http.Request, p auth.Principal, t partTarget, j cronJob, rt resTarget) {
	if err := b.allowResUnclamped(p, rt.String(), "writer"); err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	row := depCronRow{Name: j.Name, Resource: rt.String(), Schedule: j.Schedule, Path: j.Path, Role: cmp.Or(j.Role, "writer")}
	if err := row.check(); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := partScheduleOK(row.Schedule); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), "/docs/partitions.md")
		return
	}
	if err := b.cron.rewritePart(t, func(rows []depCronRow) ([]depCronRow, error) {
		if !slices.ContainsFunc(rows, func(r depCronRow) bool { return r.Name == row.Name }) && len(rows) >= partCronCap {
			return nil, statusErr{http.StatusConflict, fmt.Sprintf("a person's partition of %s has at most %d cron jobs — delete one first", t.tile, partCronCap)}
		}
		return upsertRow(rows, row), nil
	}); err != nil {
		writeRegErr(w, err)
		return
	}
	writeRegOK(w, r, !b.partRegFires(t))
}

// deletePartCron is DELETE /cron/jobs/{name} for a person's partition.
func (b *Broker) deletePartCron(w http.ResponseWriter, r *http.Request, t partTarget, name string) {
	if err := b.cron.rewritePart(t, func(rows []depCronRow) ([]depCronRow, error) {
		rows, ok := removeRow(rows, name)
		if !ok {
			return nil, statusErr{http.StatusNotFound, "no such job"}
		}
		return rows, nil
	}); err != nil {
		writeRegErr(w, err)
		return
	}
	writeRegOK(w, r, false)
}

// ---- interface instances and ingress hosts: dormant (PD-21) ----

// partRouteSep joins a person's partition to the deployment routeTarget
// answers, so the stores write the partition's own file: no deployment name
// holds a NUL.
const partRouteSep = "\x00"

// partRouteDep is t as routeTarget's deployment.
func partRouteDep(t partTarget) string {
	return strings.Join([]string{t.dep, t.pkey, string(t.part), t.user, t.uid}, partRouteSep)
}

// partRouteTarget is the partition a routeTarget deployment names, if any.
func partRouteTarget(tile, dep string) (partTarget, bool) {
	f := strings.Split(dep, partRouteSep)
	if len(f) != 5 {
		return partTarget{}, false
	}
	return partTarget{tile: tile, dep: f[0], pkey: f[1], part: util.Partition(f[2]), user: f[3], uid: f[4]}, true
}

// partRouteOf answers routeTarget for a principal of comp acting in a
// person's partition: its partition's own, always dormant. isPart false:
// any other principal.
func (b *Broker) partRouteOf(w http.ResponseWriter, p auth.Principal, comp string) (dep string, isPart, ok bool) {
	t, isPart, ok := b.partOf(w, p, comp)
	if !isPart || !ok {
		return "", isPart, ok
	}
	return partRouteDep(t), true, true
}

// storePartInstances writes a partition's interface instances (dormant).
func (b *Broker) storePartInstances(t partTarget, inst map[string]string) error {
	return b.writePartFile(t, depIfaceFile, partIfaceDoc{t.head(), inst}, len(inst) == 0)
}

// storePartHosts writes a partition's ingress hosts (dormant).
func (b *Broker) storePartHosts(t partTarget, hosts []string) error {
	return b.writePartFile(t, depIngressFile, partIngressDoc{t.head(), hosts}, len(hosts) == 0)
}

// ---- boot, drops, counts ----

// loadParts schedules every person's partition's jobs and holds their
// subscriptions, from their files (boot: after the dispatches are set). A
// directory whose record can't be read keeps its files, unloaded.
func (b *Broker) loadParts(cronToo, busToo bool) {
	_ = b.eachPartitionRecord("", func(d partitionDirOf, rec partitionRecord) {
		t := partTarget{tile: rec.Tile, dep: d.dep, pkey: d.pkey, part: util.UserPartition(rec.User), user: rec.User, uid: rec.UID}
		if cronToo {
			var doc partCronDoc
			if err := b.readPartFile(t, depCronFile, &doc); err != nil {
				slog.Warn("cron: a partition's jobs aren't loaded", "tile", t.tile, "partition", t.part, "err", err)
			} else if len(doc.Jobs) > 0 {
				b.cron.setPart(t, doc.Jobs)
			}
		}
		if busToo {
			var doc partBusDoc
			if err := b.readPartFile(t, depBusFile, &doc); err != nil {
				slog.Warn("bus subscriptions: a partition's aren't loaded", "tile", t.tile, "partition", t.part, "err", err)
			} else if len(doc.Subscriptions) > 0 {
				b.bus.setPartSubs(t, doc.Subscriptions)
			}
		}
	})
}

// dropPartRows forgets the rows the broker holds of deployment dep's user
// partition pkey of tile, and answers how many there were. The files are
// the caller's.
func (b *Broker) dropPartRows(tile, dep, pkey string) int64 {
	prefix := partPrefix(tile, dep, pkey)
	match := func(key string) bool { return strings.HasPrefix(key, prefix) }
	var n int64
	if cr := b.cron; cr != nil {
		cr.mu.Lock()
		for key, pj := range cr.part {
			if match(key) {
				cr.sched.Remove(pj.entry)
				delete(cr.part, key)
				n++
			}
		}
		cr.mu.Unlock()
	}
	if bs := b.bus; bs != nil {
		bs.mu.Lock()
		for key, st := range bs.part {
			if match(key) {
				st.gone, st.queue = true, nil
				delete(bs.part, key)
				n++
			}
		}
		bs.mu.Unlock()
	}
	return n
}

// dropPartitionRegistrationsAt drops the registrations of people's
// partitions of path and the tiles under it before a new tile is created
// there (D85, as dropDormantAt does the deployments'): the rows the broker
// holds and the files. Records and vaults stay leftovers of the path
// (partitionLeftovers).
func (b *Broker) dropPartitionRegistrationsAt(path string) int {
	n := 0
	_ = b.eachPartitionRecord("", func(d partitionDirOf, rec partitionRecord) {
		if rec.Tile != path && !strings.HasPrefix(rec.Tile, path+"/") {
			return
		}
		n += int(b.dropPartRows(rec.Tile, d.dep, d.pkey))
		for _, f := range partRegistrationFiles {
			if err := os.Remove(filepath.Join(d.dir, f)); err != nil && !errNotExist(err) {
				slog.Warn("a removed tile's partition registration file is still there", "tile", rec.Tile, "file", f, "err", err)
			}
		}
	})
	return n
}

// wipePartitionRegistrations removes every person's partition's
// registrations of the tile on a switch that deletes everything (H1:
// "global" coming or going keeps them): the rows the broker holds, counted,
// and the files with the interface instances and hosts they name.
func wipePartitionRegistrations(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if t.Kind != wipeEverything {
		return nil
	}
	var errs []error
	err := b.eachPartitionDir(t.Tile, func(d partitionDirOf) {
		pt := partTarget{tile: t.Tile, dep: d.dep, pkey: d.pkey}
		var inst partIfaceDoc
		var hosts partIngressDoc
		_ = b.readPartFile(pt, depIfaceFile, &inst)
		_ = b.readPartFile(pt, depIngressFile, &hosts)
		sum.Registrations += int64(len(inst.Instances) + len(hosts.Hosts))
		if t.DryRun {
			sum.Registrations += b.countPartRows(t.Tile, d.dep, d.pkey)
			return
		}
		sum.Registrations += b.dropPartRows(t.Tile, d.dep, d.pkey)
		for _, f := range partRegistrationFiles {
			if err := os.Remove(filepath.Join(d.dir, f)); err != nil && !errNotExist(err) {
				errs = append(errs, err)
			}
		}
	})
	return errors.Join(append(errs, err)...)
}

// countPartRows counts the rows the broker holds of one partition.
func (b *Broker) countPartRows(tile, dep, pkey string) int64 {
	prefix := partPrefix(tile, dep, pkey)
	var n int64
	b.cron.mu.Lock()
	for key := range b.cron.part {
		if strings.HasPrefix(key, prefix) {
			n++
		}
	}
	b.cron.mu.Unlock()
	b.bus.mu.Lock()
	for key := range b.bus.part {
		if strings.HasPrefix(key, prefix) {
			n++
		}
	}
	b.bus.mu.Unlock()
	return n
}

// PartitionRegCounts is what the partitions API (F7b's GET /partitions)
// shows of a person's partition's registrations: counts only (PD-46).
type PartitionRegCounts struct {
	CronJobs         int   `json:"cronJobs"`
	BusSubscriptions int   `json:"busSubscriptions"`
	IfaceInstances   int   `json:"ifaceInstances"`
	IngressHosts     int   `json:"ingressHosts"`
	MissedTicks      int64 `json:"missedTicks"`
	DormantDrops     int64 `json:"dormantDrops"`
	VaultKeys        int   `json:"vaultKeys"`
}

// PartitionRegistrations counts user partition pkey of deployment dep of
// tile's registrations and vault keys (a sealed vault's: 0).
func (b *Broker) PartitionRegistrations(tile, dep, pkey string) PartitionRegCounts {
	var out PartitionRegCounts
	prefix := partPrefix(tile, dep, pkey)
	b.cron.mu.Lock()
	for key, pj := range b.cron.part {
		if strings.HasPrefix(key, prefix) {
			out.CronJobs++
			out.MissedTicks += pj.missed
		}
	}
	b.cron.mu.Unlock()
	b.bus.mu.Lock()
	for key, st := range b.bus.part {
		if strings.HasPrefix(key, prefix) {
			out.BusSubscriptions++
			out.DormantDrops += st.stats.DormantDrops
		}
	}
	b.bus.mu.Unlock()
	t := partTarget{tile: tile, dep: cmp.Or(dep, util.MainDeployment), pkey: pkey}
	var inst partIfaceDoc
	var hosts partIngressDoc
	_ = b.readPartFile(t, depIfaceFile, &inst)
	_ = b.readPartFile(t, depIngressFile, &hosts)
	out.IfaceInstances, out.IngressHosts = len(inst.Instances), len(hosts.Hosts)
	if p, err := b.partVaultPath(tile, t.dep, pkey); err == nil {
		if doc, ok, err := readPartVaultDoc(p); err == nil && ok {
			if m, err := b.openPartVault(doc); err == nil {
				out.VaultKeys = len(m)
			}
		}
	}
	return out
}
