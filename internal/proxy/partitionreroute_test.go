package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// covers D173 PD-18 (LAND) — a request to a person's partition follows its
// generation across a swap as one to a deployment does: when the
// generation the proxy routed it to was retired and cut it off before any
// answer, an idempotent request without a body goes again through the
// partition runner's EnsurePartition — the same partition, the same start
// class — to the generation the partition has now; the call's one hold on
// the partition spans both attempts. A POST, or a generation that wasn't
// retired (a crash), answers 502 without asking for another.
func TestPartitionProxyRerouteRetired(t *testing.T) {
	px, fp, d := partProxyWorld(t)
	next := answering(t, "alice-g2")
	alice := auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame"}
	*d = Decision{Deployment: "main", Role: "admin", Partition: "user:alice", CallerPartition: "user:alice", CallerPartitionID: alicePKey}
	for _, tc := range []struct {
		name   string
		method string
		gens   []fakeGen
		want   string // body, or "502 " and the JSON error
		asked  int
	}{
		{"a GET a swap cut off reaches the partition's new generation", "GET",
			[]fakeGen{{cutting(t), true}, {next, false}}, "alice-g2", 2},
		{"a failed dial of a retired generation sent nothing: a POST too", "POST",
			[]fakeGen{{gone(t), true}, {next, false}}, "alice-g2", 2},
		{"a POST may have reached the retired generation", "POST",
			[]fakeGen{{cutting(t), true}, {next, false}}, "502 {", 1},
		{"a crash is no retirement", "GET",
			[]fakeGen{{cutting(t), false}, {next, false}}, "502 {", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp.mu.Lock()
			fp.gens, fp.asked, fp.events = tc.gens, nil, nil
			fp.mu.Unlock()
			r := httptest.NewRequest(tc.method, "/api/apps/pg/x", nil)
			r = r.WithContext(auth.WithPrincipal(r.Context(), alice))
			rec := httptest.NewRecorder()
			px.ServeHTTP(rec, r)
			got := rec.Body.String()
			if rec.Code != http.StatusOK {
				got = fmt.Sprint(rec.Code) + " " + got
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("answer %q, want %q", got, tc.want)
			}
			fp.mu.Lock()
			defer fp.mu.Unlock()
			if len(fp.asked) != tc.asked {
				t.Errorf("EnsurePartition asked %d times (%v), want %d", len(fp.asked), fp.asked, tc.asked)
			}
			for _, a := range fp.asked {
				if a != "apps/pg main user:alice interactive" {
					t.Errorf("EnsurePartition asked %q: another partition or class", a)
				}
			}
			if evs := strings.Join(fp.events, " "); evs != "+active -active" || fp.holds["active"] != 0 {
				t.Errorf("holds %q (%v): want the call's one hold across the attempts", evs, fp.holds)
			}
		})
	}
}

// covers D173 PD-18 (D177) — a rerouted request whose partition is refused
// on its second admission (the caps met during the swap) answers as a
// first admission's refusal does: the same status (503), the refusal's
// reason as the error, the same headers (Retry-After, when the first sets
// one) — never the transport's 502. The call's one hold is released.
func TestPartitionProxyRerouteRefused(t *testing.T) {
	px, fp, d := partProxyWorld(t)
	alice := auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame"}
	*d = Decision{Deployment: "main", Role: "admin", Partition: "user:alice", CallerPartition: "user:alice", CallerPartitionID: alicePKey}
	refusal := sbx.Refuse(errors.New("too many people's instances of apps/pg are running; try again shortly"))
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/apps/pg/x", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), alice))
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, r)
		return rec
	}

	fp.err = refusal // the first admission refused
	first := call()
	fp.mu.Lock()
	fp.err, fp.asked, fp.events = nil, nil, nil
	fp.gens, fp.thenErr = []fakeGen{{cutting(t), true}}, refusal // the reroute's refused
	fp.mu.Unlock()
	again := call()

	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "too many people") {
		t.Fatalf("a first admission's refusal: %d %q", first.Code, first.Body.String())
	}
	if again.Code != first.Code || again.Body.String() != first.Body.String() {
		t.Errorf("the reroute's refusal: %d %q, want the first's %d %q", again.Code, again.Body.String(), first.Code, first.Body.String())
	}
	for _, h := range []string{"Content-Type", "Retry-After"} {
		if g, w := again.Header().Values(h), first.Header().Values(h); fmt.Sprint(g) != fmt.Sprint(w) {
			t.Errorf("the reroute's %s %q, the first's %q", h, g, w)
		}
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if asked := strings.Join(fp.asked, ","); asked != "apps/pg main user:alice interactive,apps/pg main user:alice interactive" {
		t.Errorf("EnsurePartition asked %s: want the same partition and class twice", asked)
	}
	if evs := strings.Join(fp.events, " "); evs != "+active -active" || fp.holds["active"] != 0 {
		t.Errorf("holds %q (%v): want the call's one hold, released", evs, fp.holds)
	}
}
