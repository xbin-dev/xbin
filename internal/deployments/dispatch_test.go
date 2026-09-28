package deployments

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// covers NP-14-3 NP-14-4 T9 — the operation registry: an operation
// registers its request type, how it reads its subject and its run; the
// dispatcher reads the subject, judges the caller (Authorize) and only then
// runs the operation with the grant. An unregistered op answers 501; a
// refusal, a subject's error or a run's *Error is answered as is, anything
// else as a 500; a body may refine the act judged only as refinements
// allows; registering a read, a refinement or an op twice panics.
func TestOpRegistryDispatch(t *testing.T) {
	var ran []Grant
	tbl := opTable{}
	addOp(tbl, OpDeploy, Handler[azReq]{
		Subject: func(p *Plane, r *azReq) (Op, Subject, error) {
			if r.Bad != "" {
				return "", Subject{}, &Error{Status: http.StatusBadRequest, Msg: r.Bad}
			}
			op := r.Judge
			if r.Restart {
				op = OpRestart
			}
			return op, Subject{Tile: r.Tile, Deployment: "main", Record: true, Seq: 5}, nil
		},
		Run: func(ctx context.Context, p *Plane, g Grant, r *azReq) (any, error) {
			ran = append(ran, g)
			switch r.Fail {
			case "queue":
				return nil, &Error{Status: http.StatusConflict, Kind: KindState, Msg: "deploy queue full"}
			case "disk":
				return nil, errors.New("disk on fire")
			}
			return "ran", nil
		},
	})
	pl := azPlane()
	ctx := context.Background()

	// Each request value is fresh, of the registered type.
	a, ok := tbl[OpDeploy]
	if !ok {
		t.Fatal("deploy not registered")
	}
	r1, r2 := a.newReq(), a.newReq()
	if _, ok := r1.(*azReq); !ok || r1 == r2 {
		t.Fatalf("newReq gave %T, %T", r1, r2)
	}

	var e *Error
	if _, err := tbl.do(ctx, pl, azDev, OpPromote, &azReq{}); !errors.As(err, &e) || e.Status != http.StatusNotImplemented {
		t.Errorf("an unregistered op: %v, want 501", err)
	}

	res, err := tbl.do(ctx, pl, azDev, OpDeploy, &azReq{Tile: "apps/crm"})
	if err != nil || res != "ran" {
		t.Fatalf("deploy by a terminal-level user: %v, %v", res, err)
	}
	res, err = tbl.do(ctx, pl, azDev, OpDeploy, &azReq{Tile: "apps/crm", Restart: true})
	if err != nil || res != "ran" {
		t.Fatalf("restart by a terminal-level user: %v, %v", res, err)
	}
	if len(ran) != 2 || ran[0].Op != OpDeploy || ran[1].Op != OpRestart || ran[0].Subject.Seq != 5 ||
		ran[0].Subject.Tile != "apps/crm" || ran[0].P.UserID != "dev" {
		t.Fatalf("the runs got %+v", ran)
	}

	ran = nil
	for _, c := range []struct {
		name   string
		req    any
		status int
	}{
		{"a reader", &azReq{Tile: "apps/crm"}, http.StatusForbidden},
		{"a subject's error", &azReq{Tile: "apps/crm", Bad: "bad request body: nope"}, http.StatusBadRequest},
		{"a refinement not allowed", &azReq{Tile: "apps/crm", Judge: OpState}, http.StatusInternalServerError},
		{"another refinement not allowed", &azReq{Tile: "apps/crm", Judge: OpUnprotect}, http.StatusInternalServerError},
		{"the wrong request type", &struct{ Tile string }{"apps/crm"}, http.StatusInternalServerError},
		{"a nil request", (*azReq)(nil), http.StatusInternalServerError},
	} {
		p := azDev
		if c.name == "a reader" {
			p = azReader
		}
		if _, err := tbl.do(ctx, pl, p, OpDeploy, c.req); !errors.As(err, &e) || e.Status != c.status {
			t.Errorf("%s: %v, want %d", c.name, err, c.status)
		}
	}
	if len(ran) != 0 {
		t.Errorf("refused requests ran: %+v", ran)
	}

	if _, err := tbl.do(ctx, pl, azDev, OpDeploy, &azReq{Tile: "apps/crm", Fail: "queue"}); !errors.As(err, &e) ||
		e.Status != http.StatusConflict || e.Msg != "deploy queue full" {
		t.Errorf("a run's *Error: %v", err)
	}
	if _, err := tbl.do(ctx, pl, azDev, OpDeploy, &azReq{Tile: "apps/crm", Fail: "disk"}); !errors.As(err, &e) ||
		e.Status != http.StatusInternalServerError || e.Msg != "disk on fire" {
		t.Errorf("a run's plain error: %v", err)
	}

	for _, c := range []struct {
		name string
		add  func()
	}{
		{"a read", func() { addOp(opTable{}, OpState, Handler[azReq]{Subject: azNopSubject, Run: azNopRun}) }},
		{"a refinement", func() { addOp(opTable{}, OpRestart, Handler[azReq]{Subject: azNopSubject, Run: azNopRun}) }},
		{"an unknown op", func() { addOp(opTable{}, Op("nope"), Handler[azReq]{Subject: azNopSubject, Run: azNopRun}) }},
		{"twice", func() { addOp(tbl, OpDeploy, Handler[azReq]{Subject: azNopSubject, Run: azNopRun}) }},
		{"without Run", func() { addOp(opTable{}, OpPause, Handler[azReq]{Subject: azNopSubject}) }},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registering %s didn't panic", c.name)
				}
			}()
			c.add()
		}()
	}

	// The plane's own registry: whatever is registered is an operation
	// route, and an op nobody built answers 501 through the exported calls.
	for op := range ops {
		if !acts[op].post {
			t.Errorf("%s is registered but is no operation route", op)
		}
	}
	if Registered(Op("nope")) {
		t.Error("Registered(nope)")
	}
	if _, ok := NewRequest(Op("nope")); ok {
		t.Error("NewRequest(nope)")
	}
	if _, err := pl.Do(ctx, azDev, Op("nope"), nil); !errors.As(err, &e) || e.Status != http.StatusNotImplemented ||
		e.Docs() != "/docs/protocol.md" {
		t.Errorf("Do(nope): %v, want 501", err)
	}
}

// azReq is a fake operation's request body.
type azReq struct {
	Tile    string `json:"tile"`
	Restart bool   `json:"restart"`
	Judge   Op     `json:"-"` // the act the subject names
	Bad     string `json:"-"` // the subject's error
	Fail    string `json:"-"` // how the run fails
}

func azNopSubject(*Plane, *azReq) (Op, Subject, error)             { return "", Subject{}, nil }
func azNopRun(context.Context, *Plane, Grant, *azReq) (any, error) { return nil, nil }

// covers NP-14-3 — the registry's keys are the routes: the operation Ops
// are exactly the POST routes internal/boot/deployments.go mounts under
// /deployments, so a handler dispatches a route by its path.
func TestOperationsNameTheRoutes(t *testing.T) {
	src, err := os.ReadFile("../boot/deployments.go")
	if err != nil {
		t.Fatal(err)
	}
	var routes, posts []string
	for _, m := range regexp.MustCompile(`RegisterAPI\("POST /deployments/([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, m[1])
	}
	for op, a := range acts {
		if a.post {
			posts = append(posts, string(op))
		}
	}
	sort.Strings(routes)
	sort.Strings(posts)
	if len(routes) == 0 || strings.Join(routes, " ") != strings.Join(posts, " ") {
		t.Errorf("POST routes %q, operation Ops %q", routes, posts)
	}
}
