// quotas.go — the operators' bounds on each consumer's and each person's
// sandboxes (config.quotas), on top of the substrate's own limits for this
// whole tile. A count or a disk is checked when a sandbox is made; running
// sandboxes, memory and vCPUs when one starts — ours, or a stopped one a
// command brings up. Over a quota is 429 `limit`, and hello's limits say the
// effective values for the caller.
package main

import (
	"context"
	"fmt"
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// usage is what a set of sandboxes holds.
type usage struct {
	Sandboxes int `json:"sandboxes"`
	Running   int `json:"running"`
	MemMiB    int `json:"memMiB"`
	VCPUs     int `json:"vcpus"`
	DiskGiB   int `json:"diskGiB"`
}

// minPos is the smallest of the positive values (0: none is).
func minPos(xs ...int) int {
	out := 0
	for _, x := range xs {
		if x > 0 && (out == 0 || x < out) {
			out = x
		}
	}
	return out
}

// effectiveQuota is what binds c: its consumer's quota, its person's (when
// the call names one) and the substrate's limits for the whole tile, each
// field the tightest.
func (m *Manager) effectiveQuota(c caller, rt *xbin.SandboxRuntime) Quota {
	cfg := m.config()
	qc := cfg.Quotas.forConsumer(c.from)
	var qp Quota
	if c.user != "" {
		qp = cfg.Quotas.forPerson(c.user)
	}
	var l xbin.SandboxLimits
	if rt != nil {
		l = rt.Limits
	}
	return Quota{
		Sandboxes: minPos(qc.Sandboxes, qp.Sandboxes, l.Sandboxes),
		Running:   minPos(qc.Running, qp.Running, l.Running),
		MemMiB:    minPos(qc.MemMiB, qp.MemMiB, l.MemMiB),
		VCPUs:     minPos(qc.VCPUs, qp.VCPUs, l.VCPUs),
		DiskGiB:   minPos(qc.DiskGiB, qp.DiskGiB, l.DiskGiB),
	}
}

// sizeOf is a record's resources (its plan's while it is made).
func (m *Manager) sizeOf(r *record) sizeSpec {
	if r.Plan != nil && r.Plan.Size.MemMiB > 0 {
		return r.Plan.Size
	}
	if s, ok := m.cfg.size(r.Size); ok {
		return sizeSpec{s.MemMiB, s.VCPUs, s.DiskGiB}
	}
	return sizeSpec{}
}

// tally sums the sandboxes that match (m.mu held); running says which are
// up now (runtime name → true), plus the starts of ours under way.
func (m *Manager) tally(match func(*record) bool, running map[string]bool) usage {
	var u usage
	for _, r := range m.recs {
		if !match(r) {
			continue
		}
		u.Sandboxes++
		s := m.sizeOf(r)
		u.DiskGiB += s.DiskGiB
		if running[r.Runtime] || m.starting[r.ID] {
			u.Running++
			u.MemMiB += s.MemMiB
			u.VCPUs += s.VCPUs
		}
	}
	return u
}

// runningSet asks the backend which sandboxes are up (or coming up).
func (m *Manager) runningSet(ctx context.Context) (map[string]bool, error) {
	infos, err := m.backend().List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, in := range infos {
		if in.State == "running" || in.State == "starting" {
			out[in.Name] = true
		}
	}
	return out, nil
}

// quotaNeed is what a sandbox is about to take: a place in the count, its
// disk, a running slot (with its memory and vCPUs).
type quotaNeed struct{ count, disk, run bool }

// quotaCheck says whether a sandbox of consumer/person, of size s, fits
// what it is about to take (m.mu held). except is the sandbox itself (not
// counted as what it already holds); running is the substrate's running set.
func (m *Manager) quotaCheck(rt *xbin.SandboxRuntime, consumer, person string, s sizeSpec, need quotaNeed, running map[string]bool, except string) error {
	type bound struct {
		who string
		q   Quota
		u   usage
	}
	cfg := m.cfg
	bounds := []bound{{"this consumer (" + consumer + ")", cfg.Quotas.forConsumer(consumer),
		m.tally(func(r *record) bool { return r.Owner.Via == consumer && r.ID != except }, running)}}
	if person != "" {
		bounds = append(bounds, bound{person, cfg.Quotas.forPerson(person),
			m.tally(func(r *record) bool { return r.Owner.User == person && r.ID != except }, running)})
	}
	if rt != nil {
		l := rt.Limits
		bounds = append(bounds, bound{"this manager", Quota{Sandboxes: l.Sandboxes, Running: l.Running, MemMiB: l.MemMiB, VCPUs: l.VCPUs, DiskGiB: l.DiskGiB},
			m.tally(func(r *record) bool { return r.ID != except }, running)})
	}
	for _, b := range bounds {
		q, u := b.q, b.u
		switch {
		case need.count && q.Sandboxes > 0 && u.Sandboxes+1 > q.Sandboxes:
			return errf(http.StatusTooManyRequests, "limit", "%s has %d sandboxes, the most it may have", b.who, u.Sandboxes)
		case need.disk && q.DiskGiB > 0 && u.DiskGiB+s.DiskGiB > q.DiskGiB:
			return errf(http.StatusTooManyRequests, "limit", "%s has %d GiB of disk of %d; this sandbox needs %d", b.who, u.DiskGiB, q.DiskGiB, s.DiskGiB)
		case need.run && q.Running > 0 && u.Running+1 > q.Running:
			return errf(http.StatusTooManyRequests, "limit", "%s runs %d sandboxes, the most it may run at once — stop one first", b.who, u.Running)
		case need.run && q.MemMiB > 0 && u.MemMiB+s.MemMiB > q.MemMiB:
			return errf(http.StatusTooManyRequests, "limit", "%s runs %d MiB of memory of %d; this sandbox needs %d", b.who, u.MemMiB, q.MemMiB, s.MemMiB)
		case need.run && q.VCPUs > 0 && u.VCPUs+s.VCPUs > q.VCPUs:
			return errf(http.StatusTooManyRequests, "limit", "%s runs %d vCPUs of %d; this sandbox needs %d", b.who, u.VCPUs, q.VCPUs, s.VCPUs)
		}
	}
	return nil
}

// usageBy is every consumer's and every person's usage (the operators'
// view).
func (m *Manager) usageBy(running map[string]bool) (consumers, people map[string]usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	consumers, people = map[string]usage{}, map[string]usage{}
	for _, r := range m.recs {
		c := r.Owner.Via
		if _, ok := consumers[c]; !ok {
			consumers[c] = m.tally(func(x *record) bool { return x.Owner.Via == c }, running)
		}
		if p := r.Owner.User; p != "" {
			if _, ok := people[p]; !ok {
				people[p] = m.tally(func(x *record) bool { return x.Owner.User == p }, running)
			}
		}
	}
	return consumers, people
}

// sizeText names a size for a message.
func sizeText(s sizeSpec) string {
	return fmt.Sprintf("%d MiB, %d vCPUs, %d GiB", s.MemMiB, s.VCPUs, s.DiskGiB)
}
