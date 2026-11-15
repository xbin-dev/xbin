package xbin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The handle rides the context: set once for a conversation, sent on every
// AsPerson call; and the refusals a caller must tell apart are errors.Is-able.
func TestAgentSessionContextAndErrors(t *testing.T) {
	var seen []string
	tr := &delegateTransport{token: "tok", base: rtFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Header.Get("Authorization")+"|"+r.Header.Get(AgentSessionHeader))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
	})}
	ctx := InAgentSession(context.Background(), "s-1")
	for _, c := range []context.Context{ctx, context.Background()} {
		req, _ := http.NewRequestWithContext(c, "GET", "http://xbin/api/apps/x/a", nil)
		if _, err := tr.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0] != "XBin-Delegate tok|s-1" || seen[1] != "XBin-Delegate tok|" || AgentSessionID(ctx) != "s-1" {
		t.Fatalf("the handle on the wire: %q", seen)
	}
	resp := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
	}
	for want, r := range map[error]*http.Response{
		ErrNoSession:     resp(403, `{"error":"this call needs an agent session","refusal":"no-session"}`),
		ErrHiddenByScope: resp(403, `{"error":"hidden by you","refusal":"hidden"}`),
		ErrPolicyRefused: resp(403, `{"error":"Finance allows private models only","refusal":"tile"}`),
		ErrModelTaint:    resp(409, `{"code":"model_taint","orgs":["fin"],"reason":"Finance allows private models only","error":"start a new session"}`),
		ErrNeedsApproval: resp(403, `{"error":"needs approval","refusal":"needs-approval"}`),
		ErrAgentPaused:   resp(403, `{"error":"paused","refusal":"paused"}`),
		ErrAgentOff:      resp(403, `{"error":"off","refusal":"workspace-off"}`),
		ErrRateLimited:   resp(429, `{"error":"too fast","refusal":"rate-limited"}`),
	} {
		err := DelegateRefusal(r)
		if !errors.Is(err, want) {
			t.Errorf("%v: got %v", want, err)
		}
		for other := range map[error]bool{ErrNoSession: true, ErrHiddenByScope: true, ErrPolicyRefused: true, ErrModelTaint: true} {
			if other != want && errors.Is(err, other) {
				t.Errorf("%v also reads as %v", want, other)
			}
		}
		var de *DelegateError
		if want == ErrModelTaint && (!errors.As(err, &de) || len(de.Orgs) != 1 || de.Orgs[0] != "fin" || de.Reason == "") {
			t.Errorf("model_taint's orgs and reason: %+v", de)
		}
	}
	// a notice is xbind's delivery or it is nothing
	notice := func(from, user, agent string) (HolderNotice, error) {
		r := httptest.NewRequest("POST", "/agent/notices", strings.NewReader(`{"partition":"user:bob","events":[{"kind":"request","id":"r1"},{"kind":"state"}]}`))
		r.Header.Set("X-XBin-From", from)
		if user != "" {
			r.Header.Set("X-XBin-User", user)
		}
		if agent != "" {
			r.Header.Set("X-XBin-Agent", agent)
		}
		return ReadHolderNotice(r)
	}
	if n, err := notice("xbin/mail", "", ""); err != nil || n.Partition != "user:bob" || len(n.Events) != 2 || n.Events[0] != (HolderEvent{Kind: "request", ID: "r1"}) {
		t.Fatalf("a notice: %+v %v", n, err)
	}
	for _, c := range [][3]string{{"apps/x", "", ""}, {"user:bob", "bob", ""}, {"user:bob", "bob", "xbin/wsagent"}, {"xbin/mail", "bob", ""}} {
		if _, err := notice(c[0], c[1], c[2]); err == nil {
			t.Errorf("a notice from %v was taken", c)
		}
	}
}
