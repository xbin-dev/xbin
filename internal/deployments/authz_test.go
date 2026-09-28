package deployments

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// Principals built without a users store: the gates fall back to User.
var (
	azAda    = auth.Principal{UserID: "ada", User: &users.User{ID: "ada", Role: users.RoleAdmin}, Via: "session"}
	azMia    = auth.Principal{UserID: "mia", User: &users.User{ID: "mia", Role: users.RoleUser, Tiles: map[string]string{"apps/crm": users.LevelTerminal}}, Via: "session"}
	azDev    = auth.Principal{UserID: "dev", User: &users.User{ID: "dev", Role: users.RoleUser, Tiles: map[string]string{"apps/crm": users.LevelTerminal}}, Via: "session"}
	azReader = auth.Principal{UserID: "rita", User: &users.User{ID: "rita", Role: users.RoleUser, Tiles: map[string]string{"apps/crm": users.LevelRead}}, Via: "session"}
)

// azPlane's manager gate admits azMia (who manages apps/crm) and admins,
// in their own session, as Broker.MayManageDeployments does.
func azPlane() *Plane {
	return &Plane{MayManage: func(p auth.Principal, tile string) bool {
		return p.Component == "" && (p.IsAdmin() || (p.UserID == "mia" && tile == "apps/crm"))
	}}
}

func azPostActs() []Op {
	var out []Op
	for _, op := range MutatingActs() {
		if acts[op].post {
			out = append(out, op)
		}
	}
	return out
}

// covers T9 C8 — a view-as session (D64) is refused every operation with
// the view-as text, even where the viewed user may do it, by Authorize (the
// second layer behind the middleware) and so by the dispatcher, which never
// runs the operation; its reads answer as the viewed user, and Can reports
// the viewed user's answer, which the UI shows beside the read-only flag.
func TestViewAsRefusedEveryOp(t *testing.T) {
	pl := azPlane()
	viewAs := azAda
	viewAs.Impersonator = "owner"
	const readOnly = "read-only: you are viewing the workspace as another user — exit the view (top banner) to make changes"

	for _, op := range MutatingActs() {
		for _, s := range []Subject{
			{Tile: "apps/crm", Deployment: "main", Record: true, Seq: 2},
			{Tile: "apps/crm", Deployment: "main", Protected: true, Record: true, Seq: 2},
			{Tile: "apps/crm", Deployment: "dev", Record: true, Seq: 2},
			{Tile: "apps/crm"},
		} {
			_, err := pl.Authorize(viewAs, op, s)
			var e *Error
			if !errors.As(err, &e) || e.Status != http.StatusForbidden || e.Kind != KindAuthority ||
				e.Msg != readOnly || e.Docs() != "/docs/auth.md" {
				t.Errorf("%s on %+v in a view: %v, want the read-only 403", op, s, err)
			}
			if pl.Can(viewAs, op, s) != pl.Can(azAda, op, s) {
				t.Errorf("%s on %+v: Can in a view %+v, the viewed user's %+v", op, s, pl.Can(viewAs, op, s), pl.Can(azAda, op, s))
			}
		}
		// Control: the viewed user herself may.
		if _, err := pl.Authorize(azAda, op, Subject{Tile: "apps/crm", Deployment: "dev", Record: true}); err != nil {
			t.Errorf("%s by the viewed admin: %v", op, err)
		}
	}
	for _, op := range ReadActs() {
		if _, err := pl.Authorize(viewAs, op, Subject{Tile: "apps/crm", Record: true}); err != nil {
			t.Errorf("read %s in a view: %v", op, err)
		}
	}
	// One sentence whichever layer refuses: the middleware's own.
	if src, err := os.ReadFile("../server/impersonate.go"); err != nil || !strings.Contains(string(src), strconv.Quote(readOnly)) {
		t.Errorf("internal/server/impersonate.go no longer says %q (%v)", readOnly, err)
	}

	// Through the dispatcher: refused before the operation runs.
	type req struct{ Tile string }
	tbl := opTable{}
	for _, op := range azPostActs() {
		addOp(tbl, op, Handler[req]{
			Subject: func(p *Plane, r *req) (Op, Subject, error) {
				return "", Subject{Tile: r.Tile, Deployment: "dev", Record: true}, nil
			},
			Run: func(context.Context, *Plane, Grant, *req) (any, error) {
				t.Errorf("%s ran in a view", op)
				return nil, nil
			},
		})
	}
	for _, op := range azPostActs() {
		_, err := tbl.do(context.Background(), pl, viewAs, op, &req{Tile: "apps/crm"})
		if err == nil || !strings.HasPrefix(err.Error(), "read-only: ") {
			t.Errorf("dispatching %s in a view: %v", op, err)
		}
	}
}

// covers T9 D127m — the ops call Recheck at commit (06-security T9.4): it
// judges the grant again against the record as it stands. A code move onto
// the primary authorized for a terminal-level user is refused once
// protection was turned on meanwhile, while a manager's commits; a record
// removed meanwhile refuses a non-opt-in with the zero-state 409, while the
// opt-in's commit, before its record exists, passes; a grant rechecked
// against another subject never passes. The grant carries the seq it was
// judged at.
func TestRecheckJudgesTheRecordAtCommit(t *testing.T) {
	pl := azPlane()
	at := Subject{Tile: "apps/crm", Deployment: "main", Record: true, Seq: 7}

	g, err := pl.Authorize(azDev, OpDeploy, at)
	if err != nil {
		t.Fatal(err)
	}
	if g.Subject.Seq != 7 || g.Op != OpDeploy || g.P.UserID != "dev" {
		t.Fatalf("grant %+v", g)
	}
	if err := pl.Recheck(g, at); err != nil {
		t.Errorf("unchanged record: %v", err)
	}
	protected := at
	protected.Protected, protected.Seq = true, 8
	var e *Error
	if err := pl.Recheck(g, protected); !errors.As(err, &e) || e.Status != http.StatusForbidden ||
		!strings.HasPrefix(e.Msg, "the primary of apps/crm (main) is protected") {
		t.Errorf("protection turned on before the commit: %v, want the protected 403", err)
	}
	gm, err := pl.Authorize(azMia, OpDeploy, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := pl.Recheck(gm, protected); err != nil {
		t.Errorf("a manager's deploy after protection: %v", err)
	}

	// The primary reassigned meanwhile: main is no longer the protected one.
	reassigned := protected
	reassigned.Primary = "dev"
	if err := pl.Recheck(g, reassigned); err != nil {
		t.Errorf("main after the primary moved to dev: %v", err)
	}

	gone := Subject{Tile: "apps/crm", Deployment: "main"}
	if err := pl.Recheck(g, gone); !errors.As(err, &e) || e.Status != http.StatusConflict || e.Kind != KindState ||
		e.Msg != "apps/crm has no deployments yet: pause live reload or add a deployment first" {
		t.Errorf("record removed before the commit: %v, want the zero-state 409", err)
	}
	gp, err := pl.Authorize(azDev, OpPause, gone)
	if err != nil {
		t.Fatal(err)
	}
	if err := pl.Recheck(gp, gone); err != nil {
		t.Errorf("the opt-in's commit before its record exists: %v", err)
	}

	for _, now := range []Subject{
		{Tile: "apps/other", Deployment: "main", Record: true, Seq: 7},
		{Tile: "apps/crm", Deployment: "dev", Record: true, Seq: 7},
	} {
		if err := pl.Recheck(g, now); !errors.As(err, &e) || e.Status != http.StatusInternalServerError {
			t.Errorf("recheck against %+v: %v, want a 500", now, err)
		}
	}
	if err := pl.Recheck(Grant{}, at); err == nil {
		t.Error("a zero grant passed the recheck")
	}
	if _, err := pl.Authorize(azAda, OpDeploy, Subject{Deployment: "main", Record: true}); !errors.As(err, &e) ||
		e.Status != http.StatusInternalServerError {
		t.Errorf("a subject without a tile: %v, want a 500", err)
	}
}

// covers NP-14-5 D119c D119e PO-15 — the ship-dark switch (14 §6.3), read as
// Plane.OptInClosed: while off, every operation that creates or extends
// deployment state answers 409 with kind policy, a zero-state opt-in
// included, after authority is judged; resuming live reload onto main,
// removing a deployment, resetting its data and unprotecting stay allowed,
// and so do a restart and running a job now, which create nothing; reads
// never read the switch. Allowed reports it for the tile. A Plane{} literal
// is open.
func TestShipDarkClosesOnlyGrowth(t *testing.T) {
	open, shut := azPlane(), azPlane()
	shut.OptInClosed = true
	stays := map[Op]bool{OpRemove: true, OpReset: true, OpUnprotect: true, OpRestart: true, OpRunNow: true, OpPurge: true,
		OpBranchClear: true}

	for _, op := range MutatingActs() {
		s := Subject{Tile: "apps/crm", Deployment: "dev", Record: true, Seq: 1}
		if _, err := open.Authorize(azAda, op, s); err != nil {
			t.Errorf("%s with the switch on: %v", op, err)
		}
		if got := open.Allowed(op, s); !got.OK {
			t.Errorf("Allowed(%s) with the switch on: %+v", op, got)
		}
		_, err := shut.Authorize(azAda, op, s)
		allowed := shut.Allowed(op, s)
		if stays[op] {
			if err != nil || !allowed.OK {
				t.Errorf("%s with the switch off: %v / %+v, want allowed", op, err, allowed)
			}
			continue
		}
		var e *Error
		if !errors.As(err, &e) || e.Status != http.StatusConflict || e.Kind != KindPolicy ||
			!strings.Contains(e.Msg, " is turned off on this xbind (--tile-deployments=off)") {
			t.Errorf("%s with the switch off: %v, want the policy 409", op, err)
		}
		if allowed.OK || allowed.Kind != KindPolicy || err == nil || allowed.Why != err.Error() {
			t.Errorf("Allowed(%s) with the switch off: %+v", op, allowed)
		}
	}

	// Resuming onto main returns a tile toward the zero state; onto another
	// deployment it doesn't.
	if _, err := shut.Authorize(azAda, OpResume, Subject{Tile: "apps/crm", Deployment: "main", Record: true}); err != nil {
		t.Errorf("resuming onto main with the switch off: %v", err)
	}
	if _, err := shut.Authorize(azAda, OpResume, Subject{Tile: "apps/crm", Deployment: "dev", Record: true}); err == nil {
		t.Error("resuming onto dev with the switch off passed")
	}
	// The opt-ins on a zero-state tile: pausing is closed; unprotecting, a
	// no-op there, isn't.
	zero := Subject{Tile: "apps/crm", Deployment: "main"}
	if _, err := shut.Authorize(azDev, OpPause, zero); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Errorf("opting in with the switch off: %v", err)
	}
	if _, err := open.Authorize(azDev, OpPause, zero); err != nil {
		t.Errorf("opting in with the switch on: %v", err)
	}
	// Authority first: a reader learns it can't act, not that the switch is off.
	var e *Error
	if _, err := shut.Authorize(azReader, OpPause, zero); !errors.As(err, &e) || e.Kind != KindAuthority {
		t.Errorf("a reader pausing with the switch off: %v, want the authority refusal", err)
	}
	for _, op := range ReadActs() {
		if _, err := shut.Authorize(azAda, op, Subject{Tile: "apps/crm", Record: true}); err != nil {
			t.Errorf("read %s with the switch off: %v", op, err)
		}
	}
	if (&Plane{}).OptInClosed {
		t.Error("a Plane{} literal is closed")
	}
}

// covers D119c T9 — on a tile without a record only the opt-ins are accepted
// (11-contract §1.2): pausing live reload, adding a deployment, protecting
// (and unprotecting, a no-op there); every other operation answers 409 with
// kind state, after authority is judged, and a read is never refused for it.
func TestZeroStateAcceptsOnlyOptIns(t *testing.T) {
	pl := azPlane()
	// purging a checkpoint too: the store outlives an opt-out (05-model §2)
	optIn := map[Op]bool{OpPause: true, OpAdd: true, OpProtect: true, OpUnprotect: true, OpPurge: true}
	zero := Subject{Tile: "apps/crm", Deployment: "main"}
	for _, op := range MutatingActs() {
		_, err := pl.Authorize(azAda, op, zero)
		if optIn[op] {
			if err != nil {
				t.Errorf("opt-in %s on a zero-state tile: %v", op, err)
			}
			continue
		}
		var e *Error
		if !errors.As(err, &e) || e.Status != http.StatusConflict || e.Kind != KindState {
			t.Errorf("%s on a zero-state tile: %v, want the zero-state 409", op, err)
		}
		if c := pl.Can(azAda, op, zero); c.OK || c.Kind != KindState {
			t.Errorf("Can(%s) on a zero-state tile: %+v", op, c)
		}
		if _, err := pl.Authorize(azReader, op, zero); !errors.As(err, &e) || e.Kind != KindAuthority {
			t.Errorf("%s by a reader on a zero-state tile: %v, want the authority refusal first", op, err)
		}
	}
	for _, op := range ReadActs() {
		if _, err := pl.Authorize(azAda, op, zero); err != nil {
			t.Errorf("read %s on a zero-state tile: %v", op, err)
		}
	}
}
