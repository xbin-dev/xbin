package deployments

// authz_manager.go — the manager gate (05-model §10; 06-security T9): who
// manages a tile's deployments. A person in their own session who is a
// workspace admin or manages the tile (the broker's MayManage), and — P21,
// extended by the owner 2026-09-28 — the admin tile's frame standing in for
// such a person, for the acts marked frame in the authority table (authz.go):
// protect, unprotect, reassign the primary, deliveries and alwaysOn. No
// other tile credential ever passes, terminal and agent tokens included.

import "github.com/xbin-dev/xbin/internal/auth"

// Manager reports the manager gate: a person in their own session who is
// a workspace admin or manages the tile, as Broker.MayManageDeployments
// answers it — or the admin tile's frame standing in for such a person
// (frameManager), which does only the acts marked frame. The human-session
// clause is checked here as well, so no other element principal ever
// passes, whatever its tile is granted: terminal and agent tokens included.
func (p *Plane) Manager(pr auth.Principal, tile string) bool {
	if pr.Component != "" {
		return p.frameManager(pr, tile)
	}
	return p.personManages(pr, tile)
}

// personManages is the manager gate for a person in their own session.
func (p *Plane) personManages(pr auth.Principal, tile string) bool {
	if pr.Component != "" {
		return false
	}
	if p.MayManage != nil {
		return p.MayManage(pr, tile)
	}
	return pr.IsAdmin()
}

// adminFrameDriver is the person the admin tile's frame pr stands in for:
// AdminFrameDriver's answer (the broker's: a frame of a tile holding xbin
// admin, minted under that person's own login), for a frame only, and only
// a person.
func (p *Plane) adminFrameDriver(pr auth.Principal) (auth.Principal, bool) {
	if p.AdminFrameDriver == nil || pr.Via != "frame" || pr.Component == "" {
		return auth.Principal{}, false
	}
	d, ok := p.AdminFrameDriver(pr)
	return d, ok && d.Component == "" && !d.ReadOnly()
}

// frameManager reports pr as the frame of a tile holding xbin admin,
// minted under its person's own login, whose person manages tile.
func (p *Plane) frameManager(pr auth.Principal, tile string) bool {
	d, ok := p.adminFrameDriver(pr)
	return ok && p.personManages(d, tile)
}

// managerAct refuses anyone but a tile manager in their own session: tile
// credentials (terminal and agent tokens of managers included, since agents
// share them) with the human-session text, other people with the manager's.
// The one tile credential that acts is the admin tile's frame, for an act
// marked frame: it stands in for its person, who is judged as in their own
// session.
func (p *Plane) managerAct(pr auth.Principal, a act, tile string) *Error {
	if pr.Component != "" {
		d, ok := p.adminFrameDriver(pr)
		if !ok || !a.frame {
			return forbidden(a.what + " is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it")
		}
		pr = d
	}
	if !p.personManages(pr, tile) {
		return forbidden(a.what + " is a tile manager's act: the tile's owner, its org's admins, or a workspace admin")
	}
	return nil
}
