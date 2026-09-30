package broker

// partitionbussubs.go — a person's partition's bus push subscriptions
// (plans/partitions/03 §D, 04 §2, 05 §1-§2; PD-13, PD-20): kept in the
// partition's own bus-subscriptions.json (partitionregs.go has the files,
// the liveness gate and the rules), at most partBusCap. An event stamped
// with the partition (its person's namespace of a partitioned scope's own
// bus, partitionbus.go) reaches its subscriptions, and may start it only
// when the partition itself published it — its backend, its person's
// frames, terminals and agent sessions on the tile; any other event (a
// shared bus's, an unpartitioned scope's, one another tile published for
// the person) reaches them only while the partition runs, never starting
// it (a skipped one is counted, dormantDrops). The global instance's events
// never reach people's partitions. A subscription to a partitioned scope's
// bus is a reach of that scope by the partition: it is registered, and
// each event queued and delivered, only while the partition reaches the
// same person's partition there (partSubReach — its person can read the
// scope and, with partitionConsent on, consented; a shared bus is no
// person's data and needs their read access alone, never consent). Such a
// reach of another partitioned tile's people's bus is counted in the
// partition's egress ledger, once at registration and once per delivery.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// ---- bus push subscriptions ----

// setPartSubs makes rows exactly t's subscriptions; one that stays keeps
// its counters and queue.
func (bs *busSubs) setPartSubs(t partTarget, rows []depBusRow) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	keep := map[string]bool{}
	owner := t.dep
	if owner == util.MainDeployment {
		owner = "" // the name rule, as main's rows are owned
	}
	for _, row := range rows {
		row.Role = cmp.Or(row.Role, "writer")
		if err := row.check(); err != nil {
			slog.Warn("bus subscriptions: dropping a partition's", "tile", t.tile, "partition", t.part, "name", row.Name, "err", err)
			continue
		}
		key := partKey(t, row.Name)
		keep[key] = true
		if st, ok := bs.part[key]; ok {
			st.sub = row.sub(t.tile)
			continue
		}
		bs.part[key] = &busSubState{sub: row.sub(t.tile), owner: owner, part: t.part, pkey: t.pkey}
	}
	prefix := partKey(t, "")
	for key, st := range bs.part {
		if strings.HasPrefix(key, prefix) && !keep[key] {
			st.gone, st.queue = true, nil
			delete(bs.part, key)
		}
	}
}

// rewritePartSubs changes t's bus-subscriptions.json and holds what it then
// lists.
func (bs *busSubs) rewritePartSubs(t partTarget, change func([]depBusRow) ([]depBusRow, error)) error {
	bs.fileMu.Lock()
	defer bs.fileMu.Unlock()
	if err := bs.b.partPausedErr(t); err != nil {
		return err
	}
	var doc partBusDoc
	if err := bs.b.readPartFile(t, depBusFile, &doc); err != nil {
		return err
	}
	rows, err := change(doc.Subscriptions)
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].Name < rows[k].Name })
	if err := bs.b.writePartFile(t, depBusFile, partBusDoc{t.head(), rows}, len(rows) == 0); err != nil {
		return err
	}
	bs.setPartSubs(t, rows)
	return nil
}

// partSubList is GET /bus/subscriptions for a person's partition: a row is
// dormant while the partition's registrations don't fire (PD-20) or it
// can't reach the bus's scope (partSubReach).
func (b *Broker) partSubList(w http.ResponseWriter, t partTarget) {
	dormant := !b.partRegFires(t)
	rows := []busSubView{}
	prefix := partKey(t, "")
	b.bus.mu.Lock()
	for key, st := range b.bus.part {
		if strings.HasPrefix(key, prefix) {
			rows = append(rows, busSubView{busSub: st.sub, busSubStats: st.stats, Dormant: dormant})
		}
	}
	b.bus.mu.Unlock()
	for i := range rows {
		_, err := b.partSubReach(t.tile, t.dep, t.part, rows[i].Resource)
		rows[i].Dormant = rows[i].Dormant || err != nil
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].Name < rows[k].Name })
	server.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": rows})
}

// putPartSub is PUT /bus/subscriptions for a person's partition: at most
// partBusCap, the subscriber reading the bus as today's, and the partition
// reaching the bus's scope as a call of it would (partSubReach).
func (b *Broker) putPartSub(w http.ResponseWriter, r *http.Request, t partTarget, s busSub) {
	if err := b.allowRes(auth.Principal{Component: t.tile}, s.Resource, "reader"); err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	edge, err := b.partSubReach(t.tile, t.dep, t.part, s.Resource)
	if err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/partitions.md")
		return
	}
	row := depBusRow{Name: s.Name, Resource: s.Resource, Prefix: s.Prefix, Path: s.Path, Role: cmp.Or(s.Role, "writer")}
	if err := b.bus.rewritePartSubs(t, func(rows []depBusRow) ([]depBusRow, error) {
		if !slices.ContainsFunc(rows, func(r depBusRow) bool { return r.Name == row.Name }) && len(rows) >= partBusCap {
			return nil, statusErr{http.StatusConflict, fmt.Sprintf("a person's partition of %s has at most %d bus subscriptions — delete one first", t.tile, partBusCap)}
		}
		return upsertRow(rows, row), nil
	}); err != nil {
		writeRegErr(w, err)
		return
	}
	b.partSubCount(t.tile, t.part, edge)
	writeRegOK(w, r, !b.partRegFires(t))
}

// deletePartSub is DELETE /bus/subscriptions/{name} for a person's
// partition.
func (b *Broker) deletePartSub(w http.ResponseWriter, r *http.Request, t partTarget, name string) {
	if err := b.bus.rewritePartSubs(t, func(rows []depBusRow) ([]depBusRow, error) {
		rows, ok := removeRow(rows, name)
		if !ok {
			return nil, statusErr{http.StatusNotFound, "no such subscription"}
		}
		return rows, nil
	}); err != nil {
		writeRegErr(w, err)
		return
	}
	writeRegOK(w, r, false)
}

// partSubReach is the reach rule of user partition part of tile
// (deployment dep, its primary) subscribing to bus resource (05 §1-§2,
// PD-13): on a partitioned scope's bus the partition must reach the same
// person's partition there, as a call or a data reach by it would —
// reachPartition's answer for its instance: on its own scope its own
// partition (its person live on the tile); on another tile's scope, when
// its person can read that scope's root and, while the workspace policy
// partitionConsent is on, consented (partitionConsentHolds). A shared bus
// ("shared": true | "read") is no person's data (F10's rule): another
// tile's partition reaches it on its person's read access alone, never
// their consent, in both policy settings. Asked at registration, at every
// publish and at every delivery, never cached, so a consent withdrawn or a
// read lost stops the deliveries at once. edge is the scope when the reach
// goes into another partitioned tile's people's data — what the partition's
// egress ledger counts (partSubCount) — "" otherwise. No error for a bus of
// a scope no tile partitions (today's rule: the tile's grant alone).
func (b *Broker) partSubReach(tile, dep string, part util.Partition, resource string) (edge string, err error) {
	rt, ok := b.resScope(resource)
	if !ok || rt.Scope == "" {
		return "", nil
	}
	root, isTile := b.Reg.Component(rt.Scope)
	switch {
	case !isTile:
		return "", nil
	case root.PartitionRecordUnknown():
		return "", fmt.Errorf("%s's partition mode can't be read: its bus reaches no person's partition until an admin repairs it", rt.Scope)
	}
	if _, partitioned := b.Reg.PartitionedScope(rt.Scope); !partitioned {
		return "", nil
	}
	c, found := b.Reg.Component(tile)
	own := found && c.Scope == rt.Scope
	owner := dep
	if owner == util.MainDeployment {
		owner = "" // an instance token of main names none (the name rule)
	}
	set, _ := b.declaredIn(rt.Scope, b.scopePrimary(rt.Scope))
	res, declared := set[rt.Name]
	shared := declared && sharedRes(res) // undeclared: per person, the stricter rule
	p := auth.Principal{Component: tile, Via: "instance", Deployment: owner, Partition: part}
	got, err := b.reachPartition(p, rt.Scope, own, shared)
	switch {
	case err != nil:
		return "", err
	case got != string(part):
		return "", fmt.Errorf("%s: %s reaches %q of %s, not its own person's partition", tile, part, got, rt.Scope)
	case own || shared:
		return "", nil
	}
	return rt.Scope, nil
}

// partSubCount counts one reach of another partitioned tile's people's
// data by part's subscription in the partition's egress ledger (06 §6.1),
// through the one ledger seam as the data plane's reaches are
// (countPartitionReach): a registration like a bind, each delivery like a
// call. Never under the bus's lock.
func (b *Broker) partSubCount(tile string, part util.Partition, edge string) {
	if user, ok := part.User(); ok && edge != "" {
		partitionEdgeSeam(b, user, tile, edge)
	}
}

// publishToParts queues one event for people's partitions' subscriptions:
// stamped with a person's partition (their own namespace's), it reaches that
// partition's subscriptions, starting it only when from — the tile whose
// credential published it — is the subscriber itself (the partition runs
// its own code: 03 §D); unstamped (a shared bus, an unpartitioned scope's),
// every matching one. Any delivery that may not start its partition is
// queued only while it runs, and every one only while the partition reaches
// the bus's scope (partSubReach). The caller holds bs.mu.
func (bs *busSubs) publishToParts(resource, topic, stamp, from string, where busNS, now time.Time, ev **busDelivery, data any) {
	for _, st := range bs.part {
		if st.sub.Resource != resource || !strings.HasPrefix(topic, st.sub.Prefix) {
			continue
		}
		cold := false
		if stamp != "" {
			if string(st.part) != stamp {
				continue
			}
			cold = from != "" && from == st.sub.Component
		} else if !where.reaches(st) {
			continue
		}
		t := partTarget{tile: st.sub.Component, dep: cmp.Or(st.owner, util.MainDeployment), pkey: st.pkey, part: st.part}
		fires := bs.b.partRegFires(t)
		if fires {
			_, err := bs.b.partSubReach(t.tile, t.dep, t.part, resource)
			fires = err == nil
		}
		if !fires {
			st.stats.DormantEvents++
			continue
		}
		if !cold && !bs.b.partitionRunning(st.sub.Component, st.pkey) {
			st.stats.DormantDrops++
			continue
		}
		bs.enqueue(st, now, ev, resource, topic, data, cold)
	}
}

// deliverPart POSTs one queued event to a person's partition's subscription,
// re-checking its liveness, that a delivery that may not start it finds it
// running, that its tile can still read the bus and that the partition
// still reaches the bus's scope ("refused": counted dormant, the reason its
// LastError). A failed delivery logs its status only: the body is the
// person's partition's (PD-46), kept in LastError, which only it lists.
func (bs *busSubs) deliverPart(dispatch BusDispatch, st busSubSnap, d busDelivery) (outcome, errText string) {
	b, sub := bs.b, st.sub
	if _, ok := b.Reg.Component(sub.Component); !ok {
		return "gone", ""
	}
	if dispatch == nil || b.Reg.LifecycleState(sub.Component) != registry.StateEnabled || b.partitionPaused(sub.Component, st.owner) {
		return "dropped", ""
	}
	dep := cmp.Or(st.owner, util.MainDeployment)
	if !b.partRegFires(partTarget{tile: sub.Component, dep: dep, pkey: st.pkey, part: st.part}) {
		return "dormant", ""
	}
	if !d.cold && !b.partitionRunning(sub.Component, st.pkey) {
		return "skipped", ""
	}
	if err := b.busOwnerMayRead(sub.Component, st.owner, sub.Resource); err != nil {
		return "failed", err.Error()
	}
	edge, err := b.partSubReach(sub.Component, dep, st.part, sub.Resource)
	if err != nil {
		return "refused", err.Error()
	}
	b.partSubCount(sub.Component, st.part, edge)
	body, err := json.Marshal(d)
	if err != nil {
		return "failed", err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), busSubTimeout)
	defer cancel()
	p := auth.Principal{Component: BusPrincipal, Via: "bus", Role: sub.Role, Deployment: st.owner, Partition: st.part}
	code, resp := dispatch(ctx, p, sub.Component, sub.Path, body)
	if code >= 400 {
		slog.Warn("bus delivery to a person's partition failed", "component", sub.Component, "partition", st.part, "subscription", sub.Name, "status", code)
		return "failed", fmt.Sprintf("%d %s", code, firstLine(resp))
	}
	return "ok", ""
}

// prunePartSub drops a partition's subscription whose tile is gone.
func (b *Broker) prunePartSub(st busSubSnap) {
	t := partTarget{tile: st.sub.Component, dep: cmp.Or(st.owner, util.MainDeployment), pkey: st.pkey, part: st.part}
	if err := b.bus.rewritePartSubs(t, func(rows []depBusRow) ([]depBusRow, error) {
		rows, _ = removeRow(rows, st.sub.Name)
		return rows, nil
	}); err != nil {
		slog.Warn("a partition's bus subscription of a gone component not pruned", "component", t.tile, "partition", t.part, "err", err)
	}
}

func init() {
	// a person's partition's own bus event reaches its subscriptions
	// (partitionbus.go's seam; 03 §D, 04 §2)
	publishPartitionPushSeam = func(b *Broker, ra reach, from, topic string, data any) {
		b.bus.publishStamped(ra.rt.String(), ra.dep, topic, data, ra.part, from)
	}
}
