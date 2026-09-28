package deployments

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/checkpoint"
)

// covers P5 P15 T10 — the diff and the checkpoint remote over the real
// store (reads.go): while main is paused, the diff from its checkpoint to the
// work tree names the edit (patch and stat), and the remote serves the view
// repository's HEAD and nothing off its allow-list; a tile no record governs
// has nothing to diff and nothing to fetch, and no store appears for it.
func TestPlaneDiffAndFetch(t *testing.T) {
	f := newGitOpsFx(t, true)
	f.p.cps = storeAdapter{checkpoint.New(f.root)}
	ctx, tile := context.Background(), opSite
	f.settle(tile, f.must(ownerP, OpPause, &PauseRequest{Tile: tile}))
	tree := *f.p.current(tile).Deployments["main"].Checkpoint
	f.write(tile+"/index.html", "<h1>v2</h1>")

	res, err := f.p.Diff(ctx, ownerP, tile, DiffSpec{Tree: tree}, DiffSpec{WorkTree: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Patch), "+<h1>v2</h1>") || res.From.Hash != tree || res.To.Hash == tree {
		t.Errorf("diff from %s to %s:\n%s", res.From.ID, res.To.ID, res.Patch)
	}
	st, err := f.p.Diff(ctx, ownerP, tile, DiffSpec{ID: res.From.ID}, DiffSpec{ID: res.To.ID}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 1 || st.Files[0].Path != "index.html" || st.Files[0].Status != "M" {
		t.Errorf("stat = %+v", st.Files)
	}

	get := func(tile, rel string) (*httptest.ResponseRecorder, error) {
		w := httptest.NewRecorder()
		return w, f.p.ServeFetch(w, httptest.NewRequest(http.MethodGet, "/x", nil), tile, rel)
	}
	if w, err := get(tile, "HEAD"); err != nil || !strings.Contains(w.Body.String(), "refs/heads/deploy/main") {
		t.Errorf("HEAD: %v %q", err, w.Body.String())
	}
	if w, err := get(tile, "config"); !errors.Is(err, checkpoint.ErrNotFetchable) || w.Body.Len() != 0 {
		t.Errorf("config: %v, %d bytes written", err, w.Body.Len())
	}

	if _, err := f.p.Diff(ctx, ownerP, opAPI, DiffSpec{}, DiffSpec{WorkTree: true}, "", false); !errors.Is(err, checkpoint.ErrNothingToDiff) {
		t.Errorf("a zero-state diff = %v, want nothing to diff", err)
	}
	if _, err := get(opAPI, "HEAD"); !errors.Is(err, checkpoint.ErrNotFetchable) {
		t.Errorf("a zero-state fetch = %v", err)
	}
	if _, err := os.Lstat(storeDir(f.root, opAPI)); !os.IsNotExist(err) {
		t.Errorf("a zero-state read made a store (%v)", err)
	}
}
