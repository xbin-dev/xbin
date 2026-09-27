package term

// target.go — a session's target deployment (P24) (11-contract §7.4): the
// deployment of its tile a terminal or agent session's token is bound to,
// chosen when the session starts and fixed for its life (changing it
// restarts the session, as today's API switch does). The deployments plane
// tells the manager what the choice needs to know of a tile
// (TileDeployments), through a hook boot installs; ChooseTarget applies the
// rule to it (P24). A tile without a deployment record has main alone, unprotected and
// followed by live reload, so every session follows the primary and the API
// dropdown keeps today's two entries (tile API, no API).

import (
	"fmt"
	"slices"

	"github.com/xbin-dev/xbin/internal/util"
)

// TileDeployments is what a session's target choice needs to know of a
// tile: the deployments plane's answer (Plane.TileDeployments).
type TileDeployments struct {
	Record     bool     // a deployment record governs the tile; false: the zero state
	Primary    string   // the primary's name: main without a record
	Protected  bool     // the primary is protected: never a session's target (P21)
	LiveReload string   // the live reload target; "" while live reload is paused
	Names      []string // every deployment of the tile, main first
}

// TileDeploymentsFunc answers TileDeployments for a tile: the hook boot
// installs from the deployments plane. nil answers the zero state for every
// tile.
type TileDeploymentsFunc func(tile string) TileDeployments

// zeroDeployments is a tile without a record: main alone.
func zeroDeployments() TileDeployments {
	return TileDeployments{Primary: util.MainDeployment, LiveReload: util.MainDeployment,
		Names: []string{util.MainDeployment}}
}

// deploymentsOf is f's answer for tile, or the zero state when f is nil.
func (f TileDeploymentsFunc) deploymentsOf(tile string) TileDeployments {
	if f == nil {
		return zeroDeployments()
	}
	return f(tile)
}

// Target is a session's target deployment.
type Target struct {
	// Deployment names the target; "" follows the primary: whichever
	// deployment holds that role at each request (11-contract §7.1), so the
	// session moves with a reassignment.
	Deployment string
	// NoAPI: the session opens without a terminal token and echoes api:
	// false ("API off", today's api=0): asked for, or nothing else can be
	// offered (a protected primary with live reload paused).
	NoAPI bool
}

// ChooseTarget picks a new session's target on tile (11-contract §7.4):
//  1. api false (the "no API" entry): no token, as today;
//  2. a requested deployment: the primary's name follows the primary,
//     unless it is protected (refused); another deployment is named; an
//     unknown one is util.ErrNoDeployment (404);
//  3. nothing requested: the primary, unless it is protected; then the live
//     reload target, named; when there is none, no API.
//
// Any error other than util.ErrNoDeployment is a refusal (403). Clients that
// send no deployment (the shipped app, an old bx, a stale tab) get step 3,
// which without a record is today's: the session follows main.
func ChooseTarget(tile string, d TileDeployments, api bool, requested string) (Target, error) {
	switch {
	case !api:
		return Target{NoAPI: true}, nil
	case requested == "":
		switch {
		case !d.Protected:
			return Target{}, nil
		case d.LiveReload != "" && d.LiveReload != d.Primary:
			return Target{Deployment: d.LiveReload}, nil
		}
		return Target{NoAPI: true}, nil
	case !slices.Contains(d.Names, requested):
		return Target{}, util.NoDeployment(tile, requested)
	case requested != d.Primary:
		return Target{Deployment: requested}, nil
	case d.Protected:
		return Target{}, fmt.Errorf("the primary of %s is protected: terminal and agent sessions can't target it", tile)
	}
	return Target{}, nil
}

// Entries lists the deployments a terminal-level user's API dropdown offers
// for the tile, before "no API": every deployment but a protected primary
// (P24). Without a record, or with only an unprotected main, that is main
// alone: today's "tile API" entry.
func Entries(d TileDeployments) []string {
	out := make([]string, 0, len(d.Names))
	for _, n := range d.Names {
		if !(d.Protected && n == d.Primary) {
			out = append(out, n)
		}
	}
	return out
}

// DeploymentEnv is the session's XBIN_DEPLOYMENT: its target's name when
// that isn't the primary at session start, else "" (the variable is left
// out). Informational: routing reads the token, never the env.
func (t Target) DeploymentEnv(d TileDeployments) string {
	if t.NoAPI || t.Deployment == d.Primary {
		return ""
	}
	return t.Deployment
}
