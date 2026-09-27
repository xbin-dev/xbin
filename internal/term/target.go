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
//
// The session's side: pickTarget runs at every session start (both kinds),
// the token is minted for the target (mintTerminal), XBIN_DEPLOYMENT names a
// target that isn't the primary (deploymentEnv), and every answer about the
// session echoes it (echoOf). PrimaryProtected and PrimaryReassigned move
// the sessions a governance act took off their target.

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// ErrTargetProtected is the refusal of a session target on a protected
// primary: 403, 11-contract §1.14's "session target protected".
var ErrTargetProtected = errors.New("session target protected")

// targetProtected is ErrTargetProtected for one tile, with the catalogue's text.
type targetProtected string

func (e targetProtected) Error() string {
	return "the primary of " + string(e) + " is protected: terminal and agent sessions can't target it"
}
func (e targetProtected) Is(target error) bool { return target == ErrTargetProtected }

// badTargetMsg is 11-contract §1.14's "bad deployment name" (400).
const badTargetMsg = `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`

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
// Any error other than util.ErrNoDeployment is a refusal (403,
// ErrTargetProtected). Clients that
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
		return Target{}, targetProtected(tile)
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

// sessionTarget is what a session keeps of its target, all of it fixed when
// the session starts (P24). The zero value is today's session: it follows
// the primary, names nothing, and its env and session frame are unchanged.
type sessionTarget struct {
	Target
	askedPrimary bool           // the client named the primary: the answers echo its name (11-contract §8)
	env          string         // XBIN_DEPLOYMENT; "" leaves the variable out
	note         string         // the session frame's targetNote, "" for none (bx-terminal prints it like netNote)
	apiOff       bool           // a target was asked for or chosen but the session has no tile API (P24's last fallback, or a D17 clamp): the frame echoes api:false
	opener       auth.Principal // who opened it: a governance restart reopens an agent session as them
}

// pickTarget chooses a new session's target for p on tile rel (both kinds;
// the caller has passed the terminal-level gate) and keeps it in o, from
// which the token is minted and the env built (P24). requested is the
// client's ?deployment=, "" for the default. The int is the HTTP status of
// a refusal: 403 for a view-as session (a session is a write, whatever the
// verb) or a protected primary, 404 for an unknown deployment, 400 for a
// string that is no deployment name.
func (m *Manager) pickTarget(p auth.Principal, o *openOpts, rel, requested string) (int, error) {
	if p.ReadOnly() {
		return http.StatusForbidden, errors.New("read-only: you are viewing the workspace as another user — exit the view (top banner) to open terminal and agent sessions")
	}
	if o.api && requested != "" && !util.DeploymentNameOK(requested) {
		return http.StatusBadRequest, errors.New(badTargetMsg)
	}
	d := m.TileDeployments.deploymentsOf(rel)
	t, err := ChooseTarget(rel, d, o.api, requested)
	switch {
	case errors.Is(err, util.ErrNoDeployment):
		return http.StatusNotFound, err
	case err != nil:
		return http.StatusForbidden, err
	}
	st := sessionTarget{Target: t, env: t.DeploymentEnv(d), opener: p,
		askedPrimary: requested != "" && t == (Target{})}
	switch {
	case !o.api && requested != "": // the tile API is off (a D17 clamp): no target to echo, and the frame says why
		st.apiOff = true
	case t.NoAPI && o.api: // the last fallback: a protected primary with live reload paused
		o.api, st.apiOff = false, true
		st.note = "tile API off: " + d.Primary + " is protected and live reload is paused"
	case st.env != "":
		st.note = "this terminal calls " + rel + "+" + t.Deployment
	}
	o.target = st
	return 0, nil
}

// targetMinter is the part of Tokens a named target needs: auth's
// MintTerminalTarget, which binds the token to that deployment
// (11-contract §7.4).
type targetMinter interface {
	MintTerminalTarget(component, userID, target string) string
}

// mintTerminal mints the session's terminal token on rel, bound to its
// target: today's MintTerminal for a session that follows the primary, so
// such a token is today's. A minter that can't bind a deployment (a test
// stub's) mints nothing for a named target: no API rather than an API that
// reaches the primary.
func (m *Manager) mintTerminal(rel string, o openOpts) string {
	if o.target.Deployment == "" {
		return m.Tokens.MintTerminal(rel, o.userID)
	}
	if tm, ok := m.Tokens.(targetMinter); ok {
		return tm.MintTerminalTarget(rel, o.userID, o.target.Deployment)
	}
	slog.Warn("terminal token not minted: the minter can't bind a deployment", "tile", rel, "target", o.target.Deployment)
	return ""
}

// deploymentEnv is the session's XBIN_DEPLOYMENT, which both session env
// paths put next to XBIN_COMPONENT (11-contract §5): only when the target
// isn't the primary at session start; nil otherwise, so a zero-state
// session's env is today's.
func (o openOpts) deploymentEnv() []string {
	if o.target.env == "" {
		return nil
	}
	return []string{"XBIN_DEPLOYMENT=" + o.target.env}
}

// echoOf is the deployment the answers about s name: its session frame, its
// directory row, its term events and its GET /status row (11-contract
// §7.4, and §8's echo rule): its named target; the current primary for a
// session that follows the primary but asked for it by name; "" otherwise,
// so a session that sent no deployment keeps today's wire.
func (m *Manager) echoOf(s *Session) string {
	switch {
	case s.target.Deployment != "":
		return s.target.Deployment
	case s.target.askedPrimary:
		return m.TileDeployments.deploymentsOf(s.Cwd).Primary
	}
	return ""
}

// DeploymentOf is the deployment session id's answers name (echoOf): what a
// `term` event carries. "" for an unknown id.
func (m *Manager) DeploymentOf(id string) string {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return ""
	}
	return m.echoOf(s)
}

// row is s's directory row with its echo.
func (m *Manager) row(s *Session) SessionInfo {
	si := s.info()
	si.Deployment = m.echoOf(s)
	return si
}

// PrimaryProtected moves the sessions on tile that target its primary,
// which a tile manager just protected (11-contract §7.4) (P21): until then
// their tokens are bound to nothing (§7.1). The target is chosen again by
// the default (P24): an agent session restarts as …/restart does, resuming
// its conversation, onto the live reload target; a shell ends, and its
// window says why (the `deployments` event). When the default is "API off" (live
// reload is paused too), every such session ends. Returns how many
// restarted and how many ended. The protect operation calls it once the
// record has committed, outside the plane's locks: it reads TileDeployments.
func (m *Manager) PrimaryProtected(tile string) (restarted, ended int) {
	primary := m.TileDeployments.deploymentsOf(tile).Primary
	return m.retarget(tile, func(t Target) bool { return t.Deployment == "" || t.Deployment == primary })
}

// PrimaryReassigned moves, the same way, the sessions on tile whose target
// is named after the old primary or the new one (11-contract §7.4), so
// XBIN_DEPLOYMENT stays true and no session is left on a protected
// primary. Sessions that follow the primary follow the new one without a
// restart. Called like PrimaryProtected, after the reassignment commits.
func (m *Manager) PrimaryReassigned(tile, from, to string) (restarted, ended int) {
	return m.retarget(tile, func(t Target) bool { return t.Deployment != "" && (t.Deployment == from || t.Deployment == to) })
}

// retarget restarts or ends the live sessions on tile whose target moved;
// sessions without the tile API have no target and stay. An agent
// session's restart runs in the background: it waits for the old session's
// teardown, which saves the transcript it resumes from.
func (m *Manager) retarget(tile string, moved func(Target) bool) (restarted, ended int) {
	def, _ := ChooseTarget(tile, m.TileDeployments.deploymentsOf(tile), true, "")
	for _, s := range m.sorted() {
		if s.Cwd != tile || !s.api || !moved(s.target.Target) {
			continue
		}
		if s.kind == KindAgent && !def.NoAPI {
			vm := s.vm
			go func() {
				if _, _, _, err := m.RestartAgent(s.target.opener, s.ID, s.Net, s.gpu, true, &vm); err != nil {
					slog.Warn("session target moved: the agent session did not restart", "id", s.ID, "tile", tile, "err", err)
				}
			}()
			restarted++
			continue
		}
		slog.Info("session target moved: ending the session", "id", s.ID, "tile", tile, "kind", s.kind)
		s.kill()
		ended++
	}
	return restarted, ended
}
