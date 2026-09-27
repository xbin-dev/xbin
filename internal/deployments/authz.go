package deployments

// authz.go — the authority of the deployments plane (T9, C4). One function
// judges every act of the /deployments family, the reads included, for every
// principal: Authorize before an operation runs (the dispatcher calls it,
// dispatch.go), Recheck at its commit, Can for the permissions the state
// reports. The ship-dark switch (Plane.OptInClosed, NP-14-5) is read here
// and nowhere else. Beside it, what the tile itself may do (Allowed: P19,
// the admission caps), the refusals of a new deployment and the manager gate
// on joining seeded data (ops_code.go's add), and who learns of a record
// change (the record event's reader form).

import (
	"cmp"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// Op names an act that authority is judged for. An operation's Op is its
// route under /api/xbin/deployments ("live-reload/pause"); the reads have
// their own names. A few Ops refine a route by what its body asks, because
// the body changes who may do it or whether the ship-dark switch closes it:
// OpRestart is deploy with restart:true, OpUnprotect is protect with
// on:false, OpDiffWorkTree is a diff with a work-tree side.
type Op string

// The reads.
const (
	OpState        Op = "state"          // GET /deployments: the state, in the caller's view
	OpLog          Op = "log"            // GET /deployments/log
	OpDiff         Op = "diff"           // GET /deployments/diff between checkpoints
	OpDiffWorkTree Op = "diff/work-tree" // a diff with a work-tree side, which captures
	OpFetch        Op = "checkpoints"    // GET /checkpoints/{rest...}: the checkpoint remote
	OpBackups      Op = "backups"        // GET /deployments/backups
)

// The operations: POST /deployments/<Op>, and the refinements.
const (
	OpPause          Op = "live-reload/pause"
	OpResume         Op = "live-reload/resume"
	OpReloadNow      Op = "live-reload/now"
	OpAttach         Op = "live-reload/attach"
	OpAdd            Op = "add"
	OpRemove         Op = "remove"
	OpDeploy         Op = "deploy"
	OpRestart        Op = "deploy/restart" // deploy with restart:true: moves no code
	OpPromote        Op = "promote"
	OpRollback       Op = "rollback"
	OpPrimary        Op = "primary"
	OpProtect        Op = "protect"
	OpUnprotect      Op = "protect/off" // protect with on:false
	OpEdge           Op = "edge"
	OpDeliveries     Op = "deliveries"
	OpAlwaysOn       Op = "always-on"
	OpLimits         Op = "limits"
	OpSeed           Op = "seed"
	OpReset          Op = "reset"
	OpVaultCopy      Op = "vault-copy"
	OpBackup         Op = "backup"
	OpRestore        Op = "restore"
	OpBackupSchedule Op = "backup-schedule"
	OpRunNow         Op = "run-now"
)

// rule is a row of the authority table: who may do an act.
type rule int

const (
	// ruleRead: the tile's reader audience (Audience decides the view).
	ruleRead rule = iota
	// ruleWrite: the tile's write audience.
	ruleWrite
	// ruleLog: the write audience, or a non-primary deployment's own
	// principals, which read that deployment's entries only.
	ruleLog
	// ruleTerminal: terminal level on the tile, the same power as editing
	// code today: a person who holds it (admins included), or the tile's
	// own terminal and agent sessions while their user holds it.
	ruleTerminal
	// ruleCode: a code move. ruleTerminal, and onto a protected primary
	// also a tile manager in a person's own session (P21); never a token.
	ruleCode
	// ruleLive: live reload onto a deployment. ruleTerminal, and never onto
	// a protected primary, for anyone (P21).
	ruleLive
	// ruleReset: ruleTerminal; resetting main's data while main isn't the
	// primary is a tile manager's act.
	ruleReset
	// ruleManager: a tile manager, in a person's own session.
	ruleManager
	// ruleAdmin: a workspace admin, in a person's own session.
	ruleAdmin
)

// act is what the plane knows about an Op.
type act struct {
	what  string // the act, as a refusal names it
	rule  rule
	read  bool // changes nothing: view-as, the switch and the zero state don't refuse it
	post  bool // a POST route of its own, which the op registry takes (NP-14-3)
	grows bool // creates or extends deployment state: closed by the ship-dark switch (NP-14-5)
	optIn bool // accepted on a tile without a record: the opt-ins
}

// acts is the authority table, one entry per Op.
var acts = map[Op]act{
	OpState:        {what: "reading deployments", rule: ruleRead, read: true},
	OpLog:          {what: "the deploy log", rule: ruleLog, read: true},
	OpDiff:         {what: "a diff", rule: ruleWrite, read: true},
	OpDiffWorkTree: {what: "a diff of the work tree", rule: ruleTerminal, read: true},
	OpFetch:        {what: "fetching checkpoints", rule: ruleWrite, read: true},
	OpBackups:      {what: "listing a deployment's backups", rule: ruleAdmin, read: true},

	OpPause:     {what: "pausing live reload", rule: ruleTerminal, post: true, grows: true, optIn: true},
	OpResume:    {what: "resuming live reload", rule: ruleLive, post: true, grows: true},
	OpReloadNow: {what: "reloading now", rule: ruleCode, post: true, grows: true},
	OpAttach:    {what: "attaching live reload", rule: ruleLive, post: true, grows: true},
	OpAdd:       {what: "adding a deployment", rule: ruleTerminal, post: true, grows: true, optIn: true},
	OpRemove:    {what: "removing a deployment", rule: ruleTerminal, post: true},
	OpDeploy:    {what: "deploying", rule: ruleCode, post: true, grows: true},
	OpRestart:   {what: "restarting a deployment", rule: ruleTerminal},
	OpPromote:   {what: "promoting", rule: ruleCode, post: true, grows: true},
	OpRollback:  {what: "rolling back", rule: ruleCode, post: true, grows: true},

	OpPrimary:    {what: "reassigning the primary", rule: ruleManager, post: true, grows: true},
	OpProtect:    {what: "protecting the primary", rule: ruleManager, post: true, grows: true, optIn: true},
	OpUnprotect:  {what: "unprotecting the primary", rule: ruleManager, optIn: true},
	OpEdge:       {what: "setting an edge policy", rule: ruleManager, post: true, grows: true},
	OpDeliveries: {what: "switching deliveries", rule: ruleManager, post: true, grows: true},
	OpAlwaysOn:   {what: "switching alwaysOn", rule: ruleManager, post: true, grows: true},
	OpLimits:     {what: "setting resource limits", rule: ruleManager, post: true, grows: true},

	OpSeed:           {what: "seeding a deployment's data", rule: ruleManager, post: true, grows: true},
	OpReset:          {what: "resetting a deployment's data", rule: ruleReset, post: true},
	OpVaultCopy:      {what: "copying vault values", rule: ruleManager, post: true, grows: true},
	OpBackup:         {what: "backing up a deployment's data", rule: ruleAdmin, post: true, grows: true},
	OpRestore:        {what: "restoring a deployment's data", rule: ruleAdmin, post: true, grows: true},
	OpBackupSchedule: {what: "scheduling a deployment's backups", rule: ruleAdmin, post: true, grows: true},
	OpRunNow:         {what: "running a job now", rule: ruleTerminal, post: true},
}

// refinements lists the Ops a route may be judged as instead of its own,
// by what its body asks (Handler.Subject); no other substitution is taken,
// so a body can never pick a weaker row.
var refinements = map[Op][]Op{
	OpDeploy:  {OpRestart},
	OpProtect: {OpUnprotect},
}

// Subject is what an act is on, as the record says when it is judged. The
// operation reads it from the record (Handler.Subject) and again at its
// commit (Recheck).
type Subject struct {
	// Tile is the tile's path: a tile ref, resolved.
	Tile string
	// Deployment is the deployment acted on: a code move's target, live
	// reload's new target, the deployment added, removed, reset or seeded.
	// "" for an act on the tile as a whole (protection, edge policies, a
	// read of the whole state).
	Deployment string
	// Primary is the tile's primary; "" means main.
	Primary string
	// Protected: the tile's primary is protected (P21).
	Protected bool
	// Record: the tile has a deployment record; false is the zero state (P5).
	Record bool
	// Seq is the record's sequence number when this was read (0 without a
	// record).
	Seq uint64
}

func (s Subject) primary() string {
	if s.Primary == "" {
		return util.MainDeployment
	}
	return s.Primary
}

// onProtectedPrimary: the act is on the primary, and the primary is
// protected.
func (s Subject) onProtectedPrimary() bool {
	return s.Protected && s.Deployment != "" && s.Deployment == s.primary()
}

// Grant is an authorization: who may do which act on what, judged against
// the record at Subject.Seq (T9). The operation carries it to its commit and
// calls Recheck.
type Grant struct {
	P       auth.Principal
	Op      Op
	Subject Subject
}

// Can is one permission, computed by the policy that will judge the
// request: the state's allowed, caller.can and Deployment.can entries. Kind
// is set with OK false: KindAuthority, KindPolicy or KindState.
type Can struct {
	OK   bool   `json:"ok"`
	Why  string `json:"why,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Audience is what a principal may learn about a tile's deployments: the
// view of the state it gets, and whether it reads the log, the diffs and the
// checkpoint remote.
type Audience int

const (
	// AudienceNone can't read the tile.
	AudienceNone Audience = iota
	// AudienceReader is the rest of the tile's reader audience (people with
	// read, the primary's frame and instance principals): primary-scoped
	// facts only, the reader view.
	AudienceReader
	// AudienceDeployment is a non-primary deployment's own principals: the
	// primary's facts and that deployment's, the deployment view.
	AudienceDeployment
	// AudienceWrite is the tile's write audience (admins, people with write
	// at their current level, the tile's terminal and agent sessions while
	// their user holds write): every fact, the full view.
	AudienceWrite
)

// Authorize judges whether pr may do op on s: its authority first (a view-as
// session is refused every change), then, for a change, the ship-dark switch
// and the zero state (on a tile without a record only the opt-ins are
// accepted). A refusal is an *Error: 403 naming who can act, 409 for a
// policy or a state. The dispatcher calls it before an operation runs, dry
// runs included (Plane.Do).
func (p *Plane) Authorize(pr auth.Principal, op Op, s Subject) (Grant, error) {
	if e := p.judge(pr, op, s, true); e != nil {
		return Grant{}, e
	}
	return Grant{P: pr, Op: op, Subject: s}, nil
}

// Recheck judges an authorized operation again at its commit, against the
// record as it stands now (T9): protection turned on, the primary reassigned
// or the record removed since the request refuses it with the errors
// Authorize gives. An operation calls it where it writes the record, and
// commits only on nil. A grant for another subject never passes.
func (p *Plane) Recheck(g Grant, now Subject) error {
	if _, ok := acts[g.Op]; !ok || now.Tile != g.Subject.Tile || now.Deployment != g.Subject.Deployment {
		return &Error{Status: http.StatusInternalServerError,
			Msg: "deployments: " + string(g.Op) + " on " + g.Subject.Tile + " rechecked against another subject"}
	}
	if e := p.judge(g.P, g.Op, now, true); e != nil {
		return e
	}
	return nil
}

// Can is Authorize as a permission, for the state's caller.can and
// Deployment.can. A view-as session gets the viewed user's answer: its
// read-only flag is reported beside it, so an admin sees exactly what that
// user could do.
func (p *Plane) Can(pr auth.Principal, op Op, s Subject) Can {
	if e := p.judge(pr, op, s, false); e != nil {
		return e.Can()
	}
	return Can{OK: true}
}

// Allowed is what the tile itself may do, whoever asks: the state's allowed
// entries. While the ship-dark switch is off, every act that creates or
// extends deployment state is refused with kind policy (NP-14-5). Adding a
// deployment is refused, kind policy, for a tile that must keep one
// deployment (P19) and at the admission caps (P25), as mayAdd judges it.
func (p *Plane) Allowed(op Op, s Subject) Can {
	if a, ok := acts[op]; ok && p.OptInClosed && grows(op, a, s) {
		return closed(a.what).Can()
	}
	if op == OpAdd {
		if e := p.mayAdd(s.Tile); e != nil {
			return e.Can()
		}
	}
	return Can{OK: true}
}

// mayAdd is what refuses a new deployment on tile whoever asks, from what
// the plane holds in memory: a tile that must keep one deployment (P19),
// then the admission caps (P25). The add judges it again at its request,
// its deployments' checkpoints read as well (ops_code.go).
func (p *Plane) mayAdd(tile string) *Error {
	if why := p.singleDeployment(tile); why != "" {
		return cantHaveDeployments(tile, why)
	}
	c := p.CapsOf(tile)
	switch {
	case c.TileUsed >= c.Tile:
		return &Error{Status: http.StatusConflict, Kind: KindPolicy,
			Msg: tile + " has " + strconv.Itoa(c.TileUsed) + " non-primary deployments, the most allowed here"}
	case c.WorkspaceUsed >= c.Workspace:
		return &Error{Status: http.StatusConflict, Kind: KindPolicy,
			Msg: "the workspace has " + strconv.Itoa(c.WorkspaceUsed) + " non-primary deployments, the most allowed here"}
	}
	return nil
}

// singleDeployment says why tile can't have non-primary deployments (P19;
// 06-security T14), "" when it can: it is workspace chrome — root or shell,
// or its code asks for chrome, the primary's or its work tree's (the flag is
// tile-editable, so asking is enough) — or it holds an xbin or xbin:* grant,
// which governs the workspace.
func (p *Plane) singleDeployment(tile string) string {
	if tile == "root" || tile == "shell" {
		return "it is the workspace's chrome"
	}
	if c, ok := p.component(tile); ok && (c.Manifest.Chrome || c.WorkTreeManifest().Chrome) {
		return chromeWhy
	}
	if p.Reg == nil {
		return ""
	}
	for _, g := range p.Reg.Workspace().Grants {
		if g.From == tile && (g.Target == "xbin" || strings.HasPrefix(g.Target, "xbin:")) {
			return "it holds the " + g.Target + " grant, which governs the workspace"
		}
	}
	return ""
}

// chromeWhy is P19's reason for a tile whose code asks for chrome.
const chromeWhy = "its code asks to be workspace chrome"

func cantHaveDeployments(tile, why string) *Error {
	return &Error{Status: http.StatusConflict, Kind: KindPolicy, Msg: tile + " can't have non-primary deployments: " + why}
}

// CapsOf is State.caps for tile (11-contract §1.1): its non-primary
// deployments and the workspace's, each against its admission cap
// (07-runtime §10.3). Only records that govern their tile count: a held one
// runs nothing.
func (p *Plane) CapsOf(tile string) Caps {
	c := Caps{Tile: MaxNonPrimaryPerTile, Workspace: MaxNonPrimaryPerWorkspace}
	for _, t := range p.boundTiles() {
		if rec, _ := p.record(t); rec != nil {
			n := len(rec.Deployments) - 1
			c.WorkspaceUsed += n
			if t == tile {
				c.TileUsed = n
			}
		}
	}
	return c
}

// Manager reports the manager gate: a person in their own
// session who is a workspace admin or manages the tile, as
// Broker.MayManageDeployments answers it. The human-session clause is
// checked here as well, so no element principal ever passes, whatever its
// tile is granted.
func (p *Plane) Manager(pr auth.Principal, tile string) bool {
	if pr.Component != "" {
		return false
	}
	if p.MayManage != nil {
		return p.MayManage(pr, tile)
	}
	return pr.IsAdmin()
}

// Audience answers who pr is to s.Tile. A view-as session answers as the
// viewed user, since reads do.
func (p *Plane) Audience(pr auth.Principal, s Subject) Audience {
	tile := s.Tile
	switch {
	case pr.Component == "":
		if p.admin(pr) || pr.CanWriteTile(tile) {
			return AudienceWrite
		}
	case pr.Component == tile && pr.Via == "terminal":
		if driverCanWrite(pr, tile) {
			return AudienceWrite
		}
		return AudienceReader
	case pr.Component == tile && (pr.Via == "frame" || pr.Via == "instance"):
		if pr.Deployment != "" && pr.Deployment != s.primary() && driverCanWrite(pr, tile) {
			return AudienceDeployment
		}
		return AudienceReader
	}
	if pr.CanReadTile(tile) {
		return AudienceReader
	}
	return AudienceNone
}

// judge is the one authorize function. viewAs refuses a view-as session's
// changes; Can leaves it out.
func (p *Plane) judge(pr auth.Principal, op Op, s Subject, viewAs bool) *Error {
	a, ok := acts[op]
	if !ok || s.Tile == "" {
		return &Error{Status: http.StatusInternalServerError, Msg: "deployments: " + string(op) + " judged without a known operation and a tile"}
	}
	if viewAs && !a.read && pr.ReadOnly() {
		return &Error{Status: http.StatusForbidden, Kind: KindAuthority, Msg: readOnlyMsg}
	}
	if e := p.authority(pr, a, s); e != nil || a.read {
		return e
	}
	if p.OptInClosed && grows(op, a, s) {
		return closed(a.what)
	}
	if !s.Record && !a.optIn {
		return &Error{Status: http.StatusConflict, Kind: KindState,
			Msg: s.Tile + " has no deployments yet: pause live reload or add a deployment first"}
	}
	return nil
}

// authority applies a's row to pr on s.
func (p *Plane) authority(pr auth.Principal, a act, s Subject) *Error {
	tile := s.Tile
	elem := pr.Component != ""
	switch a.rule {
	case ruleRead:
		if p.Audience(pr, s) == AudienceNone {
			return forbidden("deployments of " + tile + " need read access")
		}
		return nil
	case ruleWrite:
		if p.Audience(pr, s) != AudienceWrite {
			return forbidden("deployments of " + tile + " need write access")
		}
		return nil
	case ruleLog:
		if au := p.Audience(pr, s); au != AudienceWrite && au != AudienceDeployment {
			return forbidden("deployments of " + tile + " need write access")
		}
		return nil
	case ruleManager:
		return p.managerAct(pr, a.what, tile)
	case ruleAdmin:
		if elem || !p.admin(pr) {
			return forbidden(a.what + " is a workspace admin's act, done in a person's own session")
		}
		return nil
	}

	// The terminal rows. A tile credential operates only as the tile's own
	// terminal or agent session (C4, T9.3): frame, instance, cron and bus
	// principals, and every credential of another tile, never do.
	if elem && (pr.Via != "terminal" || pr.Component != tile) {
		return forbidden(a.what + " needs terminal-level access on " + tile +
			" — only people and the tile's own terminal and agent sessions operate its deployments")
	}
	if a.rule == ruleCode && s.onProtectedPrimary() && !p.Manager(pr, tile) {
		return refuseProtected(s, http.StatusForbidden, "")
	}
	if !pr.CanTerminalTileVia(tile) {
		return forbidden(a.what + " needs terminal-level access on " + tile)
	}
	switch {
	case a.rule == ruleLive && s.onProtectedPrimary():
		return refuseProtected(s, http.StatusConflict, "live reload never attaches to a protected primary: unprotect it first")
	case a.rule == ruleReset && s.Deployment == util.MainDeployment && s.primary() != util.MainDeployment:
		return p.managerAct(pr, a.what, tile)
	}
	return nil
}

// managerAct refuses anyone but a tile manager in their own session: tile
// credentials (terminal and agent tokens of managers included, since agents
// share them) with the human-session text, other people with the manager's.
func (p *Plane) managerAct(pr auth.Principal, what, tile string) *Error {
	switch {
	case pr.Component != "":
		return forbidden(what + " is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it")
	case !p.Manager(pr, tile):
		return forbidden(what + " is a tile manager's act: the tile's owner, its org's admins, or a workspace admin")
	}
	return nil
}

// admin is the admin question for a person in their own session.
func (p *Plane) admin(pr auth.Principal) bool {
	if pr.Component != "" {
		return false
	}
	if p.IsAdmin != nil {
		return p.IsAdmin(pr)
	}
	return pr.IsAdmin()
}

// driverCanWrite reports whether the person driving a tile credential holds
// write on tile at their current level (the credential's Access is rebuilt
// on every request); an owner-driven credential, or one no person drives,
// always does.
func driverCanWrite(pr auth.Principal, tile string) bool {
	switch {
	case pr.UserID == "":
		return true
	case pr.Access != nil:
		return pr.Access.CanWriteTile(tile)
	default:
		return pr.User != nil && pr.User.CanWriteTile(tile)
	}
}

// grows reports whether op on s creates or extends deployment state, which
// the ship-dark switch closes. Resuming live reload onto main returns a tile
// toward the zero state, so it stays open (NP-14-5).
func grows(op Op, a act, s Subject) bool {
	return a.grows && !(op == OpResume && s.Deployment == util.MainDeployment)
}

// readOnlyMsg is the refusal of every change in a view-as session: the
// middleware's own text (internal/server/impersonate.go), so the UI shows one
// sentence whichever layer refused.
const readOnlyMsg = "read-only: you are viewing the workspace as another user — exit the view (top banner) to make changes"

func forbidden(msg string) *Error {
	return &Error{Status: http.StatusForbidden, Kind: KindAuthority, Msg: msg}
}

func refuseProtected(s Subject, status int, detail string) *Error {
	msg := "the primary of " + s.Tile + " (" + s.primary() + ") is protected: only tile managers change its code, and not from a terminal or agent session"
	if detail != "" {
		msg += " — " + detail
	}
	return &Error{Status: status, Kind: KindAuthority, Msg: msg}
}

func closed(what string) *Error {
	return &Error{Status: http.StatusConflict, Kind: KindPolicy,
		Msg: what + " is turned off on this xbind (--tile-deployments=off): it would create or extend deployment state; resuming live reload onto main, removing a deployment, resetting its data and unprotecting still work"}
}

// ---- the add's refusals and the record event's audience (ops_code.go) ----

// addable refuses a new deployment y on o's tile before anything is read or
// captured: a name the tile has (main always), a component at <tile>+<y>
// (P17), a tile that must keep one deployment and the caps (mayAdd), and a
// backend tile without isolation (P18).
func (p *Plane) addable(o *op, y string) error {
	if y == util.MainDeployment || o.rec.Deployments[y] != nil {
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: fmt.Sprintf("%s already has a deployment %q", o.tile, y)}
	}
	if _, ok := p.component(o.tile + "+" + y); ok {
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: "a tile exists at " + o.tile + "+" + y + "; pick another name"}
	}
	if e := p.mayAdd(o.tile); e != nil {
		return e
	}
	return p.needsIsolation(o.c)
}

// chromeCode is P19 read from the code itself, at the request (06-security
// T14 item 5): the tile counts as chrome when any of its deployments' code
// asks for it — the primary's and the work tree's are the registry's
// (mayAdd), each non-primary checkpoint is read here — or the new
// deployment's code does (tree, "" for the work tree).
func (p *Plane) chromeCode(o *op, y, tree string) error {
	var trees []string
	for _, name := range sortedKeys(o.rec.Deployments) {
		if cp := o.rec.Deployments[name].Checkpoint; cp != nil && name != o.rec.Primary {
			trees = append(trees, *cp)
		}
	}
	if tree != "" {
		trees = append(trees, tree)
	}
	for _, t := range trees {
		release := runner.NonPrimaryBuildTurn()
		root, err := p.store().Materialize(o.tile, t)
		release()
		var pc *registry.PinnedCode
		if err == nil {
			pc, err = registry.ReadCheckpoint(root)
		}
		switch {
		case err != nil && t == tree:
			return &Error{Status: http.StatusConflict, Kind: KindState,
				Msg: fmt.Sprintf("%s: checkpoint %s can't be read for %s: %v", o.tile, shortTree(t), y, err)}
		case err == nil && pc.Manifest.Chrome:
			return cantHaveDeployments(o.tile, chromeWhy)
		}
	}
	return nil
}

// joining is the (scope, y) namespace a new deployment y joins (08-data
// §6.1): nil when no sibling claims it. Joining one that holds data (seeded,
// restored, partial) gives this tile's writers reach into it: a tile
// manager's act, in a person's own session (§6.2).
func (p *Plane) joining(o *op, pr auth.Principal, y string) (*Joins, error) {
	j, err := p.joinsOf(o.tile, y)
	if err != nil {
		return nil, opError(o.tile, err)
	}
	if err := p.joinGate(pr, o.tile, y, j); err != nil {
		return nil, err
	}
	return j, nil
}

// joinGate refuses pr joining j, the namespace a new deployment y of tile
// would join, while it holds data and pr isn't a tile manager in a person's
// own session: 11-contract §1.14's "joining seeded data" 403.
func (p *Plane) joinGate(pr auth.Principal, tile, y string, j *Joins) error {
	if j == nil {
		return nil
	}
	done := map[string]string{"seeded": "seeded", "restored": "restored", "partial": "partly seeded or restored"}[j.State]
	if done != "" && !p.Manager(pr, tile) {
		when := strings.TrimSpace(j.By + " " + j.At)
		return forbidden(fmt.Sprintf("%s's %q data was %s by %s: joining it is a tile manager's act", j.Scope, y, done, cmp.Or(when, "someone")))
	}
	return nil
}

// announce publishes a committed record change: the full form to the write
// audience, and the reader form only when the reader view moved (reader:
// its fields), so a change that concerns only non-primary deployments tells
// readers nothing, not even that something changed (11-contract §1.3).
func (p *Plane) announce(tile string, rec *Record, by string, what, reader []string) {
	if p.Hub == nil {
		return
	}
	p.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: recordEvent{Op: "record", Seq: rec.Seq, By: by, What: what}})
	if len(reader) > 0 {
		p.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: recordReaderEvent{Op: "record", What: reader}})
	}
}

// readerChange names the fields of the reader view (11-contract §1.3) that
// moved from before to after: live reload as it concerns the primary (its
// name while it follows the work tree, "" otherwise), the primary, its
// protection, and the primary's own row.
func readerChange(before, after *Record) []string {
	var out []string
	lr := func(r *Record) string {
		if r.LiveReload == r.Primary {
			return r.Primary
		}
		return ""
	}
	if lr(before) != lr(after) {
		out = append(out, "liveReload")
	}
	if before.Primary != after.Primary {
		out = append(out, "primary")
	}
	if before.ProtectedPrimary != after.ProtectedPrimary {
		out = append(out, "protectedPrimary")
	}
	pb, pa := before.Deployments[before.Primary], after.Deployments[after.Primary]
	if before.Primary != after.Primary || pb == nil || pa == nil || pb.State != pa.State ||
		(pb.Checkpoint == nil) != (pa.Checkpoint == nil) || (pb.Checkpoint != nil && *pb.Checkpoint != *pa.Checkpoint) {
		out = append(out, "deployments")
	}
	return out
}
