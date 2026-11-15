package delegation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// A row can't blind the log (the second review's N1): every field is
// bounded when it is written — the tile came unbounded from a query or a
// session body — and a line the reader can't take (over-long, torn, not
// JSON, another shape) is skipped and counted, never an error for the
// month.
func TestAuditRowsCannotBlindTheLog(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir)
	big := strings.Repeat("x", 2<<20)
	if err := l.Append(Call{User: "bob", Agent: "xbin/wsagent", Method: "GET", Path: "/api/xbin/whoami", Class: "read", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Call{User: "bob", Agent: big, Method: big, Path: big, Tile: big, Class: big, Refused: big, Ticket: big, Under: big, Status: 403}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Call{User: "bob", Method: "POST", Path: "/api/apps/x/a", Tile: "apps/x", Class: "tile-write", Effect: true, Status: 200}); err != nil {
		t.Fatal(err)
	}
	rows, skipped, err := l.ReadCounted("bob", time.Time{}, 100)
	if err != nil || len(rows) != 3 || skipped != 0 {
		t.Fatalf("after a row with 2 MiB in every field: %d rows, %d skipped, %v", len(rows), skipped, err)
	}
	raw, _ := json.Marshal(rows[1])
	if len(raw) > MaxRow {
		t.Fatalf("the bounded row is %d bytes on the wire, over MaxRow %d", len(raw), MaxRow)
	}
	if !strings.HasSuffix(rows[1].Tile, "…") || len(rows[1].Tile) > MaxField+4 {
		t.Fatalf("its tile: %d bytes", len(rows[1].Tile))
	}
	// a person is never renamed by truncation: an id that long is refused
	if err := l.Append(Call{User: big, Method: "GET"}); err == nil {
		t.Fatal("a row for a 2 MiB user id was written")
	}

	// what an older xbind, a crash or a hand left in the file
	files, _ := filepath.Glob(filepath.Join(dir, "*", "audit-*.jsonl"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	junk := []string{
		`{"ts":"2026-11-15T00:00:00Z","user":"bob","agent":"a","method":"GET","path":"/x","tile":"` + big + `","class":"read","status":200}`, // over 1 MiB: what failed the scanner
		`{"ts":"2026-11-15T00:00:00Z","user":"bob","method":"GET","path":"/x",`,                                                              // torn
		`not json at all`, `[1,2,3]`, `{"user":7}`, `{"ts":"yesterday","user":"bob"}`, `{}`, ``,
		"\x00\xff\xfe binary \x00",
	}
	for _, j := range junk {
		if _, err := f.WriteString(j + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	if err := l.Append(Call{User: "bob", Method: "DELETE", Path: "/api/apps/x/b", Tile: "apps/x", Class: "tile-write", Effect: true, Status: 200}); err != nil {
		t.Fatal(err)
	}
	rows, skipped, err = l.ReadCounted("bob", time.Time{}, 100)
	if err != nil || len(rows) != 4 || rows[0].Method != "DELETE" || skipped != len(junk)-1 { // the empty line is no row and no loss
		t.Fatalf("after junk lines: %d rows, %d skipped (want %d), %v", len(rows), skipped, len(junk)-1, err)
	}
	if got, err := l.Read("bob", time.Time{}, 100); err != nil || len(got) != 4 {
		t.Fatalf("Read: %d %v", len(got), err)
	}
}

// FuzzAuditLine: whatever one line of the file holds, reading the month
// answers — the rows before and after it are still there.
func FuzzAuditLine(f *testing.F) {
	for _, seed := range []string{`{}`, `{"user":"bob"}`, `{"ts":1}`, "\x00", `"`, `{"user":"bob","path":"` + strings.Repeat("a", 70<<10) + `"}`, "a\rb", `{"user":"bob","n":1e999}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		dir := t.TempDir()
		l := Open(dir)
		if err := l.Append(Call{User: "bob", Method: "GET", Path: "/first", Status: 200}); err != nil {
			t.Fatal(err)
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*", "audit-*.jsonl"))
		fh, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.Write(append(line, '\n'))
		fh.Close()
		if err := l.Append(Call{User: "bob", Method: "GET", Path: "/last", Status: 200}); err != nil {
			t.Fatal(err)
		}
		rows, _, err := l.ReadCounted("bob", time.Time{}, 1000)
		if err != nil {
			t.Fatalf("a line made the month unreadable: %v", err)
		}
		var first, last bool
		for _, r := range rows {
			first, last = first || r.Path == "/first", last || r.Path == "/last"
		}
		if !first || !last {
			t.Fatalf("rows around the line were lost: %d rows", len(rows))
		}
	})
}
