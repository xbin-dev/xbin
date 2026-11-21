package server

// covers D252's second review — the strict reading's field lists stay in
// step with what they read, and what the SDK sends reads one way.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Every member POST /term/sessions takes is one a delegate's start may
// send: a field added to the create body is never refused for a person's
// agent by the strict reading (they share agentCreateBody — this keeps it
// so).
func TestADelegatedStartKnowsEveryCreateField(t *testing.T) {
	create, delegated := jsonFields(reflect.TypeOf(agentCreateBody{})), jsonFields(reflect.TypeOf(delegatedStart{}))
	if len(create) < 15 {
		t.Fatalf("the create body's fields: %v", create)
	}
	for name, typ := range create {
		if got, ok := delegated[name]; !ok || got != typ {
			t.Errorf("POST /term/sessions takes %q (%v); a delegated start reads it as %v (known: %v)", name, typ, got, ok)
		}
	}
	for _, name := range []string{"brief", "continue"} {
		if _, ok := delegated[name]; !ok {
			t.Errorf("a delegated start doesn't read %q", name)
		}
	}
}

// What the SDK's delegation helpers send — the request for an approval,
// a change set, a plan — reads one way: exact keys, nothing twice, nothing
// the route doesn't take. (bx makes no delegated call: a person's own
// client.)
func TestTheSDKsDelegatedBodiesReadOneWay(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	sent := map[string][]byte{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent[r.URL.Path] = b
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	ctx := context.Background()
	start, _ := json.Marshal(map[string]string{"cwd": "apps/x", "kind": "agent", "prompt": "Tidy README"})
	if _, err := xbin.RequestApprovalAs(ctx, "POST", "/api/xbin/term/sessions", "application/json", start, "tidy it"); err != nil {
		t.Fatal(err)
	}
	lines := []xbin.Change{{Kind: "grant.add", Args: map[string]string{"from": "apps/a", "target": "apps/b", "role": "reader"}, Why: "a needs b"}}
	if _, err := xbin.ProposeChanges(ctx, "wire", lines); err != nil {
		t.Fatal(err)
	}
	if _, err := xbin.ProposePlan(ctx, "plan", append(lines, xbin.Change{Kind: "agent.start", Args: map[string]string{"tile": "apps/b", "brief": "Goal: x"}, Why: "build"})); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for path, into := range map[string]any{"/api/xbin/delegate/requests": &requestNewBody{}, "/api/xbin/delegate/changesets": &changeSetBody{}, "/api/xbin/delegate/plans": &changeSetBody{}} {
		body, ok := sent[path]
		if !ok {
			t.Errorf("nothing was sent to %s", path)
			continue
		}
		if err := StrictDecode(body, into); err != nil {
			t.Errorf("%s: %s doesn't read one way: %v", path, body, err)
		}
		if cs, ok := into.(*changeSetBody); ok {
			for _, it := range cs.Items {
				if err := StrictDecode(it.Args, &map[string]string{}); err != nil {
					t.Errorf("%s: an item's args: %v", path, err)
				}
			}
		}
	}
	var in requestNewBody
	_ = json.Unmarshal(sent["/api/xbin/delegate/requests"], &in)
	if _, err := strictStart(mustBase64(t, in.BodyBase64)); err != nil {
		t.Errorf("the start the SDK carried: %v", err)
	}
}

func mustBase64(t *testing.T, s string) []byte {
	t.Helper()
	var b []byte
	if err := json.Unmarshal([]byte(`"`+s+`"`), &b); err != nil {
		t.Fatal(err)
	}
	return b
}
