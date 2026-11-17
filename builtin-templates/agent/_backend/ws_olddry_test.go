package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// oldXbind answers POST /api/xbin/delegate/plans as an xbind from before
// plans had a dry run: the body read strictly ({title, items} and nothing
// else — a field it doesn't know is a 400), a plan proposed otherwise.
type oldXbind struct {
	mu       sync.Mutex
	proposed int
	deleted  int
	lenient  bool // reads past unknown fields (no xbind did; the client's other guard)
}

func (o *oldXbind) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/xbin/delegate/changesets/") {
		o.deleted++
		_, _ = w.Write([]byte(`{}`))
		return
	}
	var in struct {
		Title string `json:"title"`
		Items []struct {
			Kind string          `json:"kind"`
			Args json.RawMessage `json:"args"`
			Why  string          `json:"why"`
		} `json:"items"`
	}
	dec := json.NewDecoder(r.Body)
	if !o.lenient {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(&in); err != nil || len(in.Items) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"need {title, items: [{kind, args, why?}]} with at least one line","docs":"/docs/protocol/delegation.md"}`))
		return
	}
	o.proposed++
	_, _ = w.Write([]byte(`{"id":"cs1","state":"proposed","title":"t","items":[{"n":1,"line":"Create the tile"}]}`))
}

// The plan dry run on an xbind that has none: the old xbind refuses the
// field it doesn't know with a 400 — the client says "unavailable" (and
// the tool then words the dry run itself), where it used to pass the 400
// on as the plan's own fault. Nothing is proposed, nothing withdrawn. A
// 400 of an xbind that knows dry runs stays the plan's.
func TestWsPlanDryRunOnAnOldXbind(t *testing.T) {
	old := &oldXbind{}
	srv := httptest.NewServer(old)
	defer srv.Close()
	h := wsHTTP{via: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}}}}
	items := []xbin.Change{{Kind: "tile.create", Args: map[string]any{"path": "apps/dice"}, Why: "the tool"}}
	_, err := h.DescribePlan(context.Background(), "sid", "Dice", items)
	if !errors.Is(err, errWsUnavailable) {
		t.Fatalf("a dry run on an xbind without one: %v — want errWsUnavailable (the tool falls back to its own words)", err)
	}
	if old.proposed != 0 || old.deleted != 0 {
		t.Fatalf("the dry run proposed %d plans and withdrew %d", old.proposed, old.deleted)
	}
	// the real proposal still works there
	if cs, err := h.ProposePlan(context.Background(), "sid", "Dice", items); err != nil || cs.ID != "cs1" || old.proposed != 1 {
		t.Fatalf("proposing on the old xbind: %+v %v", cs, err)
	}
	// an xbind that read past the field would have proposed it: withdrawn, and unavailable
	old.lenient = true
	if _, err := h.DescribePlan(context.Background(), "sid", "Dice", items); !errors.Is(err, errWsUnavailable) || old.proposed != 2 || old.deleted != 1 {
		t.Fatalf("a dry run an xbind proposed: %v (proposed %d, withdrawn %d)", err, old.proposed, old.deleted)
	}
	// a newer xbind's 400 is about the plan, and stays an error
	for _, msg := range []string{"need {title, items: [{kind, args, why?}], dryRun?} with at least one line", "line 1: no such kind of change"} {
		if wsNoDryRun(&xbin.DelegateError{Status: 400, Message: msg}) {
			t.Errorf("%q read as an xbind without dry runs", msg)
		}
	}
}

// … and the tool on such an xbind: the dry run is said in the agent's own
// words (what proposing would do, nothing proposed), not as an error the
// model would read as its plan's fault — and the plan is then proposed.
func TestWsEvalPlanDryRunOnAnOldXbind(t *testing.T) {
	fx := newWsFix(t)
	fx.x.noDryRun = true
	s := fx.main(t)
	plan := func(dry bool) string {
		return fx.tool(t, s, "plan_propose", map[string]any{"title": "A place for notes", "dry_run": dry, "changes": []any{
			map[string]any{"kind": "tile.create", "args": map[string]any{"template": "starter", "name": "Notes"}, "why": "somewhere to keep them"}}})
	}
	if got := plan(true); !strings.HasPrefix(got, "This would propose 1 change(s)") || !strings.Contains(got, "Nothing was proposed.") || len(fx.x.sets) != 0 {
		t.Fatalf("the dry run on an xbind without one: %s (%d sets)", got, len(fx.x.sets))
	}
	if got := plan(false); !strings.Contains(got, "Proposed as one plan") || len(fx.x.sets) != 1 {
		t.Fatalf("proposing it: %s", got)
	}
}
