package push

import (
	"context"
	"strings"
	"testing"
)

// The relay's vector (relay/README.md §Proof of work, relay/pow_test.go):
// the first decimal nonce solving "xbin-pow-vector-1" at 8, 16, 20 bits.
func TestPoWVector(t *testing.T) {
	for b, want := range map[int]string{8: "148", 16: "4813", 20: "268594"} {
		n, err := solvePoW(context.Background(), "xbin-pow-vector-1", b)
		if err != nil || n != want {
			t.Fatalf("%d bits: %q %v, want %q", b, n, err, want)
		}
	}
	if _, err := solvePoW(context.Background(), "x", maxPoWBits+1); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("too much work: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := solvePoW(ctx, "x", 28); err == nil {
		t.Fatal("a cancelled solve went on")
	}
}

// An admin's opt-in solves the relay's proof of work: from the challenge
// endpoint, or — on a relay that only says so in its refusal — from the
// refusal's challenge; too much work is refused with a way out.
func TestRegisterWithPoW(t *testing.T) {
	r := adminRig(t)
	r.relay.set(func() { r.relay.pow = 12 })
	if code, out, _ := r.call(owner, "PUT", "/push/config", map[string]any{"relay": r.relay.srv.URL}); code != 200 || out["enabled"] != true {
		t.Fatalf("with a challenge endpoint: %d %v", code, out)
	}
	if r.relay.regs != 1 || len(r.relay.powSpent) != 1 {
		t.Fatalf("regs %d, proofs %d", r.relay.regs, len(r.relay.powSpent))
	}

	r2 := adminRig(t)
	r2.relay.set(func() { r2.relay.pow, r2.relay.noChallenge = 10, true })
	if code, out, _ := r2.call(owner, "PUT", "/push/config", map[string]any{"relay": r2.relay.srv.URL}); code != 200 {
		t.Fatalf("from the refusal: %d %v", code, out)
	}
	if r2.relay.regs != 1 {
		t.Fatalf("regs %d", r2.relay.regs)
	}

	r3 := adminRig(t)
	r3.relay.set(func() { r3.relay.pow = 30 })
	code, out, _ := r3.call(owner, "PUT", "/push/config", map[string]any{"relay": r3.relay.srv.URL})
	if code != 502 || !strings.Contains(out["error"].(string), "workspace key") || r3.relay.regs != 0 {
		t.Fatalf("too much work: %d %v", code, out)
	}
	// a relay without proof of work (and without the endpoint) as before
	r4 := adminRig(t)
	r4.relay.set(func() { r4.relay.noChallenge = true })
	if code, out, _ := r4.call(owner, "PUT", "/push/config", map[string]any{"relay": r4.relay.srv.URL}); code != 200 || r4.relay.regs != 1 {
		t.Fatalf("no proof of work: %d %v", code, out)
	}
}
