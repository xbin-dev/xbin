package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-44 01§2.5 H1 — a switch that deletes main's namespace (user ↔
// unpartitioned, or removing "global") mounts main's volumes again, empty,
// as at provision, on every way out: after a switch to user partitions and
// global, after removing global, and after a switch whose wipe stops
// part-way (500, the request open). Without it the tile's instance at
// today's keys — global, the unpartitioned one, or a person's shared volume
// there — stays held ("its decrypted view isn't mounted") until xbind
// restarts or the workspace next changes. Over nsFakeVolumes' stand-in
// gocryptfs, so CI runs it; test/isolated's partitions smoke drives the
// real one on a dev box.
func TestPartitionSwitchRemountsMain(t *testing.T) {
	manifest := func(partition string) string {
		m := `{"runtime":"go"`
		if partition != "" {
			m += `,"partition":` + partition
		}
		return m + `,"uses":[{"target":"res:apps/docs/db","role":"writer"},{"target":"res:apps/docs/files","role":"writer"}]}`
	}
	f := newSwitchFxWith(t, map[string]string{"apps/docs/xbin.json": manifest("")})
	runs := nsFakeVolumes(t, f.b)
	f.b.MountEncrypted()
	k, err := f.b.resKeys(resTarget{Scope: "apps/docs", Name: "files"}, util.MainDeployment)
	if err != nil {
		t.Fatal(err)
	}
	mounted := func() bool { return volumeMounted(f.b.resenc, k) }
	if !mounted() || f.b.EncryptionHoldReason("apps/docs") != "" {
		t.Fatalf("before: main's files mounted %v, hold %q", mounted(), f.b.EncryptionHoldReason("apps/docs"))
	}
	root := auth.Principal{Owner: true}
	// switch asks for spec on a tile holding data (pending) and switches
	// from → to; it returns the fake gocryptfs's runs during the act
	switchTo := func(spec, body string, want int) string {
		t.Helper()
		f.kvPut("apps/docs", "db") // holds data: a request, not auto
		f.write(map[string]string{"apps/docs/xbin.json": manifest(spec)})
		f.rescan()
		if st, _, _ := f.state("apps/docs"); st != registry.PartitionPending {
			t.Fatalf("asking for %s: %v, want pending", spec, st)
		}
		before := runs()
		if code, out := f.act(root, body); code != want {
			t.Fatalf("the switch to %s: %d %v, want %d", spec, code, out, want)
		}
		if f.bucketHas("res:apps/docs/db") {
			t.Errorf("the switch to %s kept main's kv", spec)
		}
		return strings.TrimPrefix(runs(), before)
	}
	check := func(what, ran string) {
		t.Helper()
		if !mounted() {
			t.Errorf("%s: main's files volume isn't mounted again", what)
		}
		if why := f.b.EncryptionHoldReason("apps/docs"); why != "" {
			t.Errorf("%s: the tile's instance at today's keys is held: %s", what, why)
		}
		if !strings.Contains(ran, "-init") || !strings.Contains(ran, "/.xbin/resenc/apps~docs/files") {
			t.Errorf("%s: no fresh volume was made and mounted: %q", what, ran)
		}
	}

	// unpartitioned → user + global: everything goes; global runs at today's keys
	check("unpartitioned → user + global", switchTo(`["user","global"]`,
		`{"tile":"apps/docs","act":"switch","from":null,"to":{"user":true,"global":true},"confirm":"apps/docs"}`, 200))
	// removing global: global's data goes; people's shared volumes live there
	check("user + global → user", switchTo(`["user"]`,
		`{"tile":"apps/docs","act":"switch","from":{"user":true,"global":true},"to":{"user":true},"confirm":"apps/docs"}`, 200))

	// a wipe that stops part-way, after main's namespace went: 500, the
	// request stays open, and main's volumes are mounted all the same
	old := wipeHooks
	t.Cleanup(func() { wipeHooks = old })
	registerWipeHook(wipeHook{name: "failing", wipe: func(b *Broker, t wipeTarget, sum *wipeSummary) error {
		if t.DryRun {
			return nil
		}
		return errFailingHook
	}})
	check("a switch that stops part-way", switchTo("",
		`{"tile":"apps/docs","act":"switch","from":{"user":true},"to":null,"confirm":"apps/docs"}`, 500))
	if st, _, req := f.state("apps/docs"); st != registry.PartitionPending || req == nil {
		t.Errorf("after the failed switch: %v %+v, want the request open", st, req)
	}
}

var errFailingHook = errorString("a store can't be wiped")

type errorString string

func (e errorString) Error() string { return string(e) }
