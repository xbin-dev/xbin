package broker

// deployseed.go — seeding a deployment's data (08-data §8; 06-security T8)
// (D127i) (D127t): an optional, confirmed act of a tile manager of every
// claimant tile, in a person's own session, that replaces the (scope, name)
// namespace a deployment claims with a copy of the scope primary's,
// re-keyed under the target's own labels. The primary's namespace is only
// read (PO-2): kv in process, spooled still encrypted and re-encoded in
// bounded transactions; sqlite through python3's backup API and filesystem
// through rsync, both confined (L10, L11); a single-tenant volume as a
// ciphertext copy re-wrapped under the target's label; blob in process,
// opened beneath its volume (deployseed_copy.go holds the file-backed
// copies). Preflight, hold, stop, wipe, copy, verify, commit (§8.2); any
// failure leaves the target partial (§8.5).

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// The bounds of a kv copy (08-data §8.3): a read transaction of the
// primary's store holds at most seedReadKeys keys or seedReadBytes bytes, so
// no long read holds up kv.db's remaps; a write transaction of the target's
// at most seedWriteKeys keys or seedWriteBytes bytes.
var (
	seedReadKeys, seedReadBytes   = 10000, 4 << 20
	seedWriteKeys, seedWriteBytes = 1000, 8 << 20
)

const (
	seedOnline, seedStopped = "online", "stopped" // ns.json's consistency (§8.4)
	seedSpool               = "seed.spool"        // the kv copy's spool, in the target's namespace root
	nsCopied                = "copied"            // the primary's hold while a stopped seed copies it: in memory only
)

// seedDiskFree is the workspace disk's free and total bytes (diskFree).
var seedDiskFree = diskFree

// SeedFacts are what a seed does (08-data §8.1's warning, from facts xbind
// computes): the namespace and where it is copied from; the claimant tiles
// and the deployments that stop, as <tile>+<name> (the primary's too when
// stopped); the bytes of the primary's data and the resources it copies;
// those that start empty (only the target declares them) and those skipped
// (only the primary declares them, or with another type); online or
// stopped, why stopped is required, the primary's estimated downtime. Done
// is closed once a committed seed has finished (nil for a dry run); the
// outcome is the namespace's data state and the deployments event op data.
type SeedFacts struct {
	Scope       string   `json:"scope"`
	From        string   `json:"from"`
	Deployment  string   `json:"deployment"`
	Claimants   []string `json:"claimants"`
	Stops       []string `json:"stops"`
	Bytes       int64    `json:"bytes"`
	Copies      []string `json:"copies"`
	Empty       []string `json:"empty"`
	Skipped     []string `json:"skipped"`
	Consistency string   `json:"consistency"`
	StopWhy     string   `json:"stopWhy,omitempty"`
	Downtime    int      `json:"downtimeSeconds,omitempty"`

	Done <-chan struct{} `json:"-"`
}

// seedRes is one resource a seed copies: its keys in the primary's
// namespace and in the target's; single marks a single-tenant filesystem
// volume, copied as ciphertext while stopped.
type seedRes struct {
	name, typ string
	from, to  resKeys
	single    bool
}

// seedPlan is a judged seed.
type seedPlan struct {
	facts        SeedFacts
	tile, by, at string
	id, src      nsID
	res          []seedRes
	checkpoint   string // the primary's code: "c:<tree>", or "worktree"
}

// seedStep is a failure and the step it happened at, for ns.json.
type seedStep struct {
	step string
	err  error
}

func (e *seedStep) Error() string { return e.step + ": " + e.err.Error() }

func failAt(step string, err error) error { return &seedStep{step, err} }

// SeedDeploymentData seeds the namespace deployment req.Deployment of
// req.Tile claims from the scope primary's (08-data §8; 11-contract §1.8),
// for the plane's seed op and an add with data:"seed". It judges the manager
// gate again, on the tile and on every other claimant, where authorize adds
// the plane's judgement (D127t); stop stops one deployment of a tile. A dry
// run answers the facts and changes nothing. Otherwise the namespace is
// held and marked busy before this returns, and the copy runs in the
// background (SeedFacts.Done). Errors are *deployments.Error.
func (b *Broker) SeedDeploymentData(p auth.Principal, req deployments.SeedRequest,
	authorize func(tile string) error, stop func(tile, dep string)) (SeedFacts, error) {
	pl, err := b.planSeed(p, req, authorize)
	if err != nil || req.DryRun {
		return pl.facts, err
	}
	release, err := b.holdNS(pl.id, nsSeeding)
	if err != nil {
		return SeedFacts{}, err
	}
	releaseSrc := func() {}
	if pl.facts.Consistency == seedStopped {
		if releaseSrc, err = b.holdNS(pl.src, nsCopied); err != nil {
			release()
			return SeedFacts{}, err
		}
	}
	if err := b.updateNS(pl.id, true, func(m *nsMeta) {
		m.Busy, m.From, m.At, m.By = nsSeeding, pl.src.dep, pl.at, pl.by
	}); err != nil {
		releaseSrc()
		release()
		return SeedFacts{}, nsErr(http.StatusInternalServerError, "", "seeding "+pl.id.dep+": "+err.Error())
	}
	done := make(chan struct{})
	pl.facts.Done = done
	b.publishSeed(pl)
	go func() {
		defer close(done)
		sizes, err := b.runSeed(pl, stop)
		b.commitSeed(pl, sizes, err)
		releaseSrc()
		release()
		b.publishSeed(pl)
		if b.WakeBackends != nil {
			b.WakeBackends() // the always-on backends the seed stopped
		}
	}()
	return pl.facts, nil
}

// planSeed judges a seed and computes its facts; it creates nothing.
func (b *Broker) planSeed(p auth.Principal, req deployments.SeedRequest, authorize func(string) error) (pl *seedPlan, err error) {
	const what = "seeding a deployment's data"
	pl = &seedPlan{}
	fail := func(status int, kind, msg string) (*seedPlan, error) { return pl, nsErr(status, kind, msg) }
	manager := func(t string) error {
		if !b.MayManageDeployments(p, t) {
			return nsErr(http.StatusForbidden, deployments.KindAuthority, what+" is a tile manager's act: the tile's owner, its org's admins, or a workspace admin")
		}
		return nil
	}
	tile, dep := req.Tile, cmp.Or(req.Deployment, util.MainDeployment)
	switch {
	case p.ReadOnly():
		return fail(http.StatusForbidden, deployments.KindAuthority, what+" is refused in a view-as session: it is read-only")
	case p.Component != "":
		return fail(http.StatusForbidden, deployments.KindAuthority, what+
			" is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it")
	case manager(tile) != nil:
		return pl, manager(tile)
	case !util.DeploymentNameOK(dep):
		return fail(http.StatusBadRequest, "", `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`)
	}
	c, ok := b.Reg.Component(tile)
	switch {
	case !ok:
		return fail(http.StatusNotFound, "", "no such tile: "+tile)
	case !b.claims(tile, dep):
		return fail(http.StatusNotFound, "", util.NoDeployment(tile, dep).Error())
	case c.Scope == "":
		return fail(http.StatusConflict, deployments.KindPolicy, tile+" is in the workspace scope, whose resources have one namespace: "+
			dep+" has no data of its own to seed")
	}
	id, src := nsOf(c.Scope, dep), nsOf(c.Scope, b.scopePrimary(c.Scope))
	claimants := b.claimants(c.Scope, dep)
	for _, t := range claimants {
		if b.primaryOf(t) == dep {
			return fail(http.StatusConflict, deployments.KindState, dep+" is the primary of "+t)
		}
	}
	if req.Confirm != deployments.ConfirmCopyData {
		return fail(http.StatusBadRequest, "", fmt.Sprintf(`seeding %s copies %s's data, which may be personal: send confirm:%q to proceed`,
			dep, src.dep, deployments.ConfirmCopyData))
	}
	for _, t := range claimants {
		if t == tile {
			continue
		}
		if err := judgeClaimant("seed", "a tile manager", id, t, func(t string) error {
			if err := manager(t); err != nil || authorize == nil {
				return err
			}
			return authorize(t)
		}); err != nil {
			return pl, err
		}
	}
	tk, err := scopeKeys(id.scope, id.dep)
	if err != nil {
		return fail(http.StatusConflict, deployments.KindPolicy, tile+" can't have non-primary deployments: "+err.Error())
	}
	if sk, err := scopeKeys(src.scope, src.dep); err != nil || tk.NS == sk.NS || tk.DirKey == sk.DirKey || tk.Enc == sk.Enc {
		return fail(http.StatusConflict, deployments.KindPolicy, dep+" is "+src.dep+": the primary's data is never a seed's target") // keys, not names (T8.2)
	}
	for _, n := range []nsID{id, src} {
		if act := b.busyAct(n); act != "" {
			return pl, nsBusy(n.dep, act)
		}
	}
	*pl = seedPlan{tile: tile, by: p.From(), at: nowStamp(time.Now()), id: id, src: src, checkpoint: b.seedCheckpoint(c.Scope, src.dep),
		facts: SeedFacts{Scope: c.Scope, From: src.dep, Deployment: dep, Claimants: claimants, Copies: []string{}, Empty: []string{}, Skipped: []string{}}}
	b.planResources(pl)
	return pl, b.preflight(pl, req.Stop)
}

// seedKinds are the resource types whose data a seed copies; a bus and
// cron resources hold none (§8.3).
var seedKinds = map[string]bool{"kv": true, "sqlite": true, "filesystem": true, "blob": true}

// planResources lists what the seed copies: each resource the primary's
// code and the target's declare with one type (D127n); the rest start empty
// or are skipped (08-data §6.7).
func (b *Broker) planResources(pl *seedPlan) {
	from, errF := b.declaredIn(pl.id.scope, pl.src.dep)
	to, errT := b.declaredIn(pl.id.scope, pl.id.dep)
	if err := errors.Join(errF, errT); err != nil {
		slog.Warn("seed: declarations read with errors", "scope", pl.id.scope, "from", pl.src.dep, "to", pl.id.dep, "err", err)
	}
	names := append(slices.Collect(maps.Keys(from)), slices.Collect(maps.Keys(to))...)
	slices.Sort(names)
	f := &pl.facts
	for _, n := range slices.Compact(names) {
		fr, inF := from[n]
		tr, inT := to[n]
		switch {
		case inF && inT && fr.Type == tr.Type && seedKinds[fr.Type]:
			rt := resTarget{Scope: pl.id.scope, Name: n}
			fk, err1 := b.resKeys(rt, pl.src.dep)
			tk, err2 := b.resKeys(rt, pl.id.dep)
			if errors.Join(err1, err2) != nil || strings.ContainsAny(n, `*?[\`) {
				f.Skipped = append(f.Skipped, n) // a name no key or copy takes
				continue
			}
			single := fr.Type == "filesystem" && b.resSingleTenant(pl.id.scope, fr.Type)
			pl.res = append(pl.res, seedRes{name: n, typ: fr.Type, from: fk, to: tk, single: single})
			f.Copies = append(f.Copies, n)
		case inF && inT && fr.Type == tr.Type: // bus, cron: nothing to copy
		case !inF && seedKinds[tr.Type]:
			f.Empty = append(f.Empty, n)
		case inF && (inT || seedKinds[fr.Type]):
			f.Skipped = append(f.Skipped, n)
		}
	}
}

// preflight checks what the copy needs (§8.2 step 1, §8.4, §12): keys, the
// disk, and whether the primary must stop, which only stop:true asks for.
func (b *Broker) preflight(pl *seedPlan, stop bool) error {
	f, needFS := &pl.facts, false
	for _, r := range pl.res {
		f.Bytes += b.seedSize(r)
		needFS = needFS || fileBackedType(r.typ) && b.resenc != nil && b.resenc.Encrypted(r.from.DirKey, r.from.Name)
	}
	limit, state := int64(defaultQuotaBytes), deployments.KindState
	if b.disk != nil {
		limit = b.disk.quota
	}
	free, total := seedDiskFree(b.Reg.Root)
	switch {
	case b.barrier != nil && b.barrier.Initialized() && b.barrier.Sealed() && len(pl.res) > 0:
		return nsErr(http.StatusServiceUnavailable, state, "vault is sealed — unseal it first (bx vault unseal, or the admin console)")
	case needFS && !b.encryptionReady():
		return nsErr(http.StatusServiceUnavailable, state, pl.src.dep+"'s file resources can't be copied now: encryption isn't ready (gocryptfs missing, or the vault not unsealed)")
	case f.Bytes > limit:
		return nsErr(http.StatusConflict, state, fmt.Sprintf("%s's data (%s) is over %s's disk limit (%s): it can't be seeded",
			pl.src.dep, humanBytes(f.Bytes), pl.id.dep, humanBytes(limit)))
	case total > 0 && float64(free)-1.1*float64(f.Bytes) < reserveFraction*float64(total):
		return nsErr(http.StatusConflict, state, fmt.Sprintf("the workspace disk is low (%s free of %s): a seed of %s would eat into its %d%% reserve",
			humanBytes(free), humanBytes(total), humanBytes(f.Bytes), int(reserveFraction*100)))
	}
	for _, r := range pl.res {
		if r.single && f.StopWhy == "" {
			f.StopWhy = r.name + " is a container store (a single-tenant volume), which is copied only while " + pl.src.dep + " is stopped"
		}
	}
	primaries := b.claimants(pl.src.scope, pl.src.dep)
	for _, t := range primaries {
		if c, ok := b.Reg.Component(t); ok && f.StopWhy == "" && c.Manifest.VM.Enabled() && b.componentUsesFileRes(c, pl.src.dep) {
			f.StopWhy = t + " runs " + pl.src.dep + " in a VM with file resources, whose cached pages the host can't see"
		}
	}
	if f.StopWhy != "" && !stop {
		return nsErr(http.StatusConflict, state, fmt.Sprintf("seeding %s needs %s stopped for the copy: %s — send stop:true to proceed",
			pl.id.dep, pl.src.dep, f.StopWhy))
	}
	f.Consistency = map[bool]string{false: seedOnline, true: seedStopped}[stop]
	for _, t := range f.Claimants {
		f.Stops = append(f.Stops, t+"+"+pl.id.dep)
	}
	if stop {
		for _, t := range primaries {
			f.Stops = append(f.Stops, t+"+"+pl.src.dep)
		}
		f.Downtime = 5 + int(f.Bytes/(64<<20)) // an estimate, at 64 MiB/s
	}
	return nil
}

// seedSize is what r holds in the primary's namespace: its ciphertext, or
// its kv bucket's pages.
func (b *Broker) seedSize(r seedRes) (n int64) {
	if r.typ != "kv" {
		n, _ = dirUsage(b.resenc.CipherDir(r.from.DirKey, r.from.Name)) // cipher dirs are xbind's own
		return n
	}
	if db, err := b.kvDB(r.from, false); err == nil {
		_ = kvView(db, func(tx *bolt.Tx) error {
			if bk := tx.Bucket([]byte(r.from.Bucket)); bk != nil {
				s := bk.Stats()
				n = int64(s.BranchInuse + s.LeafInuse)
			}
			return nil
		})
	}
	return n
}

// seedCheckpoint is the scope primary's code, ns.json's fromCheckpoint:
// "c:<tree>" while pinned, "worktree" while it follows the work tree (and
// for a scope no tile roots), "" when it can't be told.
func (b *Broker) seedCheckpoint(scope, dep string) string {
	root, ok := b.Reg.Component(scope)
	if !ok || b.DeploymentCodeRoot == nil {
		return "worktree"
	}
	switch dir, pinned, err := b.DeploymentCodeRoot(root, dep); {
	case err != nil:
		return ""
	case !pinned:
		return "worktree"
	case len(filepath.Base(dir)) >= 40 && strings.Trim(filepath.Base(dir), "0123456789abcdef") == "":
		return "c:" + filepath.Base(dir) // a materialized tree's directory is its id
	}
	return ""
}

// ---- the copy ----

// runSeed stops what addresses the namespace, wipes it and copies each
// resource, verifying as it goes (§8.2 steps 2–6); stopped, the primary's
// deployments stop too and its write gate is held throughout. It answers
// the bytes copied per resource.
func (b *Broker) runSeed(pl *seedPlan, stop func(tile, dep string)) (map[string]int64, error) {
	online := pl.facts.Consistency == seedOnline
	for _, ref := range pl.facts.Stops {
		if i := strings.LastIndexByte(ref, '+'); stop != nil && i > 0 {
			stop(ref[:i], ref[i+1:])
		}
	}
	if !online {
		g := b.nsTab().gate(pl.src)
		g.Lock()
		defer g.Unlock()
	}
	if err := b.wipe(pl.id); err != nil {
		return nil, failAt("wipe", err)
	}
	sizes, kvs := map[string]int64{}, []seedRes{}
	for _, r := range pl.res {
		if r.typ == "kv" {
			kvs = append(kvs, r)
		}
	}
	if err := b.seedKV(pl, kvs, online, sizes); err != nil {
		return sizes, err
	}
	for _, r := range pl.res {
		var n int64
		var err error
		switch {
		case r.typ == "kv":
			continue
		case r.single:
			n, err = b.seedCipher(pl.id.scope, r)
		case r.typ == "blob":
			n, err = b.seedBlob(pl.id.scope, r)
		default:
			n, err = b.seedFiles(pl.id.scope, r)
		}
		if err != nil {
			return sizes, failAt("copying "+r.name+" ("+r.typ+")", err)
		}
		sizes[r.name] = n
	}
	return sizes, nil
}

// commitSeed writes the outcome (§8.2 step 7, §8.5), the namespace still
// held: seeded with its provenance, or partial naming the failing step,
// which keeps the deployments addressing it from starting.
func (b *Broker) commitSeed(pl *seedPlan, sizes map[string]int64, err error) {
	now := nowStamp(time.Now())
	if err == nil {
		if err = b.updateNS(pl.id, true, func(m *nsMeta) {
			*m = nsMeta{State: nsSeeded, From: pl.src.dep, FromCheckpoint: pl.checkpoint, At: pl.at, By: pl.by,
				Consistency: pl.facts.Consistency, Resources: sizes, Skipped: pl.facts.Skipped,
				History: append(m.History, nsEvent{Op: "seed", At: now, By: pl.by})}
		}); err == nil {
			var total int64
			for _, n := range sizes {
				total += n
			}
			slog.Info("deployment data seeded", "tile", pl.tile, "scope", pl.id.scope, "from", pl.src.dep, "to", pl.id.dep,
				"by", pl.by, "bytes", total, "resources", pl.facts.Copies, "skipped", pl.facts.Skipped, "consistency", pl.facts.Consistency)
			return
		}
		err = failAt("commit", err)
	}
	step, msg := "copy", err.Error()
	if se := (*seedStep)(nil); errors.As(err, &se) {
		step, msg = se.step, se.err.Error()
	}
	msg = msg[:min(len(msg), 500)]
	slog.Warn("deployment data seed failed: left partial", "tile", pl.tile, "scope", pl.id.scope, "from", pl.src.dep,
		"to", pl.id.dep, "by", pl.by, "step", step, "err", msg)
	if err := b.updateNS(pl.id, true, func(m *nsMeta) {
		m.State, m.Busy, m.Failed, m.Step, m.Error = nsPartial, "", "seed", step, msg
		m.From, m.FromCheckpoint, m.At, m.By, m.Consistency = pl.src.dep, pl.checkpoint, pl.at, pl.by, pl.facts.Consistency
		m.History = append(m.History, nsEvent{Op: "partial", At: now, By: pl.by, Error: msg})
	}); err != nil {
		slog.Error("deployment data: the failed seed's state wasn't written", "scope", pl.id.scope, "deployment", pl.id.dep, "err", err)
	}
}

// publishSeed announces the namespace's data state to each claimant tile:
// the server delivers op data naming a non-primary deployment only to the
// tile's writers and that deployment's own principals (§8.1).
func (b *Broker) publishSeed(pl *seedPlan) {
	st := b.DeploymentData(pl.tile, pl.id.dep)
	if b.Hub == nil || st == nil {
		return
	}
	for _, t := range pl.facts.Claimants {
		b.Hub.Publish(events.Event{Type: "deployments", Component: t,
			Data: map[string]string{"op": "data", "deployment": pl.id.dep, "busy": st.Busy, "state": st.State}}) // 11-contract §3.3
	}
}

// ---- kv: in process, never plaintext on disk ----

// seedKV copies the kv resources (§8.3). Under the primary's write gate its
// raw pairs, still encrypted, are spooled into a bbolt file in the target's
// namespace root in bounded read transactions: all the scope's buckets at
// one moment. Then each value is decoded under the primary's label and
// encoded under the target's, written in bounded transactions, and read
// back: key counts match and every value round-trips. Any decode error
// fails the seed. online takes the gate here; a stopped seed holds it.
func (b *Broker) seedKV(pl *seedPlan, kvs []seedRes, online bool, sizes map[string]int64) error {
	if len(kvs) == 0 {
		return nil
	}
	dir, err := b.nsDir(pl.id)
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	var spool *bolt.DB
	if err == nil {
		p := filepath.Join(dir, seedSpool)
		defer os.Remove(p)
		spool, err = bolt.Open(p, 0o600, &bolt.Options{Timeout: time.Second, NoSync: true})
	}
	if err != nil {
		return failAt("spooling kv", err)
	}
	defer spool.Close()
	counts := make([]int, len(kvs))
	if err := func() error {
		if online {
			g := b.nsTab().gate(pl.src)
			g.Lock()
			defer g.Unlock()
		}
		for i, r := range kvs {
			db, err := b.kvDB(r.from, false)
			if err == nil {
				err = eachKV(db, r.from.Bucket, seedReadKeys, seedReadBytes, func(ps []kvPair) error {
					counts[i] += len(ps)
					return putPairs(spool, strconv.Itoa(i), ps)
				})
			}
			if err != nil {
				return err
			}
		}
		return nil
	}(); err != nil {
		return failAt("spooling kv", err)
	}
	for i, r := range kvs {
		if err := b.copyKV(spool, strconv.Itoa(i), r, counts[i], sizes); err != nil {
			return err
		}
	}
	return nil
}

// copyKV re-encodes r's spooled pairs into the target's store (main's, main
// not the primary, has its bucket emptied first), each value checked to
// round-trip under the target's label, and the want spooled keys counted in
// the copy.
func (b *Broker) copyKV(spool *bolt.DB, bucket string, r seedRes, want int, sizes map[string]int64) error {
	db, err := b.kvDB(r.to, want > 0) // nothing to write makes no kv file
	if err == nil && r.to.KVFile == "" && db != nil {
		err = db.Update(func(tx *bolt.Tx) error {
			if err := tx.DeleteBucket([]byte(r.to.Bucket)); !errors.Is(err, bolt.ErrBucketNotFound) {
				return err
			}
			return nil
		})
	}
	if err == nil { // a spool batch is a write transaction's worth (the values grow by a tag and a nonce)
		err = eachKV(spool, bucket, seedWriteKeys, seedWriteBytes-64<<10, func(ps []kvPair) error {
			for j := range ps {
				plain, err := b.decodeKV(r.from.KVLabel, ps[j].v)
				if err != nil {
					return fmt.Errorf("a value doesn't decode under the primary's key: %w", err)
				}
				if ps[j].v, err = b.encodeKV(r.to.KVLabel, plain); err != nil {
					return err
				}
				if back, err := b.decodeKV(r.to.KVLabel, ps[j].v); err != nil || !bytes.Equal(back, plain) {
					return fmt.Errorf("verifying: a value doesn't round-trip under %s's key", r.to.NS)
				}
				sizes[r.name] += int64(len(ps[j].k) + len(ps[j].v))
			}
			return putPairs(db, r.to.Bucket, ps)
		})
	}
	n := 0
	if err == nil {
		err = kvView(db, func(tx *bolt.Tx) error {
			if bk := tx.Bucket([]byte(r.to.Bucket)); bk != nil {
				n = bk.Stats().KeyN
			}
			return nil
		})
	}
	if err == nil && n != want {
		err = fmt.Errorf("verifying: the copy holds %d keys, the primary %d", n, want)
	}
	if err != nil {
		return failAt("copying "+r.name+" (kv)", err)
	}
	return nil
}

// kvPair is a key and its stored value, copied out of a transaction.
type kvPair struct{ k, v []byte }

// eachKV calls fn with bucket's pairs in db in key order, a batch per read
// transaction of at most maxKeys keys or maxBytes bytes. A nil db (a
// namespace nothing wrote to) or a missing bucket has none.
func eachKV(db *bolt.DB, bucket string, maxKeys, maxBytes int, fn func([]kvPair) error) error {
	var after []byte
	for more := db != nil; more; {
		var ps []kvPair
		more = false
		if err := db.View(func(tx *bolt.Tx) error {
			bk := tx.Bucket([]byte(bucket))
			if bk == nil {
				return nil
			}
			c := bk.Cursor()
			k, v := c.First()
			if after != nil {
				if k, v = c.Seek(after); bytes.Equal(k, after) {
					k, v = c.Next()
				}
			}
			for size := 0; k != nil; k, v = c.Next() {
				if len(ps) == maxKeys || size >= maxBytes {
					more = true
					break
				}
				if v != nil { // a nested bucket holds nothing kv reaches
					ps, size = append(ps, kvPair{bytes.Clone(k), bytes.Clone(v)}), size+len(k)+len(v)
				}
				after = append(after[:0], k...)
			}
			return nil
		}); err != nil {
			return err
		}
		if len(ps) > 0 {
			if err := fn(ps); err != nil {
				return err
			}
		}
	}
	return nil
}

// putPairs writes ps into bucket of db in one transaction.
func putPairs(db *bolt.DB, bucket string, ps []kvPair) error {
	return db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(bucket))
		for _, p := range ps {
			if err == nil {
				err = bk.Put(p.k, p.v)
			}
		}
		return err
	})
}
