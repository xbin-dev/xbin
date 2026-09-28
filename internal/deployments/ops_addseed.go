package deployments

// ops_addseed.go — add with data:"seed" (05-model §5; 08-data §8.1;
// 11-contract §1.5): the deployment is added as an empty one would be, then,
// once the tile's lock is released, its data is seeded from the primary's
// through the broker's seed (GovHooks.SeedData), judged at the seed's row on
// every other claimant. The seed's copy runs on after the answer, which
// carries data.busy "seeding"; its completion is op data.

import (
	"context"
	"net/http"
)

// seedable refuses, before anything is added, a seeded add the seed would
// refuse for the tile whatever the moment: a workspace-scope tile's
// resources have one namespace, and without the broker's seed there is
// nothing to seed with.
func (p *Plane) seedable(o *op, y string) error {
	switch {
	case o.c.Scope == "":
		return &Error{Status: http.StatusConflict, Kind: KindPolicy, Msg: o.tile + " is in the workspace scope, whose resources have one namespace: " +
			y + " has no data of its own to seed"}
	case p.gov().SeedData == nil:
		return notBuilt("adding a seeded deployment", `add it empty (data:"empty") and seed it later`)
	}
	return nil
}

// runAddSeeded is add's Run: runAdd, and for data:"seed" the seed after it.
// A dry run's impact says the data is seeded. A seed the broker refuses once
// the deployment is added leaves it added, empty, and answers the refusal,
// saying so.
func runAddSeeded(ctx context.Context, p *Plane, g Grant, r *AddRequest) (any, error) {
	ans, err := runAdd(ctx, p, g, r)
	if err != nil || r.Data != DataSeed {
		return ans, err
	}
	if d, ok := ans.(DryRunAnswer); ok {
		d.Impact.Data = "seed"
		return d, nil
	}
	y, sg := g.Subject.Deployment, g
	sg.Op = OpSeed // every other claimant is judged as a seed judges it
	req := SeedRequest{Tile: g.Subject.Tile, Deployment: y, Confirm: r.Confirm}
	if _, err := p.gov().SeedData(g.P, req, p.claimantGate(sg, y), p.stopDeployment); err != nil {
		e, _ := opError(g.Subject.Tile, err).(*Error)
		if e == nil {
			e = &Error{Status: http.StatusInternalServerError, Msg: err.Error()}
		}
		return nil, &Error{Status: e.Status, Kind: e.Kind, Msg: y + " was added with empty data, but its seed was refused: " + e.Msg}
	}
	return ans, nil
}
