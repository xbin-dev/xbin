package auth

// deployment.go — the deployment a tile principal is bound to (D127g)
// (11-contract §7.1–§7.4). Every credential that yields one of a tile's own
// principals names, beside the tile path, the deployment of that tile it
// acts in, and Principal.Deployment carries it. From() stays the tile path:
// grants, bindings, ceilings and ownership keep keying on the path, and the
// deployment travels beside it, never inside it (D127f). The deployment always
// comes from xbind-held state (the instance map, the terminal map) or from a
// signed claim, never from a header, a query parameter or the environment.
//
//   - instance tokens: the deployment whose generation the token was minted
//     for (RegisterInstanceDeployment), under the name rule: "" is main;
//   - frame tokens: the claim, a sixth field inside the HMAC present only
//     for a deployment other than main (frametoken.go); a 4- or 5-field token
//     means main;
//   - terminal and agent tokens: the session's target (MintTerminalTarget):
//     a deployment name, main included, or "" for a session that follows
//     the primary, which the deployments plane resolves on every request
//     (Plane.Addressed), so auth needs no view of the record.
//
// The pre-deployment signatures (RegisterInstance, MintTerminal,
// MintFrameToken) stay, meaning main and the primary: a tile without a
// deployment record only ever has main, and its credentials keep today's
// bytes and today's principals.

import (
	"log/slog"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// claimName applies the name rule to a deployment name: main (or nothing)
// is "", the spelling every credential of main keeps; any other valid name
// is itself. ok is false for a string that is not a deployment name, which
// no credential may carry.
func claimName(dep string) (claim string, ok bool) {
	switch {
	case dep == "" || dep == util.MainDeployment:
		return "", true
	case util.DeploymentNameOK(dep):
		return dep, true
	}
	return "", false
}

// --- instance tokens: token → (tile, deployment) ---

// instanceID is what an instance token names: the tile of the generation
// it was minted for, and that generation's deployment ("" = main).
type instanceID struct {
	component  string
	deployment string
	partition  util.Partition // a user partition's generation (partition.go); "" otherwise, global's included
	uid        string         // that partition's person's uid
}

// principal is the backend principal an instance token authenticates as.
func (id instanceID) principal() Principal {
	return Principal{Component: id.component, Via: "instance", Deployment: id.deployment, Partition: id.partition}
}

// RegisterInstance registers a backend generation of the tile's main
// deployment: today's call, kept for its callers (RegisterInstanceDeployment
// with main).
func (a *Auth) RegisterInstance(token, component string) {
	a.RegisterInstanceDeployment(token, component, "")
}

// RegisterInstanceDeployment registers the instance token of one backend
// generation of deployment of component (D127g): the runner calls it at spawn
// with the deployment it is starting, from its own state. main, or "",
// registers main. A token is minted per generation, so two deployments'
// generations never share a map entry, and RevokeInstance drops it at
// process exit. A deployment that is not a valid name registers nothing
// (fail closed: the generation's calls authenticate as nobody).
func (a *Auth) RegisterInstanceDeployment(token, component, deployment string) {
	dep, ok := claimName(deployment)
	if !ok {
		slog.Warn("instance token not registered: not a deployment name", "component", component, "deployment", deployment)
		return
	}
	a.mu.Lock()
	a.instances[token] = instanceID{component: component, deployment: dep}
	a.mu.Unlock()
}

func (a *Auth) lookupInstance(token string) (instanceID, bool) {
	a.mu.RLock()
	id, ok := a.instances[token]
	a.mu.RUnlock()
	if ok && id.partition != "" && !a.partitionCovered(id) {
		return instanceID{}, false // its partition is no longer covered: 401 (partition.go)
	}
	return id, ok
}

// --- terminal and agent tokens: the session's target ---

// MintTerminalTarget registers a terminal or agent session's token on
// component, bound to the session's target (D127p; 11-contract §7.4): target
// names a deployment of the tile, main included, or is "" for a session
// that follows the primary. The target is fixed for the token's life;
// changing it means a new session with a new token. The caller has already
// chosen the target (term.ChooseTarget), so a protected primary never
// reaches here; a target that is not a deployment name mints nothing ("":
// the session opens without a token, as with the API off).
func (a *Auth) MintTerminalTarget(component, userID, target string) string {
	if target != "" && !util.DeploymentNameOK(target) {
		slog.Warn("terminal token not minted: not a deployment name", "component", component, "target", target)
		return ""
	}
	tok := util.RandomToken(24)
	a.mu.Lock()
	a.terminals[tok] = termID{component: component, userID: userID, deployment: target}
	a.mu.Unlock()
	return tok
}

// --- frame tokens: the deployment claim ---

// boundClaim is the frame-token claim p's own credential binds it to on
// component, under the name rule, when p is one of component's own
// principals: a frame token's claim, an instance token's deployment, a
// terminal or agent session's named target. named is false for everyone
// else (humans, other tiles' principals) and for a session that follows
// the primary: its deployment is the current primary, which the
// deployments plane knows and auth doesn't.
func boundClaim(p Principal, component string) (claim string, named bool) {
	if p.Component == "" || p.Component != component || (p.Via == "terminal" && p.Deployment == "") {
		return "", false
	}
	claim, ok := claimName(p.Deployment)
	return claim, ok
}

// renewalClaim is the claim MintFrameTokenFor copies: p's own binding when p
// renews its own tile's token (a frame, or a session with a named target),
// as the generation is copied; main otherwise. A session that follows the
// primary copies main, which is its binding while main is the primary (the
// zero state); for a tile whose primary is another deployment, the server
// resolves the session's deployment first and mints through
// MintFrameTokenForDeployment.
func renewalClaim(p Principal, component string) string {
	claim, _ := boundClaim(p, component)
	return claim
}

// MintFrameTokenDeployment is MintFrameToken for deployment of component:
// the token carries deployment's claim (none for main) and is bound to the
// user's current generation. "" when deployment is not a deployment name.
func (a *Auth) MintFrameTokenDeployment(component, userID, deployment string, ttl time.Duration) string {
	claim, ok := claimName(deployment)
	if !ok {
		return ""
	}
	return a.mintFrame(component, userID, a.defaultGen(userID), claim, ttl)
}

// MintFrameTokenForDeployment mints the frame token of deployment's document
// of component on behalf of p (the D4 injection of a qualified or
// reassigned document; renewal naming a deployment): MintFrameTokenFor with
// the claim named explicitly (none for main). One of component's own
// principals whose credential names its deployment gets a token only for
// that deployment, never another deployment of its own tile (D127g); "" then,
// as for a string that is not a deployment name. Who may mint at all, the
// level a human needs, and the deployment a session that follows the
// primary is bound to are the caller's decisions (mayMintFrameToken,
// Plane.Addressed).
func (a *Auth) MintFrameTokenForDeployment(p Principal, component, deployment string, ttl time.Duration) string {
	claim, ok := claimName(deployment)
	if !ok {
		return ""
	}
	if bound, named := boundClaim(p, component); named && bound != claim {
		return ""
	}
	return a.mintFrame(component, p.UserID, a.frameGenFor(p), claim, ttl)
}
