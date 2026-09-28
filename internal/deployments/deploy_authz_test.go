package deployments_test

// The authority matrix (05-model §10; 06-security T9; 15-test-plan §3.7) on
// a plane whose gates are a real broker's, with a users store: the manager
// gate is Broker.MayManageDeployments, the admin question Broker.IsAdmin,
// exactly as boot wires them. An external test package, so the broker can
// be imported whatever the broker imports later.

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

const (
	azCRM   = "apps/crm"   // owned by org:devs; carol is its admin, dev a member at terminal level
	azSolo  = "apps/solo"  // owned by user:ana
	azAdmin = "apps/admin" // granted xbin (admin) and xbin:users (writer)
	azOther = "apps/other"
)

type azFixture struct {
	brk *broker.Broker
	st  *users.Store
	pl  *deployments.Plane
}

func newAZFixture(t *testing.T) *azFixture {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"schema":1,"grants":[
		{"from":"apps/admin","target":"xbin","role":"admin"},
		{"from":"apps/admin","target":"xbin:users","role":"writer"}]}`)
	for _, tile := range []string{azCRM, azSolo, azAdmin, azOther} {
		write(tile+"/xbin.json", `{"runtime":"go"}`)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	brk, err := broker.New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	brk.Users = st
	for _, u := range []users.User{
		{ID: "wsadmin", Role: users.RoleAdmin},
		{ID: "carol", Role: users.RoleUser},
		{ID: "dev", Role: users.RoleUser},
		{ID: "ana", Role: users.RoleUser},
		{ID: "wanda", Role: users.RoleUser, Tiles: map[string]string{azCRM: users.LevelWrite}},
		{ID: "rita", Role: users.RoleUser, Tiles: map[string]string{azCRM: users.LevelRead}},
		{ID: "nora", Role: users.RoleUser, Tiles: map[string]string{azCRM: users.LevelTerminal}},
		{ID: "tess", Role: users.RoleUser, Tiles: map[string]string{azCRM: users.LevelTerminal}},
		{ID: "olly", Role: users.RoleUser},
	} {
		if _, err := st.Upsert(u, "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpsertOrg(users.Org{ID: "devs", Members: []users.Member{
		{ID: "carol", Level: users.LevelTerminal, Admin: true},
		{ID: "dev", Level: users.LevelTerminal},
	}}); err != nil {
		t.Fatal(err)
	}
	for tile, ref := range map[string]string{azCRM: "org:devs", azSolo: "user:ana"} {
		if err := st.SetOwner(tile, ref); err != nil {
			t.Fatal(err)
		}
	}
	yes := true
	if _, err := st.SetUserPersonal("nora", users.PersonalPatch{NoTerminal: &yes}); err != nil {
		t.Fatal(err)
	}
	pl := &deployments.Plane{Reg: reg, OwnerRef: st.Owner, IsAdmin: brk.IsAdmin, MayManage: brk.MayManageDeployments,
		AdminFrameDriver: brk.AdminFrameDriver}
	return &azFixture{brk: brk, st: st, pl: pl}
}

// person is id in their own browser session, as the auth layer builds it on
// every request.
func (f *azFixture) person(t *testing.T, id string) auth.Principal {
	t.Helper()
	u, ok := f.st.Get(id)
	if !ok {
		t.Fatalf("no user %q", id)
	}
	a, _ := f.st.Access(id)
	return auth.Principal{UserID: id, User: u, Access: a, Via: "session"}
}

// token is a terminal or agent session's token on tile, driven by id (agent
// sessions mint terminal tokens: the same principal).
func (f *azFixture) token(t *testing.T, id, tile string) auth.Principal {
	t.Helper()
	a, _ := f.st.Access(id)
	return auth.Principal{Component: tile, UserID: id, Access: a, Via: "terminal"}
}

// frame is tile's frame token (or tile-origin cookie) minted for id.
func (f *azFixture) frame(t *testing.T, id, tile string) auth.Principal {
	t.Helper()
	a, _ := f.st.Access(id)
	return auth.Principal{Component: tile, UserID: id, Access: a, Via: "frame"}
}

// loginFrame is tile's frame token minted under id's own login, gen: a
// session's ("s.") or the root token's ("o.", id ""), or one a terminal
// minted ("u.", "t.") — what auth.Principal.LoginFrame reads.
func (f *azFixture) loginFrame(t *testing.T, id, tile, gen string) auth.Principal {
	t.Helper()
	p := auth.Principal{Component: tile, UserID: id, Via: "frame", Gen: gen}
	if id != "" {
		p = f.frame(t, id, tile)
		p.Gen = gen
	}
	return p
}

// who is one principal of the matrix, and the tile it acts on.
type azWho struct {
	name  string
	group string
	p     auth.Principal
	tile  string
}

// Groups of the matrix's columns (15-test-plan §3.7).
const (
	azGManager = "manager"  // Managers: the owner token, workspace admins, the owning org's admins, the user-owner
	azGTerm    = "term"     // Term: a terminal-level user who isn't a manager
	azGToken   = "token"    // Tokens: the tile's terminal and agent tokens while their user holds terminal
	azGPerson  = "person"   // Write, Read, NoTerm and outsiders: people without terminal level
	azGOwnLow  = "own-low"  // the tile's own terminal tokens whose user lost terminal level (NoTerm, or every level)
	azGCred    = "cred"     // Backend, frames, Other, Xbin-el: tile credentials that never operate
	azGViewAs  = "view-as"  // an admin viewing the workspace as the terminal-level user
	azGAdminWS = "admin-ws" // managers who are workspace admins (the owner token, admin users)
	// The admin tile's frame (P21, extended by the owner 2026-09-28): a
	// frame of a tile granted xbin admin, minted under a login.
	azGAdminFrame    = "admin-frame"     // under a manager's own login: stands in for them on the frame acts
	azGAdminFrameLow = "admin-frame-low" // under a non-manager's login: the person fails the gate
	azGViewAsFrame   = "view-as-frame"   // under an admin's view of the terminal-level user
)

// azFrameReasons is a row's reasons for the admin tile's frames outside the
// manager acts they may do: a tile credential's (a view-as one's, read-only).
func azFrameReasons(reason string) map[string]string {
	return map[string]string{azGAdminFrame: reason, azGAdminFrameLow: reason, azGViewAsFrame: azReadOnly}
}

func (f *azFixture) principals(t *testing.T) []azWho {
	viewAs := f.person(t, "dev")
	viewAs.Impersonator = "wsadmin"
	viewAsFrame := f.loginFrame(t, "dev", azAdmin, "s.v")
	viewAsFrame.Impersonator = "wsadmin"
	return []azWho{
		{"the admin tile's frame, a workspace admin's login", azGAdminFrame, f.loginFrame(t, "wsadmin", azAdmin, "s.w"), azCRM},
		{"the admin tile's frame, the owning org's admin's login", azGAdminFrame, f.loginFrame(t, "carol", azAdmin, "s.c"), azCRM},
		{"the admin tile's frame, the user-owner's login", azGAdminFrame, f.loginFrame(t, "ana", azAdmin, "s.a"), azSolo},
		{"the admin tile's frame, the root token's login", azGAdminFrame, f.loginFrame(t, "", azAdmin, "o.r"), azCRM},
		{"the admin tile's frame, a terminal-level non-manager's login", azGAdminFrameLow, f.loginFrame(t, "dev", azAdmin, "s.d"), azCRM},
		{"the admin tile's frame, a workspace admin's terminal minted it", azGCred, f.loginFrame(t, "wsadmin", azAdmin, "u.e.0"), azCRM},
		{"the admin tile's frame, an owner-driven terminal minted it", azGCred, f.loginFrame(t, "", azAdmin, "t.r"), azCRM},
		{"the admin tile's terminal token, a workspace admin's", azGCred, f.token(t, "wsadmin", azAdmin), azCRM},
		{"the admin tile's agent token, owner-driven", azGCred, auth.Principal{Component: azAdmin, Via: "terminal"}, azCRM},
		{"the admin tile's frame, an admin's view-as", azGViewAsFrame, viewAsFrame, azCRM},
		{"the owner token", azGAdminWS, auth.Principal{Owner: true, Via: "bearer"}, azCRM},
		{"a workspace admin", azGAdminWS, f.person(t, "wsadmin"), azCRM},
		{"the owning org's admin", azGManager, f.person(t, "carol"), azCRM},
		{"the tile's user-owner", azGManager, f.person(t, "ana"), azSolo},
		{"a terminal-level user", azGTerm, f.person(t, "dev"), azCRM},
		{"a writer", azGPerson, f.person(t, "wanda"), azCRM},
		{"a reader", azGPerson, f.person(t, "rita"), azCRM},
		{"an outsider", azGPerson, f.person(t, "olly"), azCRM},
		{"a noTerminal account holding terminal", azGPerson, f.person(t, "nora"), azCRM},
		{"the tile's terminal token", azGToken, f.token(t, "dev", azCRM), azCRM},
		{"the tile's agent token", azGToken, f.token(t, "dev", azCRM), azCRM},
		{"an owner-driven terminal token", azGToken, auth.Principal{Component: azCRM, Via: "terminal"}, azCRM},
		{"a manager's terminal token", azGToken, f.token(t, "carol", azCRM), azCRM},
		{"a noTerminal account's terminal token", azGOwnLow, f.token(t, "nora", azCRM), azCRM},
		{"a terminal token whose user lost access", azGOwnLow, f.token(t, "olly", azCRM), azCRM},
		{"the tile's instance token", azGCred, auth.Principal{Component: azCRM, Via: "instance"}, azCRM},
		{"the tile's frame token", azGCred, f.frame(t, "dev", azCRM), azCRM},
		{"another tile's instance token", azGCred, auth.Principal{Component: azOther, Via: "instance"}, azCRM},
		{"another tile's terminal token", azGCred, f.token(t, "dev", azOther), azCRM},
		{"an xbin-granted tile's instance token", azGCred, auth.Principal{Component: azAdmin, Via: "instance"}, azCRM},
		{"an xbin-granted tile's frame token", azGCred, f.frame(t, "wsadmin", azAdmin), azCRM},
		{"a view-as session", azGViewAs, viewAs, azCRM},
	}
}

// The refusals a cell expects.
const (
	azOK           = ""
	azTerminal     = "terminal"     // 403 "<act> needs terminal-level access on <tile>"
	azCredential   = "credential"   // 403, the same with the tile-credential detail
	azManager      = "manager"      // 403 "<act> is a tile manager's act: …"
	azSession      = "session"      // 403 "<act> is a tile manager's act, done in a person's own session: …"
	azAdminAct     = "admin"        // 403 "<act> is a workspace admin's act, …"
	azProtected    = "protected"    // 403 the protected-primary text
	azProtected409 = "protected409" // 409 the protected-primary text, with the live-reload detail
	azReadOnly     = "read-only"    // 403 the view-as text
	azNeedRead     = "need-read"    // 403 "deployments of <tile> need read access"
	azNeedWrite    = "need-write"   // 403 "deployments of <tile> need write access"
)

// azCheckRefusal reports what's wrong with err against the refusal reason.
func azCheckRefusal(err error, reason, tile string) string {
	var e *deployments.Error
	if !errors.As(err, &e) {
		return "not an *Error: " + azErrString(err)
	}
	protected := "the primary of " + tile + " (main) is protected: only tile managers change its code, and not from a terminal or agent session"
	status, ok := http.StatusForbidden, false
	switch reason {
	case azTerminal:
		ok = strings.HasSuffix(e.Msg, " needs terminal-level access on "+tile)
	case azCredential:
		ok = strings.HasSuffix(e.Msg, " needs terminal-level access on "+tile+
			" — only people and the tile's own terminal and agent sessions operate its deployments")
	case azManager:
		ok = strings.HasSuffix(e.Msg, " is a tile manager's act: the tile's owner, its org's admins, or a workspace admin")
	case azSession:
		ok = strings.HasSuffix(e.Msg, " is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it")
	case azAdminAct:
		ok = strings.HasSuffix(e.Msg, " is a workspace admin's act, done in a person's own session")
	case azProtected:
		ok = e.Msg == protected
	case azProtected409:
		status, ok = http.StatusConflict, strings.HasPrefix(e.Msg, protected+" — ")
	case azReadOnly:
		ok = e.Msg == "read-only: you are viewing the workspace as another user — exit the view (top banner) to make changes"
	case azNeedRead:
		ok = e.Msg == "deployments of "+tile+" need read access"
	case azNeedWrite:
		ok = e.Msg == "deployments of "+tile+" need write access"
	}
	switch {
	case !ok:
		return "message " + e.Msg
	case e.Status != status:
		return "status " + http.StatusText(e.Status)
	case e.Kind != deployments.KindAuthority:
		return "kind " + e.Kind
	case e.Status == http.StatusForbidden && e.Docs() != "/docs/auth.md":
		return "docs " + e.Docs()
	case strings.HasPrefix(e.Msg, " ") || e.Msg == "":
		return "no act named"
	}
	return ""
}

func azErrString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

// cell is one act of a matrix row: the op, the deployment it is on, and the
// record's facts that matter.
type azCell struct {
	op        deployments.Op
	dep       string
	protected bool
	primary   string
}

func (c azCell) subject(tile string) deployments.Subject {
	return deployments.Subject{Tile: tile, Deployment: c.dep, Primary: c.primary, Protected: c.protected, Record: true, Seq: 7}
}

// covers P4 P11 P21 P26 T9 — the authority matrix, one subtest per cell:
// principal class × operation × protection. The M1 rows (pausing and
// resuming live reload, reload now; deploy, promote and roll back onto an
// unprotected primary, parity with saving, P4; restart) pass managers,
// terminal-level users and the tile's terminal and agent tokens, and refuse
// people below terminal level, noTerminal accounts and every other tile
// credential. The manager column: governance acts pass only tile managers in
// their own session, never a manager's token nor an element of a tile
// granted xbin and xbin:users; code moves onto a protected primary pass only
// them too, and live reload never attaches to one. Workspace-admin acts pass
// admins only. Each refusal names who may act; Can agrees with Authorize.
// The reads decide the audience (11-contract §1.3's views).
func TestDeployAuthzMatrix(t *testing.T) {
	f := newAZFixture(t)
	principals := f.principals(t)

	type row struct {
		name  string
		cells []azCell
		want  map[string]string // group → reason
		// override names the principals whose reason differs from their group's
		override map[string]string
	}
	// plus is m with the admin tile's frames' reasons (azFrameReasons).
	plus := func(m, frames map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			out[k] = v
		}
		for k, v := range frames {
			out[k] = v
		}
		return out
	}
	unprotected := plus(map[string]string{azGAdminWS: azOK, azGManager: azOK, azGTerm: azOK, azGToken: azOK,
		azGPerson: azTerminal, azGOwnLow: azTerminal, azGCred: azCredential, azGViewAs: azReadOnly}, azFrameReasons(azCredential))
	rows := []row{
		{
			name: "live reload and code moves (M1), and non-primary targets",
			cells: []azCell{
				{op: deployments.OpPause, dep: "main"}, {op: deployments.OpPause, dep: "dev", protected: true},
				{op: deployments.OpResume, dep: "main"}, {op: deployments.OpResume, dep: "dev", protected: true},
				{op: deployments.OpReloadNow, dep: "main"}, {op: deployments.OpReloadNow, dep: "dev", protected: true},
				{op: deployments.OpDeploy, dep: "main"}, {op: deployments.OpDeploy, dep: "dev", protected: true},
				{op: deployments.OpPromote, dep: "main"}, {op: deployments.OpPromote, dep: "dev", protected: true},
				{op: deployments.OpRollback, dep: "main"}, {op: deployments.OpRollback, dep: "dev", protected: true},
				{op: deployments.OpRestart, dep: "main"}, {op: deployments.OpRestart, dep: "main", protected: true},
				{op: deployments.OpAttach, dep: "dev"}, {op: deployments.OpAttach, dep: "dev", protected: true},
				{op: deployments.OpAdd, dep: "dev"}, {op: deployments.OpRemove, dep: "dev", protected: true},
				{op: deployments.OpReset, dep: "dev"}, {op: deployments.OpRunNow, dep: "dev", protected: true},
			},
			want: unprotected,
		},
		{
			name: "code moves onto a protected primary",
			cells: []azCell{
				{op: deployments.OpDeploy, dep: "main", protected: true},
				{op: deployments.OpPromote, dep: "main", protected: true},
				{op: deployments.OpRollback, dep: "main", protected: true},
				{op: deployments.OpReloadNow, dep: "main", protected: true},
			},
			want: plus(map[string]string{azGAdminWS: azOK, azGManager: azOK, azGTerm: azProtected, azGToken: azProtected,
				azGPerson: azProtected, azGOwnLow: azProtected, azGCred: azCredential, azGViewAs: azReadOnly}, azFrameReasons(azCredential)),
		},
		{
			name:  "live reload onto a protected primary",
			cells: []azCell{{op: deployments.OpResume, dep: "main", protected: true}, {op: deployments.OpAttach, dep: "main", protected: true}},
			want: plus(map[string]string{azGAdminWS: azProtected409, azGManager: azProtected409, azGTerm: azProtected409, azGToken: azProtected409,
				azGPerson: azTerminal, azGOwnLow: azTerminal, azGCred: azCredential, azGViewAs: azReadOnly}, azFrameReasons(azCredential)),
		},
		{
			// P21, extended by the owner 2026-09-28: the admin tile's frame
			// does these for a person who manages the tile, and only these.
			name: "governance the admin tile does: tile managers, in their own session or through the admin tile",
			cells: []azCell{
				{op: deployments.OpPrimary, dep: "dev"}, {op: deployments.OpPrimary, dep: "dev", protected: true},
				{op: deployments.OpProtect}, {op: deployments.OpUnprotect, protected: true},
				{op: deployments.OpDeliveries, dep: "dev"}, {op: deployments.OpAlwaysOn, dep: "dev"},
			},
			want: map[string]string{azGAdminWS: azOK, azGManager: azOK, azGTerm: azManager, azGToken: azSession,
				azGPerson: azManager, azGOwnLow: azSession, azGCred: azSession, azGViewAs: azReadOnly,
				azGAdminFrame: azOK, azGAdminFrameLow: azManager, azGViewAsFrame: azReadOnly},
		},
		{
			name: "governance: tile managers, in a person's own session",
			cells: []azCell{
				{op: deployments.OpEdge}, {op: deployments.OpLimits, dep: "dev"}, {op: deployments.OpSeed, dep: "dev"},
				{op: deployments.OpVaultCopy, dep: "dev"}, {op: deployments.OpPurge}, {op: deployments.OpPurge, protected: true},
			},
			want: plus(map[string]string{azGAdminWS: azOK, azGManager: azOK, azGTerm: azManager, azGToken: azSession,
				azGPerson: azManager, azGOwnLow: azSession, azGCred: azSession, azGViewAs: azReadOnly}, azFrameReasons(azSession)),
		},
		{
			name:  "resetting main's data while it isn't the primary",
			cells: []azCell{{op: deployments.OpReset, dep: "main", primary: "dev"}},
			want: plus(map[string]string{azGAdminWS: azOK, azGManager: azOK, azGTerm: azManager, azGToken: azSession,
				azGPerson: azTerminal, azGOwnLow: azTerminal, azGCred: azCredential, azGViewAs: azReadOnly}, azFrameReasons(azCredential)),
		},
		{
			name: "a workspace admin's acts, in a person's own session",
			cells: []azCell{{op: deployments.OpBackup, dep: "dev"}, {op: deployments.OpRestore, dep: "dev"},
				{op: deployments.OpBackupSchedule, dep: "dev"}},
			want: plus(map[string]string{azGAdminWS: azOK, azGManager: azAdminAct, azGTerm: azAdminAct, azGToken: azAdminAct,
				azGPerson: azAdminAct, azGOwnLow: azAdminAct, azGCred: azAdminAct, azGViewAs: azReadOnly}, azFrameReasons(azAdminAct)),
		},
		{
			name:  "reads: the state",
			cells: []azCell{{op: deployments.OpState}},
			want: map[string]string{azGAdminWS: azOK, azGManager: azOK, azGTerm: azOK, azGToken: azOK,
				azGPerson: azOK, azGOwnLow: azOK, azGCred: azOK, azGViewAs: azOK,
				azGAdminFrame: azOK, azGAdminFrameLow: azOK, azGViewAsFrame: azOK},
			override: map[string]string{"an outsider": azNeedRead,
				"another tile's instance token": azNeedRead, "an xbin-granted tile's instance token": azNeedRead},
		},
		{
			name:  "reads: the log, diffs between checkpoints, the checkpoint remote",
			cells: []azCell{{op: deployments.OpLog}, {op: deployments.OpDiff}, {op: deployments.OpFetch}},
			want: map[string]string{azGAdminWS: azOK, azGManager: azOK, azGTerm: azOK, azGToken: azOK,
				azGPerson: azNeedWrite, azGOwnLow: azOK, azGCred: azNeedWrite, azGViewAs: azOK,
				azGAdminFrame: azOK, azGAdminFrameLow: azNeedWrite, azGViewAsFrame: azNeedWrite},
			override: map[string]string{"a writer": azOK, "a noTerminal account holding terminal": azOK,
				"a terminal token whose user lost access": azNeedWrite},
		},
		{
			name:  "reads: a diff of the work tree, which captures",
			cells: []azCell{{op: deployments.OpDiffWorkTree}},
			want: map[string]string{azGAdminWS: azOK, azGManager: azOK, azGTerm: azOK, azGToken: azOK,
				azGPerson: azTerminal, azGOwnLow: azTerminal, azGCred: azCredential, azGViewAs: azOK,
				azGAdminFrame: azCredential, azGAdminFrameLow: azCredential, azGViewAsFrame: azCredential},
		},
		{
			name:  "reads: a deployment's backups",
			cells: []azCell{{op: deployments.OpBackups, dep: "dev"}},
			want: map[string]string{azGAdminWS: azOK, azGManager: azAdminAct, azGTerm: azAdminAct, azGToken: azAdminAct,
				azGPerson: azAdminAct, azGOwnLow: azAdminAct, azGCred: azAdminAct, azGViewAs: azAdminAct,
				azGAdminFrame: azAdminAct, azGAdminFrameLow: azAdminAct, azGViewAsFrame: azAdminAct},
		},
	}

	covered := map[deployments.Op]bool{}
	for _, r := range rows {
		for _, c := range r.cells {
			covered[c.op] = true
			for _, w := range principals {
				reason, ok := r.override[w.name]
				if !ok {
					if reason, ok = r.want[w.group]; !ok {
						t.Fatalf("row %q has no reason for the %s group", r.name, w.group)
					}
				}
				name := r.name + "/" + string(c.op) + "@" + c.dep
				if c.protected {
					name += "(protected)"
				}
				t.Run(name+"/"+w.name, func(t *testing.T) {
					s := c.subject(w.tile)
					g, err := f.pl.Authorize(w.p, c.op, s)
					if reason == azOK {
						if err != nil {
							t.Fatalf("refused: %v", err)
						}
						if g.Op != c.op || g.Subject != s || g.P.From() != w.p.From() {
							t.Errorf("grant %+v doesn't name the act judged", g)
						}
					} else if bad := azCheckRefusal(err, reason, w.tile); bad != "" {
						t.Fatalf("want the %s refusal: %s", reason, bad)
					}
					if (w.group == azGViewAs || w.group == azGViewAsFrame) && reason == azReadOnly {
						return // Can answers as the viewed user (TestViewAsRefusedEveryOp)
					}
					can := f.pl.Can(w.p, c.op, s)
					if can.OK != (err == nil) || (err != nil && can.Why != err.Error()) {
						t.Errorf("Can %+v disagrees with Authorize (%v)", can, err)
					}
				})
			}
		}
	}
	for _, op := range append(deployments.MutatingActs(), deployments.ReadActs()...) {
		if !covered[op] {
			t.Errorf("%s has no row in the matrix", op)
		}
	}

	// The audiences behind the read rows (11-contract §1.3): the view each
	// principal gets of the state.
	wantAudience := map[string]deployments.Audience{
		"a reader":    deployments.AudienceReader,
		"an outsider": deployments.AudienceNone,
		"a terminal token whose user lost access": deployments.AudienceReader,
		"the tile's instance token":               deployments.AudienceReader,
		"the tile's frame token":                  deployments.AudienceReader,
		"another tile's instance token":           deployments.AudienceNone,
		"another tile's terminal token":           deployments.AudienceReader,
		"an xbin-granted tile's instance token":   deployments.AudienceNone,
		"an xbin-granted tile's frame token":      deployments.AudienceReader,
		// The admin tile's frame is the write audience of what its person
		// manages, through a login frame only (P21 extended).
		"the admin tile's frame, a terminal-level non-manager's login":   deployments.AudienceReader,
		"the admin tile's frame, a workspace admin's terminal minted it": deployments.AudienceReader,
		"the admin tile's frame, an owner-driven terminal minted it":     deployments.AudienceReader,
		"the admin tile's terminal token, a workspace admin's":           deployments.AudienceReader,
		"the admin tile's agent token, owner-driven":                     deployments.AudienceReader,
		"the admin tile's frame, an admin's view-as":                     deployments.AudienceReader,
	}
	for _, w := range principals {
		want, ok := wantAudience[w.name]
		if !ok {
			want = deployments.AudienceWrite
		}
		if got := f.pl.Audience(w.p, deployments.Subject{Tile: w.tile, Record: true}); got != want {
			t.Errorf("Audience(%s) = %d, want %d", w.name, got, want)
		}
	}
}

// covers T9 P11 — noTerminal accounts (D88) are capped at write: holding
// terminal level on the tile, neither their session nor a terminal token
// they drive operates any deployment, protected or not, while a twin
// account without the cap does; the cap applies at the next request, since
// a principal's Access is rebuilt on each. They keep the write audience: the
// full state, the log, diffs and the checkpoint remote, but no work-tree
// diff, which captures.
func TestNoTerminalCannotOperate(t *testing.T) {
	f := newAZFixture(t)
	nora, noraToken := f.person(t, "nora"), f.token(t, "nora", azCRM)
	tess, tessToken := f.person(t, "tess"), f.token(t, "tess", azCRM)

	for _, op := range deployments.MutatingActs() {
		for _, protected := range []bool{false, true} {
			s := deployments.Subject{Tile: azCRM, Deployment: "dev", Protected: protected, Record: true, Seq: 3}
			for _, p := range []auth.Principal{nora, noraToken} {
				_, err := f.pl.Authorize(p, op, s)
				var e *deployments.Error
				if !errors.As(err, &e) || e.Status != http.StatusForbidden || e.Kind != deployments.KindAuthority {
					t.Errorf("%s by %s (%s, protected %v): %v, want a 403", op, p.UserID, p.Via, protected, err)
				}
			}
		}
	}
	for _, op := range []deployments.Op{deployments.OpPause, deployments.OpDeploy, deployments.OpAdd, deployments.OpRunNow} {
		s := deployments.Subject{Tile: azCRM, Deployment: "dev", Record: true}
		for _, p := range []auth.Principal{tess, tessToken} {
			if _, err := f.pl.Authorize(p, op, s); err != nil {
				t.Errorf("control: %s by tess (%s): %v", op, p.Via, err)
			}
		}
	}

	// The flag applies at once: the next request's principal carries it.
	yes := true
	if _, err := f.st.SetUserPersonal("tess", users.PersonalPatch{NoTerminal: &yes}); err != nil {
		t.Fatal(err)
	}
	s := deployments.Subject{Tile: azCRM, Deployment: "main", Record: true}
	for _, p := range []auth.Principal{f.person(t, "tess"), f.token(t, "tess", azCRM)} {
		if _, err := f.pl.Authorize(p, deployments.OpDeploy, s); err == nil {
			t.Errorf("tess (%s) still deploys after noTerminal was set", p.Via)
		}
	}

	for _, p := range []auth.Principal{nora, noraToken} {
		if got := f.pl.Audience(p, s); got != deployments.AudienceWrite {
			t.Errorf("Audience(nora, %s) = %d, want the write audience", p.Via, got)
		}
		for _, op := range []deployments.Op{deployments.OpState, deployments.OpLog, deployments.OpDiff, deployments.OpFetch} {
			if _, err := f.pl.Authorize(p, op, s); err != nil {
				t.Errorf("%s by nora (%s): %v", op, p.Via, err)
			}
		}
		if _, err := f.pl.Authorize(p, deployments.OpDiffWorkTree, s); err == nil {
			t.Errorf("a work-tree diff by nora (%s) passed", p.Via)
		}
	}
}

// covers C4 T9 P11 P26 — the tile's own runtime principals never operate
// its deployments: its instance, frame and tile-origin principals, cron and
// bus principals (today's synthetic ones, and ones bound to the tile and a
// deployment), refused by every operation, protected or not, with a record
// or without. A tile holding xbin (admin) and xbin:users (writer) grants
// gets no further: Broker.IsAdmin admits its elements, yet they are refused
// on their own tile and on every other, its own terminal token is refused
// every manager and admin act, and a plane whose gates wrongly admit every
// principal still refuses them all. Their reads stay the reader view.
func TestOwnRuntimePrincipalsCannotOperate(t *testing.T) {
	f := newAZFixture(t)
	adminInstance := auth.Principal{Component: azAdmin, Via: "instance"}
	if !f.brk.IsAdmin(adminInstance) {
		t.Fatal("fixture: the xbin-granted tile's instance should pass Broker.IsAdmin")
	}
	if f.brk.MayManageDeployments(adminInstance, azCRM) || f.brk.MayManageDeployments(adminInstance, azAdmin) {
		t.Fatal("the broker's manager gate admitted an element principal")
	}

	runtime := func(tile string) []auth.Principal {
		return []auth.Principal{
			{Component: tile, Via: "instance"},
			f.frame(t, "wsadmin", tile),
			{Component: tile, UserID: "wsadmin", Via: "frame", Gen: "g1"}, // a tile-origin cookie
			{Component: tile, Via: "instance", Deployment: "dev"},
			{Component: broker.CronPrincipal, Via: "cron", Role: "admin"},
			{Component: broker.BusPrincipal, Via: "bus", Role: "writer"},
			{Component: tile, Via: "cron", Role: "admin", Deployment: "dev"},
			{Component: tile, Via: "bus", Role: "writer"},
		}
	}
	leaky := &deployments.Plane{
		IsAdmin:   func(auth.Principal) bool { return true },
		MayManage: func(auth.Principal, string) bool { return true },
	}
	planes := map[string]*deployments.Plane{"the broker's gates": f.pl, "gates admitting everyone": leaky}

	for pname, pl := range planes {
		for _, target := range []string{azCRM, azAdmin} {
			principals := runtime(target)
			if target == azCRM {
				principals = append(principals, runtime(azAdmin)[:4]...)
			}
			for _, p := range principals {
				for _, op := range deployments.MutatingActs() {
					for _, s := range []deployments.Subject{
						{Tile: target, Deployment: "main", Record: true, Seq: 4},
						{Tile: target, Deployment: "main", Protected: true, Record: true, Seq: 4},
						{Tile: target, Deployment: "dev", Record: true, Seq: 4},
						{Tile: target, Deployment: "main"},
					} {
						_, err := pl.Authorize(p, op, s)
						var e *deployments.Error
						if !errors.As(err, &e) || e.Status != http.StatusForbidden || e.Kind != deployments.KindAuthority {
							t.Errorf("%s: %s (%s of %s) on %+v: %v, want a 403", pname, op, p.Via, p.Component, s, err)
						}
					}
				}
			}
		}

		// The granted tile's own terminal token, driven by a workspace admin:
		// terminal level, never a manager.
		adminToken := f.token(t, "wsadmin", azAdmin)
		refused := map[deployments.Op]bool{}
		for _, op := range []deployments.Op{deployments.OpPrimary, deployments.OpProtect, deployments.OpUnprotect,
			deployments.OpEdge, deployments.OpDeliveries, deployments.OpAlwaysOn, deployments.OpLimits,
			deployments.OpSeed, deployments.OpVaultCopy, deployments.OpBackup, deployments.OpRestore,
			deployments.OpBackupSchedule, deployments.OpPurge} {
			refused[op] = true
		}
		for _, op := range deployments.MutatingActs() {
			s := deployments.Subject{Tile: azAdmin, Deployment: "dev", Record: true}
			if _, err := pl.Authorize(adminToken, op, s); refused[op] && err == nil {
				t.Errorf("%s: the xbin-granted tile's own terminal token passed %s", pname, op)
			}
		}
		for _, op := range []deployments.Op{deployments.OpDeploy, deployments.OpPromote, deployments.OpRollback,
			deployments.OpReloadNow, deployments.OpResume, deployments.OpAttach} {
			s := deployments.Subject{Tile: azAdmin, Deployment: "main", Protected: true, Record: true}
			if _, err := pl.Authorize(adminToken, op, s); err == nil {
				t.Errorf("%s: the xbin-granted tile's own terminal token passed %s onto its protected primary", pname, op)
			}
		}
	}

	for _, p := range runtime(azCRM)[:3] {
		if got := f.pl.Audience(p, deployments.Subject{Tile: azCRM, Record: true}); got != deployments.AudienceReader {
			t.Errorf("Audience(%s of crm) = %d, want the reader view", p.Via, got)
		}
	}
}
