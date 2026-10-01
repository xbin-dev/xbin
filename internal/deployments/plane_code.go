package deployments

// plane_code.go — what code a deployment runs (CodeFor, SettledCodeFor):
// moved out of plane.go (its size budget).

import (
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// CodeFor answers what deployment dep of tile runs: its record's checkpoint,
// or the work tree while live reload drives it (D119e); util.ErrNoDeployment for
// a name the tile doesn't have; a *HeldError while its record holds it, so
// nothing starts. Without a record: the work tree for main.
func (p *Plane) CodeFor(tile, dep string) (runner.Code, error) {
	rec, err := p.record(tile)
	if err != nil {
		return runner.Code{}, err
	}
	if rec == nil {
		if dep != util.MainDeployment {
			return runner.Code{}, util.NoDeployment(tile, dep)
		}
		return runner.Code{WorkTree: true}, nil
	}
	d := rec.Deployments[dep]
	switch {
	case d == nil:
		return runner.Code{}, util.NoDeployment(tile, dep)
	case d.Checkpoint == nil:
		return runner.Code{WorkTree: true}, nil
	}
	return runner.Code{Tree: *d.Checkpoint}, nil
}

// SettledCodeFor is CodeFor once no operation is detaching tile's live
// reload (the pausing overlay): the runner asks it before a generation built
// from the work tree serves, so one built while a pause took its checkpoint
// never serves once the pause commits (D174).
func (p *Plane) SettledCodeFor(tile, dep string) (runner.Code, error) {
	p.pausing.settle(tile)
	return p.CodeFor(tile, dep)
}
