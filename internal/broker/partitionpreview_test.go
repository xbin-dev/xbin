package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// A preview (D234) reads partitions that exist and asks nobody anything: on
// its own tile and ACROSS tiles it never starts a person's first partition,
// and where consent is needed and missing it is refused without a prompt.
func TestPreviewOpensNoPartition(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	preview := frameOf("apps/q", "alice")
	preview.Impersonator = auth.PreviewViewer
	opened := func(tile string) {
		t.Helper()
		pkey, uid, err := b.partitionKeyOf("alice")
		if err != nil {
			t.Fatal(err)
		}
		b.notePartitionStart(tile, "main", "user:alice", pkey, uid)
		if !b.partitionOpened("alice", tile) {
			t.Fatalf("%s: alice's partition is not recorded as opened", tile)
		}
	}
	refused := func(what, target, want string) {
		t.Helper()
		if part, err := b.addressedPartition(preview, target); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %q, %v — want a refusal saying %q", what, part, err, want)
		}
	}
	// her own tile, never opened
	refused("q, never opened", "apps/q", "open it once first")
	opened("apps/q")
	if part, err := b.addressedPartition(preview, "apps/q"); err != nil || part != "user:alice" {
		t.Fatalf("q, opened: %q, %v", part, err)
	}
	// across tiles: q is open, pg never was — the preview's frame of q must
	// not be what starts alice's first partition of pg
	refused("q → pg, pg never opened", "apps/pg", "never creates a partition")
	opened("apps/pg")
	if part, err := b.addressedPartition(preview, "apps/pg"); err != nil || part != "user:alice" {
		t.Fatalf("q → pg, both opened: %q, %v", part, err)
	}
	// the consent policy on, no consent: refused, and alice is NOT asked
	partRouteConsent(f.partWS, true)
	refused("q → pg without consent", "apps/pg", "alice hasn't let apps/q use their apps/pg data")
	time.Sleep(150 * time.Millisecond) // a prompt would leave on its own goroutine
	if n := len(f.pushed()); n != 0 {
		t.Errorf("a preview's refused call prompted alice %d time(s): %q", n, f.pushed())
	}
	// the person's own frame, same call: refused too, and asked (today's rule)
	if _, err := b.addressedPartition(frameOf("apps/q", "alice"), "apps/pg"); err == nil {
		t.Error("alice's own frame reached pg without consent")
	}
	waitFor(t, func() bool { return len(f.pushed()) == 1 }, "the person's own prompt")
}
