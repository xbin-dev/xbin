package boot

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sealed"
)

// What makes a component a delegation holder, and where xbind delivers its
// notices, is the release's table and nothing else: boot installs both
// lookups from it (auth.SetDelegateHolders, auth.SetDelegateNotify). A row
// without the capability holds nothing and is told nothing; a tile — whatever
// its manifest says — neither.
func TestSealedTableNamesHoldersAndTheirNoticePath(t *testing.T) {
	ws := probeWorkspace(t)
	holder := sealed.Spec{Name: "people", Title: "People", Source: sealedCounter("h1"), Authored: "apps/counter",
		Capabilities: sealed.Capabilities{Delegate: true, Agent: true}, Notify: "/notify"}
	d := startSealed(t, ws, []sealed.Spec{probeSpec(), holder})
	t.Cleanup(func() { auth.SetDelegateHolders(nil); auth.SetDelegateNotify(nil) })

	if !auth.HoldsDelegation("xbin/people") || auth.DelegateNotifyPath("xbin/people") != "/notify" {
		t.Errorf("the holder row: holds %v, notices on %q; want true and /notify",
			auth.HoldsDelegation("xbin/people"), auth.DelegateNotifyPath("xbin/people"))
	}
	// …and which component is the agent xbin's chrome frames (D232): the
	// row marked so, as GET /delegate/state names it
	if got := d.st.Sealed.Agent(); got != "xbin/people" {
		t.Errorf("the table's agent: %q", got)
	}
	if code, body := d.do("GET", "/api/xbin/delegate/state", nil); code != 200 || !strings.Contains(body, `"agent":"xbin/people"`) {
		t.Errorf("GET /delegate/state names no agent: %d %s", code, body)
	}
	for _, c := range []string{probePath, "apps/plain", "apps/prov", "xbin/nobody", "xbin/people/sub", ""} {
		if auth.HoldsDelegation(c) || auth.DelegateNotifyPath(c) != "" {
			t.Errorf("%q: holds %v, notices on %q; want neither", c, auth.HoldsDelegation(c), auth.DelegateNotifyPath(c))
		}
	}
}
