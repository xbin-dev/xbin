package xbin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// AsPerson sends the partition's own token under the delegate scheme —
// Client keeps sending it as a Bearer — and the helpers speak the
// delegation API; every refusal xbind gives is an error a caller can tell
// apart.
func TestDelegateClient(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	type seen struct{ method, path, auth, ticket, model, dry, body string }
	var mu sync.Mutex
	var calls []seen
	refuse := func(w http.ResponseWriter, status int, refusal, msg string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "refusal": refusal, "class": "tile-write"})
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-XBin-Ticket"), r.Header.Get("X-XBin-Agent-Model"), r.Header.Get("X-XBin-Dry-Run"), string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Header.Get("X-XBin-Dry-Run") != "":
			_, _ = w.Write([]byte(`{"dryRun":true,"allowed":true,"class":"tile-write","needsApproval":true,"tile":"apps/mail"}`))
		case r.URL.Path == "/api/apps/mail/send" && r.Header.Get("X-XBin-Ticket") == "t1":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/api/apps/mail/send":
			refuse(w, 403, "needs-approval", "a write to apps/mail needs its person's approval")
		case r.URL.Path == "/api/apps/fin/x":
			refuse(w, 403, "tile", "Finance allows private models only")
		case r.URL.Path == "/api/xbin/grants":
			refuse(w, 403, "not-delegated", "the admin API is never delegated")
		case r.URL.Path == "/api/apps/paused/x":
			refuse(w, 403, "paused", "your workspace agent is off: you paused it")
		case r.URL.Path == "/api/apps/off/x":
			refuse(w, 403, "workspace-off", "an admin hasn't turned workspace agents on")
		case r.URL.Path == "/api/apps/fast/x":
			w.Header().Set("Retry-After", "7")
			refuse(w, 429, "rate-limited", "your agent is acting too fast")
		case r.URL.Path == "/api/apps/used/x":
			refuse(w, 403, "ticket", "the ticket was approved for another call")
		case r.URL.Path == "/api/apps/own/x":
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"error":"the tile's own refusal"}`))
		case r.Method == "POST" && r.URL.Path == "/api/xbin/delegate/requests":
			_, _ = w.Write([]byte(`{"id":"t1","kind":"call","method":"POST","target":"/api/apps/mail/send","tile":"apps/mail","state":"pending","preview":{"role":"writer"}}`))
		case r.Method == "GET" && r.URL.Path == "/api/xbin/delegate/requests/t1":
			_, _ = w.Write([]byte(`{"id":"t1","state":"approved","grant":"once"}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/xbin/delegate/requests/t1":
			_, _ = w.Write([]byte(`{"id":"t1","state":"withdrawn"}`))
		case r.URL.Path == "/api/xbin/delegate/approvals":
			_, _ = w.Write([]byte(`{"approvals":[{"id":"a1","tile":"apps/mail","scope":"call","method":"POST","path":"/api/apps/mail/send"},{"id":"a2","tile":"apps/x","scope":"tile"},{"id":"a3","tile":"apps/old"}]}`))
		case r.Method == "POST" && r.URL.Path == "/api/xbin/delegate/changesets":
			_, _ = w.Write([]byte(`{"id":"c1","title":"t","state":"proposed","items":[{"n":1,"kind":"grant.add","line":"Let apps/a use apps/b as reader","blocked":""}]}`))
		case r.URL.Path == "/api/xbin/delegate/changesets/c1":
			_, _ = w.Write([]byte(`{"id":"c1","state":"applied","items":[{"n":1,"kind":"grant.add","result":"applied"}]}`))
		default:
			w.WriteHeader(401)
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	clientOnce, personOnce = sync.Once{}, sync.Once{}
	defer func() { clientOnce, personOnce = sync.Once{}, sync.Once{} }()
	ctx := context.Background()
	do := func(method, path, body string, opts ...func(*http.Request)) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, method, "http://xbin"+path, strings.NewReader(body))
		for _, o := range opts {
			o(req)
		}
		return AsPerson().Do(req)
	}
	refusal := func(path string) error {
		resp, err := do("POST", path, "{}")
		if err != nil {
			t.Fatal(err)
		}
		return DelegateRefusal(resp)
	}

	// each refusal is its own error
	for path, want := range map[string]error{
		"/api/apps/mail/send": ErrNeedsApproval, "/api/apps/fin/x": ErrPolicyRefused, "/api/xbin/grants": ErrNotDelegated,
		"/api/apps/paused/x": ErrAgentPaused, "/api/apps/off/x": ErrAgentOff, "/api/apps/fast/x": ErrRateLimited, "/api/apps/used/x": ErrBadTicket,
	} {
		err := refusal(path)
		if !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", path, err, want)
		}
		for _, other := range []error{ErrNeedsApproval, ErrPolicyRefused, ErrNotDelegated, ErrAgentPaused, ErrAgentOff, ErrRateLimited, ErrBadTicket, ErrAdminRead} {
			if other != want && errors.Is(err, other) {
				t.Errorf("%s: %v is also %v", path, err, other)
			}
		}
	}
	var de *DelegateError
	if err := refusal("/api/apps/fin/x"); !errors.As(err, &de) || de.Message != "Finance allows private models only" || de.Status != 403 || de.Class != "tile-write" {
		t.Errorf("the policy's reason: %+v", de)
	}
	if err := refusal("/api/apps/fast/x"); !errors.As(err, &de) || de.RetryAfter != 7*time.Second {
		t.Errorf("retry after: %+v", de)
	}
	// a tile's own refusal is an error of no delegation kind
	if err := refusal("/api/apps/own/x"); !errors.As(err, &de) || de.Refusal != "" || de.Status != 422 || de.Message != "the tile's own refusal" || errors.Is(err, ErrNotDelegated) {
		t.Errorf("a tile's own refusal: %v", err)
	}

	// ask, poll, send with the ticket
	body := []byte(`{"to":"ada"}`)
	ask, err := RequestApproval(ctx, "POST", "/api/apps/mail/send", body, "send it to Ada")
	if err != nil || ask.ID != "t1" || ask.Approved() || ask.Decided() || ask.Preview["role"] != "writer" {
		t.Fatalf("RequestApproval: %+v %v", ask, err)
	}
	if got, err := ApprovalOf(ctx, ask.ID); err != nil || !got.Approved() || got.Decided() {
		t.Fatalf("ApprovalOf: %+v %v", got, err)
	}
	resp, err := do("POST", "/api/apps/mail/send", string(body), func(r *http.Request) { WithTicket(r, ask.ID); WithAgentModel(r, "m-1") })
	if err != nil || DelegateRefusal(resp) != nil {
		t.Fatalf("the ticketed call: %v", err)
	}
	resp.Body.Close()
	if out, err := DryRun(ctx, "POST", "/api/apps/mail/send?x=1"); err != nil || out["needsApproval"] != true || out["tile"] != "apps/mail" {
		t.Fatalf("DryRun: %v %v", out, err)
	}
	if err := WithdrawApproval(ctx, ask.ID); err != nil {
		t.Fatal(err)
	}
	aps, err := StandingApprovals(ctx)
	if err != nil || len(aps) != 3 || aps[0].Tile != "apps/mail" || !aps[0].Until.IsZero() {
		t.Fatalf("StandingApprovals: %+v %v", aps, err)
	}
	// an approval is of one call unless it says the whole tile; one without a scope covers nothing
	for i, c := range []struct {
		method, path string
		want         bool
	}{{"POST", "/api/apps/mail/send", true}, {"post", "/api/apps/mail/send", true}, {"DELETE", "/api/apps/mail/send", false}, {"POST", "/api/apps/mail/drafts", false}} {
		if aps[0].Covers(c.method, c.path) != c.want {
			t.Errorf("case %d: a call approval covers %s %s = %v", i, c.method, c.path, !c.want)
		}
	}
	if !aps[1].Covers("DELETE", "/api/apps/x/a/b") || aps[1].Covers("POST", "/api/apps/xy/a") || aps[2].Covers("POST", "/api/apps/old/a") {
		t.Errorf("tile and unscoped approvals: %+v", aps)
	}
	cs, err := ProposeChanges(ctx, "t", []Change{{Kind: "grant.add", Args: map[string]string{"from": "apps/a", "target": "apps/b", "role": "reader"}, Why: "so a can read b"}})
	if err != nil || cs.ID != "c1" || cs.State != "proposed" || cs.Items[0].Line == "" {
		t.Fatalf("ProposeChanges: %+v %v", cs, err)
	}
	if got, err := ChangeSetOf(ctx, cs.ID); err != nil || got.State != "applied" || got.Items[0].Result != "applied" {
		t.Fatalf("ChangeSetOf: %+v %v", got, err)
	}

	// the component's own client still sends a Bearer
	if r, err := Client().Get("http://xbin/api/xbin/whoami"); err == nil {
		r.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	last := calls[len(calls)-1]
	if last.auth != "Bearer tok" {
		t.Errorf("Client() sent %q", last.auth)
	}
	var sawTicket, sawAsk, sawDry bool
	for _, c := range calls[:len(calls)-1] {
		if c.auth != "XBin-Delegate tok" {
			t.Errorf("%s %s sent Authorization %q", c.method, c.path, c.auth)
		}
		switch {
		case c.ticket == "t1" && c.model == "m-1" && c.body == string(body):
			sawTicket = true
		case c.method == "POST" && c.path == "/api/xbin/delegate/requests":
			var in map[string]string
			_ = json.Unmarshal([]byte(c.body), &in)
			dec, _ := base64.StdEncoding.DecodeString(in["bodyBase64"])
			sawAsk = in["method"] == "POST" && in["path"] == "/api/apps/mail/send" && string(dec) == string(body) && in["summary"] == "send it to Ada"
		case c.dry == "1" && c.path == "/api/apps/mail/send?x=1":
			sawDry = true
		}
	}
	if !sawTicket || !sawAsk || !sawDry {
		t.Errorf("ticket %v, ask %v, dry run %v", sawTicket, sawAsk, sawDry)
	}
}
