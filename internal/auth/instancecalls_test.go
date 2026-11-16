package auth

import (
	"net/http/httptest"
	"testing"
)

// Calls are tracked per instance token, released once, and nothing is
// tracked for a request that carries no instance token.
func TestInstanceCalls(t *testing.T) {
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	a.RegisterInstance("tok-1", "apps/x")
	call := func(tok string) func() {
		r := httptest.NewRequest("PUT", "/api/xbin/cron/jobs", nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		return a.TrackInstanceCall(r, "PUT /api/xbin/cron/jobs")
	}
	r1, r2 := call("tok-1"), call("tok-1")
	call("")()                  // no credential
	call("not-an-instance")()   // not an instance token
	call(a.OwnerTokenValue())() // the owner's
	if got := a.InstanceCalls("tok-1"); len(got) != 2 || got[0].Route != "PUT /api/xbin/cron/jobs" || got[1].Since.Before(got[0].Since) {
		t.Fatalf("two calls in flight: %+v", got)
	}
	r1()
	r1() // once
	if got := a.InstanceCalls("tok-1"); len(got) != 1 {
		t.Fatalf("one released: %+v", got)
	}
	r2()
	if got := a.InstanceCalls("tok-1"); len(got) != 0 || len(a.calls.calls) != 0 {
		t.Fatalf("all released: %+v %v", got, a.calls.calls)
	}
}
