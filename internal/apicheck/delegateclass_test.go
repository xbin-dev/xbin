package apicheck

import (
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/server"
)

// TestDelegateRouteClasses is the guard of the delegation cap (D228;
// internal/server/delegateclass.go): every /api/xbin route xbind mounts has
// a row there — delegated, never, or refused, each decided — and every row
// names a route that exists. A new route fails here until someone decides
// whether a person's workspace agent may reach it; at run time a route
// without a row is refused all the same.
func TestDelegateRouteClasses(t *testing.T) {
	inv := inventory(t)
	table := server.DelegateClasses()
	var problems []string
	for p, where := range inv {
		if table[p] == server.DelegateUnlisted {
			problems = append(problems, where+" registers "+p+", which has no row in internal/server/delegateclass.go: "+
				"decide it — dNo(why) keeps a person's workspace agent out; anything else is a security decision (docs/auth.md §Delegation)")
		}
	}
	for p, c := range table {
		if _, ok := inv[p]; !ok {
			problems = append(problems, "internal/server/delegateclass.go classes "+p+" ("+c.String()+"), which no code registers: drop the row")
		}
		method, _, _ := strings.Cut(p, " ")
		switch c {
		case server.DelegateRead, server.DelegateAdminRead:
			if method != "GET" {
				problems = append(problems, p+" is classed "+c.String()+" and is no GET")
			}
		case server.DelegateAct:
			if method == "GET" {
				problems = append(problems, p+" is classed an act and is a GET")
			}
		}
		// what a person must do themselves is never the agent's
		if c.Allowed() && server.PartitionClassOf(p) == server.PersonOnly {
			problems = append(problems, p+" is a person's own act (partitionclass.go) and classed "+c.String())
		}
		if server.DelegateTicketed(p) && c != server.DelegateAct {
			problems = append(problems, p+" needs a ticket and is no act")
		}
	}
	for p := range server.DelegateUnconverted() {
		if !table[p].Allowed() {
			problems = append(problems, "delegateUnconverted lists "+p+", which isn't delegated")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	n := map[server.DelegateClass]int{}
	for _, c := range table {
		n[c]++
	}
	t.Logf("%d routes: %d read, %d act, %d admin-read, %d never, %d refused", len(table),
		n[server.DelegateRead], n[server.DelegateAct], n[server.DelegateAdminRead], n[server.DelegateNever], n[server.DelegateRefused])
}
