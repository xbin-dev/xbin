package sandbox

import (
	"math/rand/v2"
	"net/netip"
	"testing"
)

// Covers decides whether a running sandbox keeps its flows when its class
// changes (plans/tile-sandbox-runtime.md §4): true only for a superset.
func TestCovers(t *testing.T) {
	for _, c := range []struct {
		p, q []string
		want bool
	}{
		{nil, nil, true},
		{[]string{"net:internet"}, nil, true}, // anything covers none
		{nil, []string{"net:internet"}, false},
		{[]string{"net:internet"}, []string{"net:internet"}, true},
		{[]string{"net:internet"}, []string{"net:internet:443"}, true},
		{[]string{"net:internet:443"}, []string{"net:internet"}, false},
		{[]string{"net:internet:443"}, []string{"net:internet:80"}, false},
		{[]string{"net:internet"}, []string{"net:api.github.com:443", "net:*.example.com"}, true}, // pins are public-only
		{[]string{"net:internet"}, []string{"net:8.8.8.0/24", "net:1.1.1.1:53"}, true},
		{[]string{"net:internet"}, []string{"net:10.0.0.0/8"}, false},
		{[]string{"net:internet"}, []string{"net:8.0.0.0/5"}, false}, // holds 10/8
		{[]string{"net:*.github.com"}, []string{"net:api.github.com:443"}, true},
		{[]string{"net:*.github.com"}, []string{"net:github.com"}, true},
		{[]string{"net:*.github.com"}, []string{"net:*.api.github.com"}, true},
		{[]string{"net:api.github.com"}, []string{"net:*.github.com"}, false},
		{[]string{"net:api.github.com"}, []string{"net:internet"}, false},
		{[]string{"net:api.github.com:443"}, []string{"net:api.github.com"}, false},
		{[]string{"net:10.0.0.0/8"}, []string{"net:10.42.0.0/16", "net:10.1.2.3:22"}, true},
		{[]string{"net:10.42.0.0/16"}, []string{"net:10.0.0.0/8"}, false},
		{[]string{"net:10.0.0.0/8"}, []string{"net:internet"}, false},
		{[]string{"net:10.0.0.0/8"}, []string{"net:192.168.0.0/16"}, false},
		{[]string{"net:internet", "net:10.0.0.0/8"}, []string{"net:10.1.0.0/16", "net:internet:443"}, true},
		// a union of p's rules covering one q rule counts as narrowed: conservative
		{[]string{"net:10.0.0.0/9", "net:10.128.0.0/9"}, []string{"net:10.0.0.0/8"}, false},
		{[]string{"net:[2001:db8::]/32"}, []string{"net:[2001:db8:1::]/48"}, true},
		{[]string{"net:[2001:db8::]/32"}, []string{"net:10.0.0.0/8"}, false},
	} {
		p, err := Parse(c.p)
		if err != nil {
			t.Fatal(err)
		}
		q, err := Parse(c.q)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Covers(q); got != c.want {
			t.Errorf("%v covers %v = %v, want %v", c.p, c.q, got, c.want)
		}
	}
}

// Covers is sound: whenever it says p ⊇ q, every address/port q allows, p
// allows too — probed on random rule sets and addresses.
func TestCoversSound(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	pool := []string{"net:internet", "net:internet:443", "net:10.0.0.0/8", "net:10.42.0.0/16", "net:10.42.1.7:22",
		"net:192.168.0.0/16:5432", "net:8.8.8.0/24", "net:8.0.0.0/5", "net:1.1.1.1:53", "net:0.0.0.0/0", "net:127.0.0.1"}
	probes := []string{"10.1.2.3", "10.42.1.7", "192.168.3.4", "8.8.8.8", "9.9.9.9", "1.1.1.1", "127.0.0.1", "0.0.0.0", "172.16.0.1"}
	pick := func() EgressPolicy {
		var targets []string
		for i := rng.IntN(4); i > 0; i-- {
			targets = append(targets, pool[rng.IntN(len(pool))])
		}
		pol, err := Parse(targets)
		if err != nil {
			t.Fatal(err)
		}
		return pol
	}
	covered := 0
	for i := 0; i < 5000; i++ {
		p, q := pick(), pick()
		if !p.Covers(q) {
			continue
		}
		covered++
		for _, s := range probes {
			for _, port := range []int{22, 53, 80, 443, 5432} {
				if ip := netip.MustParseAddr(s); q.Allow(ip, port) && !p.Allow(ip, port) {
					t.Fatalf("%v covers %v, yet %s:%d is allowed only by the latter", p.Strings(), q.Strings(), s, port)
				}
			}
		}
	}
	if covered < 500 {
		t.Fatalf("only %d covered pairs probed", covered)
	}
}
