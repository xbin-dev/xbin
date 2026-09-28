package deployments

// ops_gov_data.go — the data and delivery acts of the governance operations
// (08-data §8–§10; 09-fabric §7; 11-contract §1.8–§1.9), delegating to their
// planes without the tile lock: seed, reset, vault copy, run now. Split
// verbatim from ops_gov.go at its size cap.

import (
	"context"
	"strings"

	"github.com/xbin-dev/xbin/internal/events"
)

// runSeed fills y's namespace from the primary's (08-data §8; 11-contract
// §1.8), confirmed: a copy of data that may be personal. The copy runs on
// after the answer; its completion is op data, which the broker publishes. A
// dry run's stops are the seed's own: in the stopped mode the primary's too.
func runSeed(ctx context.Context, p *Plane, g Grant, r *SeedRequest) (any, error) {
	var tile string
	var facts SeedFacts
	ans, err := p.dataAct(ctx, g, r.Seq, r.DryRun, "seed", func(o *op, y string) ([]string, error) {
		if err := confirmed(r.Confirm, ConfirmCopyData, "seeding "+y+" copies "+o.rec.Primary+"'s data, which may be personal"); err != nil {
			return nil, err
		}
		seed := p.gov().SeedData
		if seed == nil {
			return nil, notBuilt("seeding a deployment's data", "keep it empty, or give it test data from its own terminal")
		}
		req := *r
		req.Tile, req.Deployment, tile = o.tile, y, o.tile
		f, err := seed(g.P, req, p.claimantGate(g, y), p.stopDeployment)
		facts = f
		return f.Claimants, err
	})
	if d, ok := ans.(DryRunAnswer); ok && err == nil && facts.Stops != nil {
		d.Impact.Stops = make([]string, 0, len(facts.Stops))
		for _, s := range facts.Stops { // the tile's own deployments by name, as dataAct names them
			d.Impact.Stops = append(d.Impact.Stops, strings.TrimPrefix(s, tile+"+"))
		}
		ans = d
	}
	return ans, err
}

// runReset empties y's namespace (08-data §9.1), vault:true its vault too.
func runReset(ctx context.Context, p *Plane, g Grant, r *ResetRequest) (any, error) {
	return p.dataAct(ctx, g, r.Seq, r.DryRun, "erase", func(o *op, y string) ([]string, error) {
		if err := confirmed(r.Confirm, ConfirmEraseData, "resetting "+y+" erases its data"); err != nil {
			return nil, err
		}
		if p.ResetData == nil {
			return nil, notBuilt("resetting a deployment's data", "the data plane isn't wired")
		}
		return p.ResetData(o.tile, y, o.by, r.Vault, r.DryRun, p.claimantGate(g, y), p.stopDeployment)
	})
}

func (p *Plane) stopDeployment(tile, dep string) {
	if p.Run != nil {
		p.Run.StopDeployment(tile, dep)
	}
}

// dataAct runs a namespace act on g's deployment, never the primary, with no
// tile lock (the data plane holds the namespace, 08-data §6). act answers the
// claimant tiles, whose deployments of the name stop; each hears op data.
func (p *Plane) dataAct(ctx context.Context, g Grant, seq *int64, dry bool, data string, act func(*op, string) ([]string, error)) (any, error) {
	o, err := p.start(g, dry, false, seq, true, true, "")
	if err != nil {
		return nil, err
	}
	y := g.Subject.Deployment
	claimants, err := act(o, y)
	if err != nil {
		return nil, opError(o.tile, err)
	}
	stops := []string{}
	for _, t := range claimants {
		stops = append(stops, map[bool]string{true: y, false: t + "+" + y}[t == o.tile])
	}
	if dry {
		return p.answer(ctx, true, nil, Impact{Data: data, Stops: stops, Affects: "deployment"}, false)
	}
	for _, t := range claimants {
		ev := dataEvent{Op: "data", Deployment: y}
		if ds := (*DataState)(nil); p.DataOf != nil {
			if ds = p.DataOf(t, y); ds != nil {
				ev.Busy, ev.State = ds.Busy, ds.State
			}
		}
		if p.Hub != nil {
			p.Hub.Publish(events.Event{Type: "deployments", Component: t, Data: ev})
		}
	}
	return p.answer(ctx, false, nil, Impact{}, false)
}

// dataEvent is the deployments event's op data (11-contract §3.3).
type dataEvent struct {
	Op         string `json:"op"`
	Deployment string `json:"deployment"`
	Busy       string `json:"busy"`
	State      string `json:"state,omitempty"`
}

// runVaultCopy copies vault values from the primary into y (08-data §10);
// the vault plane judges the manager again and audits keys, never values.
func runVaultCopy(ctx context.Context, p *Plane, g Grant, r *VaultCopyRequest) (any, error) {
	o, err := p.start(g, r.DryRun, false, r.Seq, true, true, "")
	switch {
	case err != nil:
		return nil, err
	case p.VaultCopy == nil:
		return nil, notBuilt("copying vault values", "the vault isn't wired")
	}
	req := *r
	req.Tile, req.Deployment = o.tile, g.Subject.Deployment
	ans, err := p.VaultCopy(g.P, req)
	if err != nil || !r.DryRun {
		return ans, opError(o.tile, err)
	}
	return p.answer(ctx, true, nil, Impact{Data: "none", Affects: "deployment"}, false)
}

// runRunNow delivers y's cron job once through the broker, whatever its
// deliveries switch says (09-fabric §7; 11-contract §1.9); no tile lock.
func runRunNow(ctx context.Context, p *Plane, g Grant, r *RunNowRequest) (any, error) {
	o, err := p.start(g, r.DryRun, false, r.Seq, true, true, ": its jobs fire on schedule")
	switch {
	case err != nil:
		return nil, err
	case r.Job == "":
		return nil, badRequest("bad request body: job is required")
	case r.DryRun:
		return p.answer(ctx, true, nil, Impact{Data: "none", Affects: "deployment"}, false)
	case p.RunNow == nil:
		return nil, notBuilt("running a job now", "the broker's cron dispatch isn't wired")
	}
	d, err := p.RunNow(ctx, o.tile, g.Subject.Deployment, r.Job)
	if err != nil {
		return nil, opError(o.tile, err)
	}
	return RunNowAnswer{Delivery: d}, nil
}

// claimantGate judges g's act on each other claimant tile of dep's shared
// namespace (D127t; 08-data §6.3), for the data plane: its own row there.
func (p *Plane) claimantGate(g Grant, dep string) func(string) error {
	return func(tile string) error {
		_, err := p.Authorize(g.P, g.Op, Subject{Tile: tile, Deployment: dep, Primary: p.Primary(tile), Record: true})
		return err
	}
}
