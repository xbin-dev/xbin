package broker

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

func TestReplaceWordBoundaries(t *testing.T) {
	cases := []struct{ in, want string }{
		{`res:apps/calendar/events`, `res:apps/planner/events`},              // self resource
		{`/api/apps/calendar/x`, `/api/apps/planner/x`},                      // API URL
		{`"apps/calendar"`, `"apps/planner"`},                                // quoted path
		{`apps/calendar`, `apps/planner`},                                    // whole string
		{`apps/calendar-sync`, `apps/calendar-sync`},                         // sibling extension — untouched
		{`apps/calendarx`, `apps/calendarx`},                                 // word continuation — untouched
		{`a apps/calendar,apps/calendar b`, `a apps/planner,apps/planner b`}, // adjacent matches
	}
	for _, c := range cases {
		got, _ := replaceWord(c.in, "apps/calendar", "apps/planner")
		if got != c.want {
			t.Errorf("replaceWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClone(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root

	// A frontend that hardcodes its own path (the thing rewrite must fix) and
	// mentions a sibling-extension path that must survive untouched.
	js := "xbin.bus.on('res:apps/calendar/bus/x/', f);\nfetch('/api/apps/calendar/y');\n// see apps/calendar-sync\n"
	if err := os.WriteFile(filepath.Join(root, "apps/calendar/app.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = b.Reg.Rescan()

	do := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/clone", strings.NewReader(body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
		w := httptest.NewRecorder()
		b.apiClone(w, r)
		return w
	}

	if w := do(`{"from":"apps/calendar","to":"apps/planner"}`); w.Code != 200 {
		t.Fatalf("clone: %d %s", w.Code, w.Body.String())
	}

	// The clone is registered and its self-refs resolve in the NEW scope.
	if _, ok := b.Reg.Component("apps/planner"); !ok {
		t.Fatal("clone not registered")
	}
	if warns := b.unresolvedUses("apps/planner"); len(warns) > 0 {
		t.Fatalf("clone has unresolved uses: %v", warns)
	}
	man, _ := os.ReadFile(filepath.Join(root, "apps/planner/xbin.json"))
	if !strings.Contains(string(man), "res:apps/planner/events") {
		t.Fatalf("manifest self-refs not rewritten: %s", man)
	}
	got, _ := os.ReadFile(filepath.Join(root, "apps/planner/app.js"))
	want := "xbin.bus.on('res:apps/planner/bus/x/', f);\nfetch('/api/apps/planner/y');\n// see apps/calendar-sync\n"
	if string(got) != want {
		t.Fatalf("code rewrite wrong:\n got: %q\nwant: %q", got, want)
	}

	// Source untouched; unrelated components untouched.
	srcMan, _ := os.ReadFile(filepath.Join(root, "apps/calendar/xbin.json"))
	if !strings.Contains(string(srcMan), "res:apps/calendar/events") {
		t.Fatal("source manifest was modified")
	}
	emailMan, _ := os.ReadFile(filepath.Join(root, "apps/email/xbin.json"))
	if !strings.Contains(string(emailMan), "res:apps/calendar/bus") {
		t.Fatal("email's cross-scope ref to the ORIGINAL must be untouched")
	}

	// Guards: duplicate dest, missing source, nesting, reserved.
	if w := do(`{"from":"apps/calendar","to":"apps/planner"}`); w.Code != 409 {
		t.Fatalf("existing dest: want 409, got %d", w.Code)
	}
	if w := do(`{"from":"apps/nope","to":"apps/x"}`); w.Code != 404 {
		t.Fatalf("missing source: want 404, got %d", w.Code)
	}
	if w := do(`{"from":"apps/calendar","to":"apps/calendar/sub"}`); w.Code < 400 {
		t.Fatalf("nested dest must be rejected, got %d", w.Code)
	}
	if w := do(`{"from":"apps/calendar","to":"data/x"}`); w.Code != 400 {
		t.Fatalf("reserved dest: want 400, got %d", w.Code)
	}
}
