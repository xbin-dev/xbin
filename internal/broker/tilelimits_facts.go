package broker

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
)

// Who is asking (D203, docs/auth.md §Ownership): the tile limits' viewers
// and actors, and what the runtime and the registry say about a tile's
// kinds — gathered with no store lock held, since the runtime's answers
// call out.

const limitsDocs = "/docs/isolation.md"

// limitsViewer is a caller of the limits routes and /alerts, resolved to
// the person it stands for.
type limitsViewer struct {
	p      auth.Principal // the person (a frame's driver), or the caller as it is
	person bool           // p may decide: a person's own session, app or device, the root token, an admin-driven console, a chrome frame's driver
	admin  bool           // a workspace admin: reads every row; decides anything (when person)
	self   string         // a tile's own credentials: that tile (its row, read only)
}

// limitsViewerOf resolves p: a person as they are (view-as reads as the
// person viewed, deciding nothing); the admin console's frame as its
// driver, deciding only when the driver is an admin; a trusted chrome
// frame — the shell, which reads /alerts and posts an alert's dismiss with
// its frame token — as its driver; every other tile credential as itself.
func (b *Broker) limitsViewerOf(p auth.Principal) limitsViewer {
	if p.Component == "" {
		person := p.Impersonator == "" && (p.Owner || p.UserID != "") &&
			p.Via != "terminal" && p.Via != "instance" && p.Via != "frame" // a terminal's token, wherever it's bound, is never the person's session
		return limitsViewer{p: p, person: person, admin: b.IsAdmin(p)}
	}
	if d, ok := b.AdminFrameDriver(p); ok {
		admin := b.IsAdmin(d)
		return limitsViewer{p: d, person: admin, admin: admin}
	}
	if d, ok := b.chromeFrameDriver(p); ok {
		return limitsViewer{p: d, person: true, admin: b.IsAdmin(d)}
	}
	return limitsViewer{p: p, admin: b.IsAdmin(p), self: p.Component}
}

// chromeFrameDriver is the person behind a trusted chrome frame minted
// under their own login (auth.Principal.LoginFrame), in its primary: root,
// shell, or a tile whose chrome a workspace admin approved (D118). Such a
// document already runs with the person's session cookie, so its frame
// token standing for them adds nothing it couldn't do.
func (b *Broker) chromeFrameDriver(p auth.Principal) (auth.Principal, bool) {
	if !p.LoginFrame() || !b.trustedChromeTile(p.Component) {
		return auth.Principal{}, false
	}
	if _, np, err := b.nonPrimary(p); err != nil || np {
		return auth.Principal{}, false
	}
	if p.UserID == "" {
		return auth.Principal{Owner: true, Via: p.Via, Gen: p.Gen}, true // the root token's login
	}
	if b.Users == nil {
		return auth.Principal{}, false
	}
	u, ok := b.Users.Get(p.UserID)
	if !ok || u.Disabled {
		return auth.Principal{}, false
	}
	acc, _ := b.Users.Access(p.UserID)
	return auth.Principal{UserID: p.UserID, User: u, Access: acc, Via: p.Via, Gen: p.Gen}, true
}

// trustedChromeTile mirrors server.trustedChrome for a primary: the
// implicit chrome, shipped chrome, or an approved request.
func (b *Broker) trustedChromeTile(path string) bool {
	if path == "root" || path == "shell" {
		return true
	}
	c, ok := b.Reg.Component(path)
	if !ok || !c.Manifest.Chrome {
		return false
	}
	return path == "tiles/organisations" || b.Users != nil && b.Users.ChromeApproved(path)
}

// limitsActor is the viewer of a write — a person — or a 403 (answered).
func (b *Broker) limitsActor(w http.ResponseWriter, p auth.Principal) (limitsViewer, bool) {
	v := b.limitsViewerOf(p)
	if !v.person {
		server.WriteError(w, http.StatusForbidden, "deciding a tile's limits is a person's act, in their own session (bx, the shell, /xbin/limits, "+
			"the admin console driven by a workspace admin); no tile's backend, frame, terminal or agent does it — and viewing as someone doesn't", limitsDocs)
	}
	return v, v.person
}

// orgOf is the org that owns tile and whether the viewer admins it ("" when
// tile is not org-owned).
func (b *Broker) orgOf(v limitsViewer, tile string) (org string, admin bool) {
	if b.Users == nil || tile == "" {
		return "", false
	}
	org, _ = b.Users.OwnerOrg(tile)
	return org, org != "" && b.approverOrg(v.p, tile) == org
}

// ceilingOf is org's ceiling for k (0 = none).
func (b *Broker) ceilingOf(org string, k limits.Kind) int64 {
	if org == "" || b.Users == nil {
		return 0
	}
	return b.Users.OrgLimitsOf(org)[k]
}

// mayApprove reports whether v may decide value val of kind k for tile
// (the scope's root tile for disk; "" = none: workspace admins only), and
// why not.
func (b *Broker) mayApprove(v limitsViewer, tile string, k limits.Kind, val int64) (bool, string) {
	switch {
	case !v.person:
		return false, "a person decides it"
	case v.admin:
		return true, ""
	}
	org, orgAdmin := b.orgOf(v, tile)
	if !orgAdmin {
		if org != "" {
			return false, "workspace admins or an admin of org " + org + " approve it"
		}
		return false, "workspace admins approve it"
	}
	c := b.ceilingOf(org, k)
	switch {
	case c == 0:
		return false, "org " + org + " has no ceiling for " + k.Meta().Label + ": ask a workspace admin"
	case val > c:
		return false, "above org " + org + "'s ceiling of " + limits.Format(k, c) + ": ask a workspace admin"
	}
	return true, ""
}

// mayDecideKind reports whether v may decide kind k of tile at some value
// above def: a row's canDecide, a hit's dismissal.
func (b *Broker) mayDecideKind(v limitsViewer, tile string, k limits.Kind, def int64) bool {
	if !v.person {
		return false
	}
	if v.admin {
		return true
	}
	org, orgAdmin := b.orgOf(v, tile)
	return orgAdmin && b.ceilingOf(org, k) > def
}

// mayRevert reports whether v may put kind k of tile back to its default:
// the tile's managers (its user-owner, the owning org's admins) and
// workspace admins — narrowing is safe. A scope no tile roots: admins.
func (b *Broker) mayRevert(v limitsViewer, tile string) bool {
	return v.person && (v.admin || tile != "" && b.mayManageTile(v.p, tile))
}

// approversFor is who may approve value val of kind k for tile.
func (b *Broker) approversFor(tile string, k limits.Kind, val int64) []string {
	out := []string{limits.ApproverAdmin}
	if b.Users == nil || tile == "" {
		return out
	}
	if org, ok := b.Users.OwnerOrg(tile); ok {
		if c := b.ceilingOf(org, k); c > 0 && val <= c {
			out = append(out, limits.ApproverOrg+org)
		}
	}
	return out
}

// mayReadLimits reports whether v sees tile's row at all: admins, the
// tile's managers and its owning org's admins (every row they list), its
// readers and its own credentials (read only, asked by name).
func (b *Broker) mayReadLimits(v limitsViewer, tile string, byName bool) bool {
	switch {
	case v.admin, v.self != "" && v.self == tile:
		return true
	case v.self != "":
		return false
	case b.mayManageTile(v.p, tile):
		return true
	}
	if _, orgAdmin := b.orgOf(v, tile); orgAdmin {
		return true
	}
	return byName && v.p.CanReadTile(tile)
}

// ---- what the runtime and the registry say ----

// kindFacts is one kind of one tile: the runtime's default, maximum, usage
// and what runs now, the manifest's ask, and whether the kind applies.
type kindFacts struct {
	def, max, usage, applied, ask int64
	enforced, placed              bool
	note                          string // why an ask is ignored or dormant
}

// applies reports whether the kind is decided for the tile at all.
func (kf kindFacts) applies() bool { return kf.enforced && kf.placed }

// pending reports whether the manifest's ask waits for an approver.
func (kf kindFacts) pending(tf tileFacts, e limits.Entry) bool {
	return kf.applies() && !tf.offloaded && limits.Pending(kf.def, kf.max, e, kf.ask)
}

// effective is the limit that applies with e.
func (kf kindFacts) effective(e limits.Entry) int64 {
	return limits.Effective(kf.def, kf.max, e, kf.ask)
}

// wanted is what approving the manifest's ask grants: the ask, at most the
// maximum.
func (kf kindFacts) wanted() int64 {
	if kf.max > 0 {
		return min(kf.ask, kf.max)
	}
	return kf.ask
}

// tileFacts is a tile's facts: where it sits, who owns it, its kinds (every
// kind that applies or that its manifest asks).
type tileFacts struct {
	tile, scope, scopeKey, org string
	rootsScope, offloaded      bool
	kinds                      map[limits.Kind]kindFacts
}

// keyOf is the store's key for kind k of the tile: its path, or its scope's
// data key for disk.
func (tf tileFacts) keyOf(k limits.Kind) string {
	if k == limits.DiskGiB {
		return tf.scopeKey
	}
	return tf.tile
}

// tileFacts gathers c's facts. It calls out (the runtime's sources): never
// under the store's lock.
func (b *Broker) tileFacts(c *registry.Component) tileFacts {
	tf := tileFacts{tile: c.Path, scope: c.Scope, scopeKey: limitScopeKey(c.Scope), rootsScope: c.Scope != "" && c.Scope == c.Path,
		offloaded: registry.IsOffloaded(b.Reg.LifecycleState(c.Path)), kinds: map[limits.Kind]kindFacts{}}
	if b.Users != nil {
		tf.org, _ = b.Users.OwnerOrg(c.Path)
	}
	backend, vm := c.HasBackend(), c.Manifest.VM.Enabled()
	for _, k := range limits.Kinds {
		kf := kindFacts{ask: c.Manifest.LimitAsk(k)}
		switch k {
		case limits.DiskGiB:
			kf.placed = tf.rootsScope
			if !kf.placed {
				where := "the workspace's"
				if c.Scope != "" {
					where = "its scope's (" + c.Scope + ")"
				}
				kf.note = "ignored: the quota is " + where + "; ask from the tile that roots it"
			}
		case limits.MemMiB:
			kf.placed = backend && !vm
			switch {
			case !backend:
				kf.note = "ignored: the tile runs no backend"
			case vm:
				kf.note = "ignored: a VM backend's memory is vm.memory"
			}
		case limits.VMMemMiB, limits.VMVCPUs:
			kf.placed = backend && vm
		default: // the tile sandboxes' total
			kf.placed = b.SandboxesFor(c.Path)
			if !kf.placed {
				kf.note = "dormant: the tile holds no cap:sandboxes grant"
			}
		}
		if k == limits.DiskGiB {
			kf.def, kf.enforced, kf.max = b.diskDefault(), b.disk != nil, b.kindMax(k)
			kf.usage = b.diskUsageOf(tf.scopeKey)
		} else {
			kf.def, kf.enforced = b.kindDefault(c.Path, k)
			kf.max = b.kindMax(k)
			if kf.applies() {
				kf.usage = b.kindUsage(c.Path, k)
				kf.applied = b.kindApplied(c.Path, k)
			}
		}
		if kf.placed && !kf.enforced {
			kf.note = "not enforced on this workspace"
		}
		if kf.ask == 0 {
			kf.note = "" // a note explains an ask
		}
		tf.kinds[k] = kf
	}
	return tf
}

// The runtime's answers, nil-safe.

func (b *Broker) kindDefault(tile string, k limits.Kind) (int64, bool) {
	if k == limits.DiskGiB {
		return b.diskDefault(), b.disk != nil
	}
	if f := b.tl.src.Default; f != nil {
		return f(tile, k)
	}
	return 0, false
}

func (b *Broker) kindMax(k limits.Kind) int64 {
	if k == limits.DiskGiB {
		return b.diskMax()
	}
	if f := b.tl.src.Max; f != nil {
		return f(k)
	}
	return 0
}

func (b *Broker) kindUsage(tile string, k limits.Kind) int64 {
	if f := b.tl.src.Usage; f != nil {
		return f(tile, k)
	}
	return 0
}

func (b *Broker) kindApplied(tile string, k limits.Kind) int64 {
	if f := b.tl.src.Applied; f != nil {
		return f(tile, k)
	}
	return 0
}
