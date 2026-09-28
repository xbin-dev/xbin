package registry

// qualifier.go — the deployment URL qualifier (11-contract §2.1, §2.2, §2.4)
// (D127j). A tile ref "<tile>+<name>" names deployment <name> of <tile>; the
// qualifier sits inside the tile path's last segment, never in a segment of
// its own. ResolveRef is the one resolver the /c/ and /api/ planes, the
// asset-token plane and tile origins share. It answers every path exactly as
// Resolve does unless the path holds a '+', a deployment record governs the
// tile it names, and today's resolution finds nothing at the full candidate
// "<tile>+<name>": a component at least as deep, or anything on disk there,
// wins. So a zero-state tile never splits, a directory whose name holds a
// '+' keeps resolving, and "<tile>+main" on a zero-state tile is what it is
// today (D119c).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/util"
)

// DeploymentLookup is what ResolveRef asks about a tile's deployments: the
// deployments plane answers it (*deployments.Plane has these three methods).
// Every answer is an in-memory lookup, since ResolveRef runs on requests.
// A nil DeploymentLookup answers as a workspace without deployment records:
// no tile splits, and every tile's primary is main.
type DeploymentLookup interface {
	// HasRecord reports whether a deployment record governs tile.
	HasRecord(tile string) bool
	// HasDeployment reports whether tile has a deployment called name.
	HasDeployment(tile, name string) bool
	// Primary names tile's primary deployment (main without a record).
	Primary(tile string) string
}

// ErrNoComponent is a path no tile answers: today's resolution finds no
// component, and no qualifier names a deployment. A 404 on the wire; the
// error ResolveRef returns reads "no such tile: <path>".
var ErrNoComponent = errors.New("no such tile")

// ErrNestedTile is a qualified path that reaches into a component deeper
// than its tile: a qualified URL never enters a nested component, whose own
// deployments carry the qualifier on its own last segment (11-contract
// §2.4). A 404 on the wire; the error ResolveRef returns names the nested
// tile and its deployment URLs.
var ErrNestedTile = errors.New("a tile of its own")

// nestedTile is ErrNestedTile for the nested tile at path.
type nestedTile struct{ path string }

func (e nestedTile) Error() string {
	return e.path + " is a tile of its own; its deployments are /c/" + e.path + "+<name>/"
}

func (e nestedTile) Is(target error) bool { return target == ErrNestedTile }

// ResolveRef maps a cleaned path (util.SafeJoin already applied; no leading
// "/c/" or "/api/") or a tile ref to the component it names, the deployment
// it acts on, and the remainder beneath the tile. qualified reports that the
// path named a deployment; dep is then that name, and otherwise the tile's
// primary. d answers about deployment records; nil is a workspace without
// any.
//
//   - A path without '+' takes Resolve's answer, and so does every path of a
//     workspace without records: the fast path.
//   - Otherwise each segment's last '+', deepest segment first, splits it
//     into a tile and a deployment name, when the name follows the grammar
//     (util.DeploymentNameOK) and a record governs a registered tile there.
//     A component at least as deep as that segment, or anything on disk at
//     the full candidate "<tile>+<name>" (Lstat, following nothing), keeps
//     Resolve's answer: today's resolution wins.
//   - "<tile>+<primary>" is the alias: the primary, qualified.
//   - An unknown name on a tile with a record is util.ErrNoDeployment (the
//     error reads `<tile> has no deployment "<name>"`), never a fallback to a
//     shorter prefix that would serve "<tile>+<name>/…" as a parent tile's
//     file. A remainder that lands in a component deeper than the tile is
//     ErrNestedTile. With either error c and dep still name the tile and the
//     qualifier, so the caller can apply its gate first: whoever may not know
//     the tile's deployments gets that gate's refusal, whether or not the
//     name exists (11-contract §2.2, §2.3).
//   - A path Resolve can't place and no qualifier names is ErrNoComponent.
//
// A path that isn't clean (".", "..", empty segments) never splits: it takes
// Resolve's answer, so a remainder never leaves its tile.
func (r *Registry) ResolveRef(p string, d DeploymentLookup) (c *Component, dep string, qualified bool, rest string, err error) {
	base, baseRest, baseOK := r.Resolve(p)
	if d != nil && strings.IndexByte(p, '+') >= 0 {
		if t, name, tRest, ok, tErr := r.split(strings.Trim(p, "/"), base, baseOK, d); ok {
			return t, name, true, tRest, tErr
		}
	}
	if !baseOK {
		return nil, "", false, "", fmt.Errorf("%w: %s", ErrNoComponent, strings.Trim(p, "/"))
	}
	return base, primaryOf(d, base.Path), false, baseRest, nil
}

// split is ResolveRef's qualified half on a trimmed path p, given Resolve's
// answer for it: ok when a segment of p names a deployment of a tile with a
// record, with that tile, the name and the remainder, or the error the
// qualifier meets. !ok leaves p to Resolve's answer.
func (r *Registry) split(p string, base *Component, baseOK bool, d DeploymentLookup) (t *Component, name, rest string, ok bool, err error) {
	if !fs.ValidPath(p) {
		return nil, "", "", false, nil
	}
	segs := strings.Split(p, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		j := strings.LastIndexByte(segs[i], '+')
		if j < 1 || !util.DeploymentNameOK(segs[i][j+1:]) {
			continue
		}
		tile := path.Join(path.Join(segs[:i]...), segs[i][:j])
		tc, found := r.Component(tile)
		if !found || !d.HasRecord(tile) {
			continue // a zero-state tile: '+' means nothing (D119c)
		}
		if baseOK && depth(base.Path) >= i+1 || r.onDisk(path.Join(segs[:i+1]...)) {
			break // today's answer wins at the full candidate
		}
		name, rest = segs[i][j+1:], path.Join(segs[i+1:]...)
		if !d.HasDeployment(tile, name) {
			return tc, name, "", true, util.NoDeployment(tile, name)
		}
		if n, _, deeper := r.Resolve(path.Join(tile, rest)); deeper && n.Path != tile {
			return tc, name, "", true, nestedTile{n.Path}
		}
		return tc, name, rest, true, nil
	}
	return nil, "", "", false, nil
}

// onDisk reports whether anything, a dangling symlink included, sits at the
// workspace-relative path rel. Lstat only: nothing is followed or opened.
func (r *Registry) onDisk(rel string) bool {
	_, err := os.Lstat(filepath.Join(r.Root, filepath.FromSlash(rel)))
	return err == nil
}

// depth counts a component path's segments.
func depth(p string) int { return strings.Count(p, "/") + 1 }

// primaryOf is d's primary for tile, or main without a lookup.
func primaryOf(d DeploymentLookup, tile string) string {
	if d == nil {
		return util.MainDeployment
	}
	return d.Primary(tile)
}
