package sandboxcontract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The suite runs against the reference manager in the xbin repository
// (hack/fakesandbox: TestContract); here, how a Caller says who is asking.
func TestCallerHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "from": r.Header.Get("X-XBin-From"),
			"user": r.Header.Get("X-XBin-User"), "asserted": r.Header.Get("Sbx-User"), "hooked": r.Header.Get("X-Hooked")})
	}))
	defer srv.Close()
	var got map[string]string
	c := Target{URL: srv.URL + "/"}.As(t, "apps/a").Verified("alice").Asserting("bob")
	c.Call("GET", "/hello", nil, 200, &got)
	if got["path"] != "/sbx/hello" || got["from"] != "apps/a" || got["user"] != "alice" || got["asserted"] != "bob" {
		t.Fatalf("default headers: %v", got)
	}
	hooked := Target{URL: srv.URL,
		Consumer: func(r *http.Request, c string) { r.Header.Set("X-Hooked", "from="+c) },
		Verified: func(r *http.Request, u string) { r.Header.Add("X-Hooked", "user="+u) },
		Asserted: func(r *http.Request, u string) { r.Header.Add("X-Hooked", "asserted="+u) },
	}
	resp, b, err := hooked.As(t, "apps/b").Verified("carol").Asserting("dan").Do(context.Background(), "GET", "/x", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	_ = json.Unmarshal(b, &got)
	if got["from"] != "" || got["user"] != "" || got["hooked"] != "from=apps/b" {
		t.Fatalf("hooks: %v", got)
	}
	if v := resp.Request.Header.Values("X-Hooked"); len(v) != 3 || v[1] != "user=carol" || v[2] != "asserted=dan" {
		t.Fatalf("hooks: %v", v)
	}
}
