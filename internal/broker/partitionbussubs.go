package broker

// partitionbussubs.go — a person's partition's bus push subscriptions
// (plans/partitions/03 §D, 04 §2; PD-20): kept in the partition's own
// bus-subscriptions.json (partitionregs.go has the files, the liveness gate
// and the rules), at most partBusCap. An event the partition publishes on
// its scope's own bus — stamped with it (partitionbus.go) — reaches its
// subscriptions and may start it; a shared bus's, an unpartitioned scope's
// or another scope's reaches them only while the partition runs, never
// starting it (a skipped one is counted, dormantDrops). The global
// instance's events never reach people's partitions.

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

// partSubList is GET /bus/subscriptions for a person's partition.
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
	sort.Slice(rows, func(i, k int) bool { return rows[i].Name < rows[k].Name })
	server.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": rows})
}

// putPartSub is PUT /bus/subscriptions for a person's partition: at most
// partBusCap, the subscriber reading the bus as today's.
func (b *Broker) putPartSub(w http.ResponseWriter, r *http.Request, t partTarget, s busSub) {
	if err := b.allowRes(auth.Principal{Component: t.tile}, s.Resource, "reader"); err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
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

// publishToParts queues one event for people's partitions' subscriptions:
// stamped with a person's partition (their own namespace's), it reaches that
// partition's subscriptions, starting it when the bus is the subscriber's
// own scope's; unstamped (a shared bus, an unpartitioned scope's), every
// matching one, only while its partition runs. The caller holds bs.mu.
func (bs *busSubs) publishToParts(resource, topic, stamp string, where busNS, now time.Time, ev **busDelivery, data any) {
	for _, st := range bs.part {
		if st.sub.Resource != resource || !strings.HasPrefix(topic, st.sub.Prefix) {
			continue
		}
		cold := false
		if stamp != "" {
			if string(st.part) != stamp {
				continue
			}
			c, ok := bs.b.Reg.Component(st.sub.Component)
			cold = ok && c.Scope == where.scope
		} else if !where.reaches(st) {
			continue
		}
		t := partTarget{tile: st.sub.Component, dep: cmp.Or(st.owner, util.MainDeployment), pkey: st.pkey, part: st.part}
		if !bs.b.partRegFires(t) {
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
// running, and that its tile can still read the bus.
func (bs *busSubs) deliverPart(dispatch BusDispatch, st busSubSnap, d busDelivery) (outcome, errText string) {
	b, sub := bs.b, st.sub
	if _, ok := b.Reg.Component(sub.Component); !ok {
		return "gone", ""
	}
	if dispatch == nil || b.Reg.LifecycleState(sub.Component) != registry.StateEnabled || b.partitionPaused(sub.Component, st.owner) {
		return "dropped", ""
	}
	if !b.partRegFires(partTarget{tile: sub.Component, dep: cmp.Or(st.owner, util.MainDeployment), pkey: st.pkey, part: st.part}) {
		return "dormant", ""
	}
	if !d.cold && !b.partitionRunning(sub.Component, st.pkey) {
		return "skipped", ""
	}
	if err := b.busOwnerMayRead(sub.Component, st.owner, sub.Resource); err != nil {
		return "failed", err.Error()
	}
	body, err := json.Marshal(d)
	if err != nil {
		return "failed", err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), busSubTimeout)
	defer cancel()
	p := auth.Principal{Component: BusPrincipal, Via: "bus", Role: sub.Role, Deployment: st.owner, Partition: st.part}
	code, resp := dispatch(ctx, p, sub.Component, sub.Path, body)
	if code >= 400 {
		slog.Warn("bus delivery failed", "component", sub.Component, "partition", st.part, "subscription", sub.Name,
			"status", code, "body", firstLine(resp))
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
	publishPartitionPushSeam = func(b *Broker, ra reach, topic string, data any) {
		b.bus.publishStamped(ra.rt.String(), ra.dep, topic, data, ra.part)
	}
}
