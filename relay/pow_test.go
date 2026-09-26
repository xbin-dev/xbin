package relay

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The shared vector (relay/README.md §Proof of work; internal/push checks
// its solver against the same numbers): the first decimal nonce that
// solves "xbin-pow-vector-1" at 8, 16 and 20 bits.
var powVector = []struct {
	bits  int
	nonce string
}{{8, "148"}, {16, "4813"}, {20, "268594"}}

func TestPoWVector(t *testing.T) {
	for _, v := range powVector {
		n, err := SolvePoW(context.Background(), "xbin-pow-vector-1", v.bits)
		if err != nil || n != v.nonce {
			t.Fatalf("%d bits: %q %v, want %q", v.bits, n, err, v.nonce)
		}
		if !PoWSolves("xbin-pow-vector-1", v.nonce, v.bits) || PoWSolves("xbin-pow-vector-1", v.nonce, v.bits+8) {
			t.Fatalf("%d bits: PoWSolves disagrees", v.bits)
		}
	}
}

func (r *rig) challenge() (string, int) {
	r.t.Helper()
	code, out, _ := r.call("GET", "/v1/workspaces/challenge", "", nil)
	if code != 200 {
		r.t.Fatalf("challenge: %d %v", code, out)
	}
	c, _ := out["challenge"].(string)
	return c, int(out["bits"].(float64))
}

func (r *rig) solved(c string, b int) map[string]any {
	r.t.Helper()
	n, err := SolvePoW(context.Background(), c, b)
	if err != nil {
		r.t.Fatal(err)
	}
	return map[string]any{"pow": map[string]string{"challenge": c, "nonce": n}}
}

func TestPoWOff(t *testing.T) {
	r := newRig(t, nil)
	if c, b := r.challenge(); c != "" || b != 0 {
		t.Fatalf("challenge without -registration-pow: %q %d", c, b)
	}
	r.workspace() // no proof needed
}

func TestPoWRegistration(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.RegistrationPoW = 10
		c.NewWorkspaceRate = Rate{PerHour: 3600, Burst: 100}
	})
	// none given: 401 with a challenge to solve
	code, out, _ := r.call("POST", "/v1/workspaces", "", nil)
	if code != 401 || out["code"] != ErrPoWRequired || out["challenge"] == "" || out["bits"] != float64(10) {
		t.Fatalf("no proof: %d %v", code, out)
	}
	fresh := out["challenge"].(string)
	// a challenge from the endpoint, solved
	c, b := r.challenge()
	if b != 10 || c == "" {
		t.Fatalf("challenge: %q %d", c, b)
	}
	body := r.solved(c, b)
	code, out, _ = r.call("POST", "/v1/workspaces", "", body)
	if code != 200 || out["key"] == "" {
		t.Fatalf("solved: %d %v", code, out)
	}
	// spent: the same proof again is refused, with a new challenge
	code, out, _ = r.call("POST", "/v1/workspaces", "", body)
	if code != 401 || out["code"] != ErrPoWInvalid || out["challenge"] == c {
		t.Fatalf("replay: %d %v", code, out)
	}
	// the challenge of a refusal works too
	if code, out, _ := r.call("POST", "/v1/workspaces", "", r.solved(fresh, 10)); code != 200 {
		t.Fatalf("the refusal's challenge: %d %v", code, out)
	}
	bad := func(name string, body any) {
		t.Helper()
		if code, out, _ := r.call("POST", "/v1/workspaces", "", body); code != 401 || out["code"] != ErrPoWInvalid {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	c2, _ := r.challenge()
	// a nonce that does not solve it (the first that fails at 10 bits)
	wrong := "0"
	for i := 0; PoWSolves(c2, wrong, 10); i++ {
		wrong = fmtInt(int64(i + 1))
	}
	bad("wrong nonce", map[string]any{"pow": map[string]string{"challenge": c2, "nonce": wrong}})
	bad("nonce charset", map[string]any{"pow": map[string]string{"challenge": c2, "nonce": "a b"}})
	bad("garbage", map[string]any{"pow": map[string]string{"challenge": "xbin-pow-vector-1", "nonce": "268594"}})
	// tampered: an easier difficulty in the challenge breaks its MAC
	raw := []byte(c2)
	raw[1] ^= 1
	bad("tampered", map[string]any{"pow": map[string]string{"challenge": string(raw), "nonce": "1"}})
	// another relay's challenge (another key)
	other := newRig(t, func(c *Config) { c.RegistrationPoW = 10 })
	oc, _ := other.challenge()
	bad("another relay's", r.solved(oc, 10))
	// expired
	late := r.solved(c2, 10)
	r.advance(powTTL + time.Second)
	bad("expired", late)
	// a harder relay refuses a challenge minted easier (a restart with a
	// new difficulty makes a new key anyway; this is the belt)
	r.relay.cfg.RegistrationPoW = 12
	c3, b3 := r.challenge()
	if b3 != 12 {
		t.Fatal(b3)
	}
	r.relay.cfg.RegistrationPoW = 10
	easy, _ := r.challenge()
	r.relay.cfg.RegistrationPoW = 12
	bad("too easy", r.solved(easy, 10))
	if code, out, _ := r.call("POST", "/v1/workspaces", "", r.solved(c3, 12)); code != 200 {
		t.Fatalf("12 bits: %d %v", code, out)
	}
}

// A refusal by the rate limits spends nothing: the same proof works once
// the limit lets it through.
func TestPoWNotSpentWhenLimited(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.RegistrationPoW = 8
		c.AllNewWorkspacesRate = Rate{PerHour: 3600, Burst: 1}
	})
	c, _ := r.challenge()
	if code, _, _ := r.call("POST", "/v1/workspaces", "", r.solved(c, 8)); code != 200 {
		t.Fatal(code)
	}
	c2, _ := r.challenge()
	body := r.solved(c2, 8)
	code, _, hdr := r.call("POST", "/v1/workspaces", "", body)
	if code != 429 || hdr.Get("Retry-After") == "" {
		t.Fatalf("global limit: %d", code)
	}
	r.advance(2 * time.Second)
	if code, out, _ := r.call("POST", "/v1/workspaces", "", body); code != 200 {
		t.Fatalf("after the limit: %d %v", code, out)
	}
}

// An operator's registration token skips the work; without one the relay
// closed to tokens still says so (not pow).
func TestPoWWithRegistrationTokens(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.RegistrationPoW = 30 // far too much to solve here: a token must skip it
		c.RegistrationTokens = []string{"op-token"}
	})
	if code, out, _ := r.call("POST", "/v1/workspaces", "op-token", nil); code != 200 || out["key"] == "" {
		t.Fatalf("token: %d %v", code, out)
	}
	if code, out, _ := r.call("POST", "/v1/workspaces", "", nil); code != http.StatusUnauthorized || out["code"] != ErrRegistration {
		t.Fatalf("no token: %d %v", code, out)
	}
}

func TestPoWConfigBounds(t *testing.T) {
	if _, err := New(Config{RegistrationPoW: 33}); err == nil {
		t.Fatal("33 bits accepted")
	}
}
