package delegation

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The audit log: rows per person, newest first, every row written;
// KeepMonths monthly files.
func TestAuditLog(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	const alice = "al/../ice" // an id never names a path
	read := Call{User: alice, Agent: "xbin/agent", Method: "GET", Path: "/api/apps/x/notes", Tile: "apps/x", Class: "tile-read", Status: 200}
	act := Call{User: alice, Agent: "xbin/agent", Method: "POST", Path: "/api/apps/x/send", Tile: "apps/x", Class: "tile-write", Effect: true, Status: 200, Ticket: "t1"}
	refused := Call{User: alice, Agent: "xbin/agent", Method: "POST", Path: "/api/xbin/grants", Class: "never", Status: 403, Refused: "the admin API is never delegated"}
	for _, c := range []Call{read, read, act, refused, act} {
		if err := l.Append(c); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Second)
	}
	_ = l.Append(Call{User: "bob", Agent: "xbin/agent", Method: "GET", Path: "/api/xbin/code/tree", Class: "read", Status: 200})

	rows, err := l.Read(alice, time.Time{}, 0)
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows: %d %v", len(rows), err)
	}
	if rows[0].Path != "/api/apps/x/send" || rows[0].Ticket != "t1" || !rows[0].Time.After(rows[1].Time) || rows[4].Class != "tile-read" {
		t.Fatalf("newest first: %+v … %+v", rows[0], rows[4])
	}
	if got, _ := l.Read(alice, rows[0].Time, 2); len(got) != 2 || got[0].Refused == "" {
		t.Fatalf("before + limit: %+v", got)
	}
	if got, _ := l.Read("bob", time.Time{}, 0); len(got) != 1 {
		t.Fatalf("bob's: %+v", got)
	}
	if got, _ := l.Read("nobody", time.Time{}, 0); len(got) != 0 {
		t.Fatalf("nobody's: %+v", got)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Fatalf("%d directories under the log", len(ents))
	}
	for _, e := range ents {
		fi, _ := os.Stat(filepath.Join(dir, e.Name()))
		if !e.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v", e.Name(), fi.Mode())
		}
	}

	// retention: a file older than KeepMonths goes when a new month starts
	now = now.AddDate(0, KeepMonths, 0)
	_ = l.Append(act)
	if rows, _ = l.Read(alice, time.Time{}, 0); len(rows) != 1 {
		t.Fatalf("after %d months: %d rows, want the new one only", KeepMonths, len(rows))
	}
	var nilLog *Log
	if err := nilLog.Append(act); err != nil {
		t.Fatal(err)
	}
}
