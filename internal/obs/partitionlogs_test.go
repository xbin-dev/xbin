package obs

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-24 06§5 — a followed partition log is read only while the
// broker still says so: when the share that let an admin read it ends (or
// is revoked), the stream ends by itself.
func TestPartitionLogFollowEndsWithTheShare(t *testing.T) {
	oldPoll, oldCheck := logPoll, logRecheck
	logPoll, logRecheck = 5*time.Millisecond, 20*time.Millisecond
	defer func() { logPoll, logRecheck = oldPoll, oldCheck }()

	o := testPlane(t)
	rel := ".xbin/partition/" + util.TileKey("apps/calendar") + "/main/u-alice/backend.log"
	p := filepath.Join(o.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("alice's line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var shared atomic.Bool
	shared.Store(true)
	o.PartitionLog = func(_ auth.Principal, tile string, q url.Values) (string, util.Partition, bool, int, error) {
		if !shared.Load() {
			return "", "", true, 403, errForbidden
		}
		return rel, "user:alice", true, 0, nil
	}
	r := httptest.NewRequest("GET", "/logs?component=apps/calendar&user=alice&follow=1", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "bob"}))
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { o.apiLogs(w, r); close(done) }()
	time.Sleep(60 * time.Millisecond)
	shared.Store(false) // the share ends (expired, or revoked)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the follow kept streaming a log whose share ended")
	}
	if body := w.Body.String(); !strings.Contains(body, "alice's line") || !strings.Contains(body, "stream closed") {
		t.Errorf("body %q", body)
	}
}

var errForbidden = &forbiddenErr{}

type forbiddenErr struct{}

func (*forbiddenErr) Error() string { return "not shared" }
