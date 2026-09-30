package push

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// covers PD-27 06§8 — a person's partition of a partitioned tile acts for
// its person: its backend notifies only them, as a frame does, never
// another reader of the tile; the global instance (an instance token with
// no user partition) keeps the backend's rule.
func TestNotifyFromPartition(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-alice")
	r.register(bob, "phone", "handle-bob")
	bobsPartition := auth.Principal{Component: "apps/other", Via: "instance", Partition: "user:bob"}
	global := auth.Principal{Component: "apps/other", Via: "instance", Partition: "global"}
	for _, c := range []struct {
		p    auth.Principal
		user string
		want int
		text string
	}{
		{bobsPartition, "alice", 403, "a person's partition notifies only its person (bob)"},
		{bobsPartition, "bob", 202, ""},
		{bobsPartition, "user:bob", 202, ""},
		{global, "alice", 202, ""},
	} {
		code, out, _ := r.call(c.p, "POST", "/notify", map[string]any{"user": c.user, "title": "x"})
		if code != c.want || c.text != "" && !strings.Contains(out["error"].(string), c.text) {
			t.Errorf("%s (%s) → %s: %d %v, want %d %q", c.p.From(), c.p.Partition, c.user, code, out, c.want, c.text)
		}
	}
}
