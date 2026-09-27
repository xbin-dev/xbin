package deployments

import (
	"net/http"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// codeWho is one principal of the code operations' matrix.
type codeWho struct {
	name  string
	group string // manager, term, token, person (below terminal level) or cred (a tile credential that never operates)
	p     auth.Principal
}

// codePrincipals are the matrix's columns (15-test-plan §3.7) for opSite:
// mia manages it (the fixture's manager gate), dev holds terminal level.
func codePrincipals() []codeWho {
	frame := userP("dev", opSite, users.LevelTerminal)
	frame.Component, frame.Via = opSite, "frame"
	return []codeWho{
		{"the owner token", "manager", ownerP},
		{"a workspace admin", "manager", azAda},
		{"the tile's manager", "manager", userP("mia", opSite, users.LevelTerminal)},
		{"a terminal-level user", "term", userP("dev", opSite, users.LevelTerminal)},
		{"a writer", "person", userP("wanda", opSite, users.LevelWrite)},
		{"a reader", "person", userP("rita", opSite, users.LevelRead)},
		{"an outsider", "person", userP("olly", opAPI, users.LevelTerminal)},
		{"the tile's terminal token", "token", terminalP("dev", opSite, users.LevelTerminal)},
		{"a manager's terminal token", "token", terminalP("mia", opSite, users.LevelTerminal)},
		{"an owner-driven terminal token", "token", auth.Principal{Component: opSite, Via: "terminal"}},
		{"a terminal token whose user holds write", "person", terminalP("wanda", opSite, users.LevelWrite)},
		{"the tile's instance token", "cred", auth.Principal{Component: opSite, Via: "instance"}},
		{"the tile's frame token", "cred", frame},
		{"another tile's terminal token", "cred", terminalP("dev", opAPI, users.LevelTerminal)},
		{"an xbin-granted tile's instance token", "cred", auth.Principal{Component: "apps/admin", Via: "instance"}},
	}
}

// matrixFx is a static tile with main following the work tree and two
// pinned deployments, dev and exp, added by the owner; protected pauses
// live reload and protects main. Its manager gate admits admins and mia.
func matrixFx(t *testing.T, protected bool) *codeFx {
	t.Helper()
	f := newCodeFx(t, false)
	f.p.MayManage = func(pr auth.Principal, tile string) bool {
		return pr.Component == "" && (pr.IsAdmin() || pr.UserID == "mia" && tile == opSite)
	}
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	f.write(opSite+"/index.html", "<h1>exp</h1>")
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "exp"})
	if protected {
		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		if _, err := f.p.idx.commit(opSite, -1, func(r *Record) error { r.ProtectedPrimary = true; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	f.took()
	return f
}

// covers P4 P11 P21 P26 T9 C4 — the authority matrix of the code operations
// through the dispatcher and the operations themselves, one subtest per
// cell: adding and removing a deployment, promoting onto a non-primary one
// and onto an unprotected primary (parity, P4), and attaching live reload
// pass managers, terminal-level people and the tile's terminal and agent
// tokens, and refuse the rest with a 403 naming who may act — people below
// terminal level, the tile's frame and instance tokens, every other tile's
// credentials — changing nothing. Onto a protected primary only a tile
// manager in a person's own session promotes, naming the reviewed checkpoint
// and seq (400 without them, before anything is captured), never a token;
// live reload never attaches to it (409 for everyone who may operate).
// Adding seeded data is a tile manager's act in a person's own session.
func TestDeployAuthzMatrixCode(t *testing.T) {
	type cell struct {
		name      string
		protected bool
		op        Op
		req       func(f *codeFx) any
		// want maps a group to a refusal (azOK, azTerminal, azCredential,
		// azProtected, azProtected409, azManager, azSession) or a status.
		want map[string]string
	}
	operators := map[string]string{"manager": azOK, "term": azOK, "token": azOK, "person": azTerminal, "cred": azCredential}
	seq := func(f *codeFx) *int64 { return ptr(f.rec(opSite).Seq) }
	cells := []cell{
		{"add", false, OpAdd, func(*codeFx) any { return &AddRequest{Tile: opSite, Deployment: "new"} }, operators},
		{"remove", false, OpRemove, func(*codeFx) any { return &RemoveRequest{Tile: opSite, Deployment: "exp", Confirm: ConfirmErase} }, operators},
		{"promote onto a non-primary deployment", false, OpPromote, func(*codeFx) any { return &PromoteRequest{Tile: opSite, From: "dev", To: "exp"} }, operators},
		{"promote onto the primary (parity)", false, OpPromote, func(*codeFx) any { return &PromoteRequest{Tile: opSite, From: "dev", To: "main"} }, operators},
		{"attach", false, OpAttach, func(*codeFx) any { return &AttachRequest{Tile: opSite, Deployment: "dev"} }, operators},
		{"add seeded data", false, OpAdd, func(*codeFx) any {
			return &AddRequest{Tile: opSite, Deployment: "new", Data: DataSeed, Confirm: ConfirmCopyData}
		}, map[string]string{"manager": "409", "term": azManager, "token": azSession, "person": azTerminal, "cred": azCredential}}, // a workspace-scope tile has no data of its own
		{"promote onto a protected primary, reviewed", true, OpPromote, func(f *codeFx) any {
			return &PromoteRequest{Tile: opSite, From: "dev", To: "main", Expect: "c:" + cp(f.rec(opSite), "dev")[:12], Seq: seq(f)}
		}, map[string]string{"manager": azOK, "term": azProtected, "token": azProtected, "person": azProtected, "cred": azCredential}},
		{"promote onto a protected primary, unreviewed", true, OpPromote, func(f *codeFx) any {
			return &PromoteRequest{Tile: opSite, From: "dev", To: "main", Seq: seq(f)}
		}, map[string]string{"manager": "400", "term": azProtected, "token": azProtected, "person": azProtected, "cred": azCredential}},
		{"attach to a protected primary", true, OpAttach, func(*codeFx) any { return &AttachRequest{Tile: opSite, Deployment: "main"} },
			map[string]string{"manager": azProtected409, "term": azProtected409, "token": azProtected409, "person": azTerminal, "cred": azCredential}},
	}
	for _, c := range cells {
		for _, w := range codePrincipals() {
			t.Run(c.name+"/"+w.name, func(t *testing.T) {
				f := matrixFx(t, c.protected)
				before := f.rec(opSite)
				_, err := f.do(w.p, c.op, c.req(f))
				want := c.want[w.group]
				switch want {
				case azOK:
					if err != nil {
						t.Fatalf("refused: %v", err)
					}
					if f.rec(opSite).Seq == before.Seq {
						t.Error("passed and changed nothing")
					}
					return
				case "400", "409", "501":
					if code, msg := errStatus(err); itoa(int64(code)) != want {
						t.Fatalf("%d %s, want %s", code, msg, want)
					}
				default:
					if bad := azCheckCode(err, want, opSite); bad != "" {
						t.Fatalf("want the %s refusal: %s", want, bad)
					}
				}
				if after := f.rec(opSite); after.Seq != before.Seq || len(f.took()) != 0 {
					t.Errorf("a refusal changed the record (seq %d → %d) or called the broker", before.Seq, after.Seq)
				}
			})
		}
	}
}

// covers P14 P28 T9 — joining a (scope, name) namespace that holds data
// (seeded, restored, partly either) gives the joining tile's writers reach
// into it: a tile manager's act in a person's own session (08-data §6.2),
// refused to everyone else with 11-contract §1.14's text naming who filled
// it; joining an empty one, or none, is anyone's who may add.
func TestJoinSeededDataManagerOnlyCode(t *testing.T) {
	f := matrixFx(t, false)
	for _, w := range codePrincipals() {
		for _, state := range []string{"", "empty", "seeded", "restored", "partial"} {
			var j *Joins
			if state != "" {
				j = &Joins{Scope: "apps/shop", State: state, By: "user:ana", At: "2026-09-27T10:00:00Z"}
			}
			err := f.p.joinGate(w.p, opSite, "dev", j)
			open := state == "" || state == "empty" || w.group == "manager"
			switch {
			case open && err != nil:
				t.Errorf("%s joining %q data: %v", w.name, state, err)
			case !open:
				code, msg := errStatus(err)
				if code != http.StatusForbidden || !strings.HasPrefix(msg, `apps/shop's "dev" data was `) ||
					!strings.HasSuffix(msg, " by user:ana 2026-09-27T10:00:00Z: joining it is a tile manager's act") {
					t.Errorf("%s joining %q data: %d %s", w.name, state, code, msg)
				}
			}
		}
	}
}

// azCheckCode is deploy_authz_test.go's refusal check for this package: the
// 403 (409 for protected409) of kind authority with the text reason names.
func azCheckCode(err error, reason, tile string) string {
	e, ok := err.(*Error)
	if !ok {
		return "not an *Error: " + errText(err)
	}
	protected := "the primary of " + tile + " (main) is protected: only tile managers change its code, and not from a terminal or agent session"
	status, good := http.StatusForbidden, false
	switch reason {
	case azTerminal:
		good = strings.HasSuffix(e.Msg, " needs terminal-level access on "+tile)
	case azCredential:
		good = strings.HasSuffix(e.Msg, " needs terminal-level access on "+tile+
			" — only people and the tile's own terminal and agent sessions operate its deployments")
	case azManager:
		good = strings.HasSuffix(e.Msg, " is a tile manager's act: the tile's owner, its org's admins, or a workspace admin")
	case azSession:
		good = strings.HasSuffix(e.Msg, " is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it")
	case azProtected:
		good = e.Msg == protected
	case azProtected409:
		status, good = http.StatusConflict, strings.HasPrefix(e.Msg, protected+" — ")
	}
	switch {
	case !good:
		return "message " + e.Msg
	case e.Status != status:
		return "status " + http.StatusText(e.Status)
	case e.Kind != KindAuthority:
		return "kind " + e.Kind
	}
	return ""
}

// The matrix's reasons, as deploy_authz_test.go (package deployments_test)
// spells them.
const (
	azOK           = ""
	azTerminal     = "terminal"
	azCredential   = "credential"
	azManager      = "manager"
	azSession      = "session"
	azProtected    = "protected"
	azProtected409 = "protected409"
)

// covers T9 C8 — a view-as session (D64) is refused every code operation
// with the view-as text, dry runs included, even where the viewed admin may
// act: through the dispatcher the operation never runs, the record stays as
// it was, and on a tile without a record nothing is created.
func TestViewAsRefusedEveryOpCode(t *testing.T) {
	viewAs := azAda
	viewAs.Impersonator = "owner"
	const readOnly = "read-only: you are viewing the workspace as another user — exit the view (top banner) to make changes"
	f := matrixFx(t, false)
	for _, c := range []struct {
		op  Op
		req any
	}{
		{OpAdd, &AddRequest{Tile: opSite, Deployment: "new"}},
		{OpAdd, &AddRequest{Tile: opSite, Deployment: "new", DryRun: true}},
		{OpAdd, &AddRequest{Tile: opSite, Deployment: "new", Attach: true}},
		{OpAdd, &AddRequest{Tile: opSite, Deployment: "new", Data: DataSeed, Confirm: ConfirmCopyData}},
		{OpRemove, &RemoveRequest{Tile: opSite, Deployment: "exp", Confirm: ConfirmErase}},
		{OpRemove, &RemoveRequest{Tile: opSite, Deployment: "exp", Confirm: ConfirmErase, DryRun: true}},
		{OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main"}},
		{OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "exp", DryRun: true}},
		{OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev"}},
		{OpAdd, &AddRequest{Tile: opAPI, Deployment: "new"}},
		{OpAdd, &AddRequest{Tile: opAPI, Deployment: "new", DryRun: true}},
	} {
		before := f.rec(opSite).Seq
		_, err := f.do(viewAs, c.op, c.req)
		if code, msg := errStatus(err); code != http.StatusForbidden || msg != readOnly {
			t.Errorf("%s %+v in a view: %d %s", c.op, c.req, code, msg)
		}
		if f.rec(opSite).Seq != before || len(f.took()) != 0 {
			t.Errorf("%s in a view changed something", c.op)
		}
		// The viewed admin herself may (control).
		if _, err := f.p.Authorize(azAda, c.op, f.p.subjectOf(opSite, "dev")); err != nil {
			t.Errorf("%s by the viewed admin: %v", c.op, err)
		}
	}
	if f.rec(opAPI) != nil || f.st.Exists(opAPI) {
		t.Error("a view's add made deployment state on a tile without a record")
	}
}
