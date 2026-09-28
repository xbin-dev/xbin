//go:build linux && integration

package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// covers T20 D119g D78 — the purge's one run inside a real sandbox (the test
// rootfs's git, sed and rm, the store the only thing bound): a checkpoint a
// deploy log names is rewritten out of it, its refs and its unique content
// go, its materialized tree goes, and the kept checkpoint stays whole.
func TestPurgeConfined(t *testing.T) {
	confined(t)
	s, _ := testStore(t)
	ctx := context.Background()
	src := tile(t, s, "apps/purge", map[string]string{"app.js": "shared\n", "secret.env": "TOKEN=hunter2\n"})
	t1 := capture(t, s, src, true).Hash
	secret := blobOf(t, s, src.Tile, t1, "secret.env")
	if err := os.Remove(filepath.Join(src.WorkTree, "secret.env")); err != nil {
		t.Fatal(err)
	}
	t2 := capture(t, s, src, false).Hash
	for i, tr := range []string{t1, t2} {
		if err := s.AppendLog(ctx, src.Tile, LogEntry{ID: int64(i + 1), Deployment: "main", How: "deploy", Checkpoint: tr,
			Feed: FeedWorkTree, By: "user:ana", Result: LogOK, FinishedAt: logAt}); err != nil {
			t.Fatal(err)
		}
	}
	root1, err := s.Materialize(src.Tile, t1)
	if err != nil {
		t.Fatal(err)
	}

	res, err := s.Purge(ctx, src.Tile, t1, func() []string { return []string{t2} })
	if err != nil {
		t.Fatalf("a confined purge: %v", err)
	}
	if res.Entries != 1 || len(res.Logs) != 1 || res.Logs[0] != "main" {
		t.Errorf("Purge answered %+v", res)
	}
	if hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+t1) || hasObject(t, s, src.Tile, secret) || exists(root1) {
		t.Error("the purged checkpoint survived a confined purge")
	}
	if _, err := s.Resolve(ctx, src.Tile, "c:"+t1[:12]); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Errorf("the purged checkpoint still resolves: %v", err)
	}
	got, _, err := s.Log(ctx, src.Tile, LogQuery{Deployment: "main"})
	if err != nil || len(got) != 2 || got[1].Checkpoint != "" || got[0].Checkpoint != t2 {
		t.Errorf("main's log after a confined purge: %+v, %v", got, err)
	}
	if _, err := s.Materialize(src.Tile, t2); err != nil {
		t.Errorf("the kept checkpoint can't be materialized: %v", err)
	}
}
