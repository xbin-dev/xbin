package proxy

// partitionroute.go — partitioned tiles on the /api/<tile>/… path
// (plans/partitions/02 §6-§7). Route (the broker) decides which partition a
// call reaches and which one the caller acts in; the proxy
//
//   - tells the callee the caller's partition: X-XBin-Partition (the key,
//     a display name) and X-XBin-Partition-Id (the opaque partition id, user
//     partitions only) — never from the request: every inbound X-XBin-* is
//     stripped first (identify);
//   - for an F5 call from a user partition to its own tile's global instance
//     (05 §6, the F9 pack), attributes it to the partition's person;
//   - starts the reached user partition through the runner's
//     EnsurePartition, tracking it as the runner's TrackPartition asks: a
//     text/event-stream response alone is passive (03 §A.5).
//
// A call between two unpartitioned ends carries no new header and takes
// today's EnsureDeployment/TrackDeployment path; so does a call reaching a
// partitioned tile's global instance, which is today's instance (PD-04).

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
)

const (
	// HeaderPartition names the partition the caller acts in (02 §6): its
	// own partition for a partitioned tile's principals (instances, frames,
	// terminals, tickets, its cron/bus/mail deliveries), including on calls
	// to tiles that aren't partitioned; user:<id> for a person calling a
	// partitioned tile directly; global for a global instance's outbound
	// calls and the root token reaching a global instance. Absent for
	// everything else.
	HeaderPartition = "X-XBin-Partition"
	// HeaderPartitionID is the caller's partition id (a "u-" + 32 hex
	// pkey), for user partitions only: opaque, stable for the person's
	// incarnation, so a recreated person never inherits a provider's
	// records of the old one. Providers key per-caller state on (From,
	// Partition-Id).
	HeaderPartitionID = "X-XBin-Partition-Id"
)

// PartitionStart is why a user partition is started (03 §A.5), the
// runner's start class by name: a cron or bus delivery's start is
// background, a mail doorbell's background and counted against the tile's
// mail start rate, everything else interactive.
type PartitionStart uint8

const (
	StartInteractive PartitionStart = iota
	StartBackground
	StartMail
)

// startOf is the start class of a call Route decided.
func startOf(d Decision) PartitionStart {
	switch d.Delivery {
	case "":
		return StartInteractive
	case "mail":
		return StartMail
	}
	return StartBackground
}

// PartitionRunner is the runner's side of user partitions (03 §A.2), part
// "user:<id>" (string(util.Partition)): EnsurePartition starts (or reuses)
// partition part of deployment dep of c and answers its generation
// (PartitionGen); TrackPartition holds it while a request runs, a passive
// hold (a live event stream) not counting as use. An admission refusal
// wraps sbx.ErrRefused (503). Boot installs an adapter over the runner's
// methods (internal/boot/partitionroute.go); without it no user partition
// runs here, and a call reaching one answers 503.
type PartitionRunner interface {
	EnsurePartition(ctx context.Context, c *registry.Component, dep, part string, class PartitionStart) (PartitionGen, error)
	TrackPartition(tile, dep, part string, passive bool) func()
}

// PartitionGen is the generation of a person's partition a request is sent
// to: its socket, and whether xbind has retired it since (the runner's
// Gen). A request a swap of that partition cut off goes to the generation
// that replaced it, as one to a deployment does (rerouting, D173).
type PartitionGen interface {
	Sock() string
	Retired() bool
}

// errNoPartitionRunner answers a call reaching a user partition on an
// xbind whose runner can't start one.
var errNoPartitionRunner = sbx.Refuse(errors.New("a person's partition can't run on this xbind: it has no partition runner"))

// identifyPartition sets the partition headers of d on r, after identify
// stripped every inbound X-XBin-* and set today's identity; nothing for a
// call between unpartitioned ends.
func (px *Proxy) identifyPartition(r *http.Request, d Decision) {
	if cp := d.CallerPartition; cp != "" {
		r.Header.Set(HeaderPartition, string(cp))
		if id := d.CallerPartitionID; id != "" && cp.IsUser() {
			r.Header.Set(HeaderPartitionID, id)
		}
	}
	if a := d.Attribute; a != nil { // F5 from a user partition: its person, never the tile itself
		r.Header.Set(HeaderUser, a.UserID)
		r.Header.Set(HeaderRole, a.Role)
		r.Header.Del(HeaderUserLevel)
		if a.Level != "" {
			r.Header.Set(HeaderUserLevel, a.Level)
		}
	}
}

// backendHold is a proxied call's hold on the instance answering it.
type backendHold struct {
	mu         sync.Mutex
	release    func()
	onResponse func(*http.Response) // nil: the hold never changes
}

// done releases the hold the call has now.
func (h *backendHold) done() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.release()
}

// ensureTarget starts (or reuses) the instance d reaches: a user partition
// through the partition runner, everything else — every unpartitioned
// tile, a partitioned tile's global instance — as today. It answers the
// request's transport and the call's hold on that instance. A request the
// generation's retirement cut off (a swap, D173) goes again to the
// generation that instance has now: the same deployment and the same
// partition, through the same ensure (a user partition's gates and
// admission included), never another. The hold stays valid across that:
// the runner tracks a deployment or a partition, not a generation.
func (px *Proxy) ensureTarget(ctx context.Context, comp *registry.Component, target string, d Decision) (*rerouting, *backendHold, error) {
	if !d.Partition.IsUser() {
		// Every unpartitioned tile's one instance, and a partitioned tile's
		// global one (today's instance, PD-04).
		again := func() (generation, error) {
			// deployment: the target Route returned.
			return px.Runner.EnsureDeploymentGen(ctx, comp, target)
		}
		gen, err := again()
		if err != nil {
			return nil, nil, err
		}
		return &rerouting{px: px, gen: gen, again: again, d: d}, &backendHold{release: px.Runner.TrackDeployment(comp.Path, target)}, nil
	}
	if px.Partitions == nil {
		return nil, nil, errNoPartitionRunner
	}
	part, class := string(d.Partition), startOf(d)
	again := func() (generation, error) {
		return px.Partitions.EnsurePartition(ctx, comp, target, part, class)
	}
	gen, err := again()
	if err != nil {
		return nil, nil, err
	}
	h := &backendHold{release: px.Partitions.TrackPartition(comp.Path, target, part, false)}
	h.onResponse = func(res *http.Response) {
		if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
			return
		}
		// a live stream alone doesn't keep a partition in use: take the
		// passive hold before dropping the active one, so the instance is
		// never unheld in between
		passive := px.Partitions.TrackPartition(comp.Path, target, part, true)
		h.mu.Lock()
		active := h.release
		h.release = passive
		h.mu.Unlock()
		active()
	}
	return &rerouting{px: px, gen: gen, again: again, d: d}, h, nil
}

// ensureStatus is the status of a failed ensure of a user partition: a
// refusal (sbx.ErrRefused: the caps, a deferred delivery, a start this
// xbind can't make now — boot's adapter marks the runner's) is 503 (03
// §A.5); the rest as for a deployment.
func ensureStatus(d Decision, err error) (int, bool) {
	if d.Partition.IsUser() && errors.Is(err, sbx.ErrRefused) {
		return http.StatusServiceUnavailable, true
	}
	return 0, false
}
