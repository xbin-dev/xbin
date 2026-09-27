package broker

// deployhooks.go — what the broker exposes to tile deployments: the hooks the
// deployments plane installs at boot, the manager gate, and the broker's
// answers to the server's deployment questions. Every hook is nil-safe, and
// nil answers exactly as a workspace without tile deployments does today.

import (
	"context"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
)

// DeploymentHooks are installed into the broker by the deployments plane at
// boot (Broker embeds them).
type DeploymentHooks struct {
	// The tile's life (P29): a deployment record belongs to the tile it was
	// made for — its path, owner ref and creation stamp — and never applies
	// to a new tile at that path.

	// RewriteDeploymentOwner rewrites the owner ref of tile's deployment
	// record in the same step as a transfer (D39), before the tile restarts,
	// so a pinned primary stays pinned (a record whose owner ref doesn't
	// match is ignored). nil, or a tile without a record: nothing to do.
	RewriteDeploymentOwner func(tile, ownerRef string) error
	// ResetDeploymentState drops path's deployment state (its record and
	// view repository) before a creation path assigns the new tile's owner,
	// so a tile created at a path starts in the zero state, whoever creates
	// it. nil: nothing to reset.
	ResetDeploymentState func(path string) error
	// DeploymentLeftovers lists the deployment state still keyed by path
	// that a new tile there would find (a record, a checkpoint store,
	// non-main data namespaces), for pathLeftovers' refusal list (D82).
	// nil: none.
	DeploymentLeftovers func(path string) []string
	// RestoreDeploymentState puts a restore's validated deployment section
	// back (deploymentRestorer, backup_deploy.go): it rebuilds the tile's
	// checkpoint store from the staged objects and installs the archived
	// record only when the tile has none. nil: the section is left out.
	RestoreDeploymentState func(ctx context.Context, tile string, record []byte, objects string, refs map[string]string) error

	// The server's deployment questions (server.Policy): brokerPolicy
	// answers through these, and nil answers as server.NoopPolicy does.
	DeploymentCodeRoot     func(c *registry.Component, dep string) (root string, pinned bool, err error)
	DeploymentExists       func(tile, name string) bool
	AddressableDeployments func(p auth.Principal, tile string) []string
	// DeploymentSummary answers /components' primary summary of tile
	// (server.PrimarySummaryPolicy); nil: no entry has one.
	DeploymentSummary func(tile string) (primary string, pinned, protected, ok bool)
}

// MayManageDeployments is the manager gate of the deployments plane: a
// person in their own session (no tile credential: terminal, agent, frame,
// instance, cron or bus) who is a workspace admin or manages the tile — its
// user-owner or an admin of its owning org (D24, D33). No element principal
// passes, whatever xbin or xbin:users grants its tile holds.
func (b *Broker) MayManageDeployments(p auth.Principal, tile string) bool {
	return p.Component == "" && (b.IsAdmin(p) || b.mayManageTile(p, tile))
}

// rewriteDeploymentOwner is the transfer's seam: RewriteDeploymentOwner, or
// nothing.
func (b *Broker) rewriteDeploymentOwner(tile, ownerRef string) error {
	if f := b.RewriteDeploymentOwner; f != nil {
		return f(tile, ownerRef)
	}
	return nil
}

// resetDeploymentState is the creation paths' seam: ResetDeploymentState,
// or nothing.
func (b *Broker) resetDeploymentState(path string) error {
	if f := b.ResetDeploymentState; f != nil {
		return f(path)
	}
	return nil
}

// deploymentLeftovers is pathLeftovers' seam: DeploymentLeftovers, or none.
func (b *Broker) deploymentLeftovers(path string) []string {
	if f := b.DeploymentLeftovers; f != nil {
		return f(path)
	}
	return nil
}

func (p brokerPolicy) CodeRoot(c *registry.Component, dep string) (string, bool, error) {
	if f := p.b.DeploymentCodeRoot; f != nil {
		return f(c, dep)
	}
	return server.NoopPolicy{}.CodeRoot(c, dep)
}

func (p brokerPolicy) HasDeployment(tile, name string) bool {
	if f := p.b.DeploymentExists; f != nil {
		return f(tile, name)
	}
	return server.NoopPolicy{}.HasDeployment(tile, name)
}

func (p brokerPolicy) Addressable(pr auth.Principal, tile string) []string {
	if f := p.b.AddressableDeployments; f != nil {
		return f(pr, tile)
	}
	return server.NoopPolicy{}.Addressable(pr, tile)
}

// brokerPolicy answers /components' deployments summary (WP-19).
var _ server.PrimarySummaryPolicy = brokerPolicy{}

func (p brokerPolicy) PrimarySummary(tile string) (string, bool, bool, bool) {
	if f := p.b.DeploymentSummary; f != nil {
		return f(tile)
	}
	return "", false, false, false
}
