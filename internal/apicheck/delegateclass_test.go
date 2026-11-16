package apicheck

import (
	"os"
	"path/filepath"
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

	// The CLASS of every route is pinned, not only its presence: moving a
	// route into the cap — "never" to "read", an act that stops needing a
	// ticket — is a security decision, and this file is where a reviewer
	// sees it as a diff of its own.
	var lines []string
	for p, c := range table {
		line := p + "\t" + c.String()
		if server.DelegateTicketed(p) {
			line += "\tticket"
		}
		if _, un := server.DelegateUnconverted()[p]; un {
			line += "\tunconverted: refused until its listing is filtered"
		}
		// …and what the gate does for a route beside classing it: whether a
		// read leaves a row in the person's log, and which function names
		// the tile whose org rules are asked (none: no tile's rules are)
		audit, tileBy := server.DelegateRowFacts(p)
		if audit {
			line += "\taudit"
		}
		if tileBy != "" {
			line += "\ttile:" + tileBy
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	want := "# The delegation cap, route by route (internal/server/delegateclass.go): what a person's\n" +
		"# workspace agent reaches of /api/xbin. TestDelegateRouteClasses fails on any difference —\n" +
		"# a change here is a security decision (docs/auth.md §Delegation). Regenerate with\n" +
		"# UPDATE_DELEGATE_CLASSES=1 go test ./internal/apicheck -run TestDelegateRouteClasses\n" +
		strings.Join(lines, "\n") + "\n"
	golden := filepath.Join("testdata", "delegate-classes.golden")
	got, _ := os.ReadFile(golden)
	if string(got) != want {
		if why := goldenUpdateRefused(os.Getenv); os.Getenv("UPDATE_DELEGATE_CLASSES") != "" && why != "" {
			t.Fatal(why)
		}
		if os.Getenv("UPDATE_DELEGATE_CLASSES") != "" {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s", golden)
		} else {
			was := map[string]string{}
			for _, l := range strings.Split(string(got), "\n") {
				if route, class, ok := strings.Cut(l, "\t"); ok && !strings.HasPrefix(l, "#") {
					was[route] = class
				}
			}
			now := map[string]string{}
			for _, l := range lines {
				route, class, _ := strings.Cut(l, "\t")
				now[route] = class
				if w, ok := was[route]; !ok {
					t.Errorf("%s is classed %q and isn't in %s", route, class, golden)
				} else if w != class {
					t.Errorf("%s was %q and is now %q", route, strings.ReplaceAll(w, "\t", " + "), strings.ReplaceAll(class, "\t", " + "))
				}
			}
			for route := range was {
				if _, ok := now[route]; !ok {
					t.Errorf("%s is in %s and has no class any more", route, golden)
				}
			}
			t.Errorf("the delegation cap differs from %s: what a person's agent reaches changed. If that is the decision, say so in the change and regenerate (UPDATE_DELEGATE_CLASSES=1 go test ./internal/apicheck -run TestDelegateRouteClasses)", golden)
		}
	}
	n := map[server.DelegateClass]int{}
	for _, c := range table {
		n[c]++
	}
	t.Logf("%d routes: %d read, %d act, %d admin-read, %d never, %d refused", len(table),
		n[server.DelegateRead], n[server.DelegateAct], n[server.DelegateAdminRead], n[server.DelegateNever], n[server.DelegateRefused])
}

// goldenUpdateRefused is why UPDATE_DELEGATE_CLASSES may not rewrite the
// golden file here ("": it may). Never where CI runs: a pipeline that sets
// the variable — by accident, or to get a build green — would approve
// whatever the table says. Regenerating is a person's act on their own
// machine, and the diff it leaves is what a reviewer reads.
func goldenUpdateRefused(getenv func(string) string) string {
	for _, k := range []string{"CI", "GITHUB_ACTIONS", "BUILDKITE", "GITLAB_CI"} {
		if v := getenv(k); v != "" && v != "0" && v != "false" {
			return "UPDATE_DELEGATE_CLASSES is refused where " + k + " is set: the delegation cap's golden file is regenerated by a person, locally, and reviewed as a diff"
		}
	}
	return ""
}

// The golden file pins, beside each route's class, the audit flag and the
// tile function; and regenerating it is refused in CI.
func TestDelegateGoldenPinsTheRow(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "delegate-classes.golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"GET /grants\tadmin-read\taudit",                        // an admin read is always logged
		"GET /components/{path...}\tread\ttile:byComponentPath", // the tile whose org is asked
		"GET /code/file\tread\taudit\ttile:byComponent",
		"GET /term/sessions/{id}/events\tread\ttile:bySession",
		"POST /term/sessions\tact\tticket",
	} {
		if !strings.Contains(string(raw), "\n"+line+"\n") {
			t.Errorf("the golden file has no line %q", line)
		}
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if goldenUpdateRefused(env(map[string]string{"CI": "true"})) == "" || goldenUpdateRefused(env(map[string]string{"GITHUB_ACTIONS": "1"})) == "" {
		t.Error("the golden file may be regenerated in CI")
	}
	if why := goldenUpdateRefused(env(map[string]string{"CI": "false"})); why != "" {
		t.Errorf("a local run: %s", why)
	}
}
