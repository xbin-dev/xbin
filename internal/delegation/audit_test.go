package delegation

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The audit log: rows per person, newest first; side effects and refusals
// always written, repeated reads once a minute with the count carried into
// the next row; KeepMonths monthly files.
func TestAuditLog(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	read := Call{User: "al/../ice", Agent: "xbin/agent", Method: "GET", Path: "/api/xbin/components", Class: "read", Status: 200}
	for i := 0; i < 5; i++ { // one row, four counted
		if err := l.Append(read); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Second)
	}
	act := Call{User: "al/../ice", Agent: "xbin/agent", Method: "POST", Path: "/api/apps/x/send", Tile: "apps/x", Class: "tile-write", Effect: true, Status: 200}
	refused := Call{User: "al/../ice", Agent: "xbin/agent", Method: "POST", Path: "/api/xbin/grants", Class: "never", Status: 403, Refused: "the admin API is never delegated"}
	for i := 0; i < 2; i++ { // never coalesced
		_ = l.Append(act)
		_ = l.Append(refused)
	}
	now = now.Add(2 * time.Minute)
	_ = l.Append(read) // the window closed: a row, carrying the four
	_ = l.Append(Call{User: "bob", Agent: "xbin/agent", Method: "GET", Path: "/api/xbin/whoami", Class: "read", Status: 200})

	rows, err := l.Read("al/../ice", time.Time{}, 0)
	if err != nil || len(rows) != 6 {
		t.Fatalf("rows: %d %v", len(rows), err)
	}
	if rows[0].Path != "/api/xbin/components" || rows[0].N != 4 || !rows[0].Time.After(rows[1].Time) {
		t.Fatalf("newest first, with the coalesced count: %+v", rows[0])
	}
	if rows[5].Path != "/api/xbin/components" || rows[5].N != 0 {
		t.Fatalf("the oldest: %+v", rows[5])
	}
	if got, _ := l.Read("al/../ice", rows[0].Time, 2); len(got) != 2 || got[0].Refused == "" {
		t.Fatalf("before + limit: %+v", got)
	}
	if got, _ := l.Read("bob", time.Time{}, 0); len(got) != 1 {
		t.Fatalf("bob's: %+v", got)
	}
	if got, _ := l.Read("nobody", time.Time{}, 0); len(got) != 0 {
		t.Fatalf("nobody's: %+v", got)
	}
	// a person's id never names a path: one directory level, 0700/0600
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
	rows, _ = l.Read("al/../ice", time.Time{}, 0)
	if len(rows) != 1 {
		t.Fatalf("after %d months: %d rows, want the new one only", KeepMonths, len(rows))
	}
	var nilLog *Log
	if err := nilLog.Append(act); err != nil {
		t.Fatal(err)
	}
}
