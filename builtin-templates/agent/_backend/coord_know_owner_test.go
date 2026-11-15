package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testKnowKind registers an owner kind for a test and takes it out again.
func testKnowKind(t *testing.T, k *knowKind) {
	t.Helper()
	registerKnowKind(k)
	t.Cleanup(func() { delete(knowKinds, k.Kind) })
}

// tableShape is a table's columns and its indexes' SQL, as sqlite has them.
func tableShape(t *testing.T, q interface {
	Query(string, ...any) (*sql.Rows, error)
}, table string) string {
	t.Helper()
	var out []string
	rows, err := q.Query(`SELECT name || ' ' || type || ' ' || "notnull" || ' ' || COALESCE(dflt_value, '') || ' ' || pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	rows.Close()
	rows, err = q.Query(`SELECT COALESCE(sql, name) FROM sqlite_master WHERE tbl_name=? AND type IN ('table','index') ORDER BY name`, table)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	rows.Close()
	return strings.Join(out, "\n")
}

// An instance from before owners keeps its wiki as it is: a database whose
// wiki tables the previous build made (testdata/wiki-schema-before-owners.sql)
// and filled opens with this one, no table of the store altered and no row
// moved — a project's owner key resolves to the id its rows already carry,
// so its pages, their history, the search and a run's recorded read (the
// compare-and-swap's base) all still answer. Another kind of owner then
// keeps the same slugs beside it without touching it.
func TestKnowStoreFromBaseSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	schema, err := os.ReadFile("testdata/wiki-schema-before-owners.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(schema)); err != nil {
		t.Fatalf("the previous build's schema: %v", err)
	}
	// project 7's wiki as that build wrote it: an index at version 2 (both
	// versions kept), a page, their search rows, run 41's read of the page
	// and its snapshot of the index; project 9 has a page of the same slug
	for _, q := range []string{
		`INSERT INTO wiki_pages (id, project_id, slug, title, body, version, updated_by, updated_ms, created_ms, reads) VALUES
			(1, 7, 'index', 'Index', '- [[build]]: how to build', 2, 'alice', 2000, 1000, 3),
			(2, 7, 'build', 'Build', 'run make build, then make test', 1, 'task #1 (dream)', 1500, 1500, 1),
			(3, 9, 'build', 'Build', 'cargo build', 1, 'bob', 1600, 1600, 0)`,
		`INSERT INTO wiki_revisions (page_id, project_id, version, title, body, by, run_id, task_n, note, at_ms, at) VALUES
			(1, 7, 1, 'Index', 'nothing yet', 'alice', 0, 0, 'start', 1000, ''),
			(1, 7, 2, 'Index', '- [[build]]: how to build', 'alice', 0, 0, 'map', 2000, ''),
			(2, 7, 1, 'Build', 'run make build, then make test', 'task #1 (dream)', 40, 1, 'learned', 1500, 'web@1234567'),
			(3, 9, 1, 'Build', 'cargo build', 'bob', 0, 0, '', 1600, '')`,
		`INSERT INTO wiki_fts (title, body, project_id, page_id) VALUES
			('Index', '- [[build]]: how to build', 7, 1), ('Build', 'run make build, then make test', 7, 2), ('Build', 'cargo build', 9, 3)`,
		`INSERT INTO wiki_reads (run_id, page_id, version, at_ms) VALUES (41, 2, 1, 1700)`,
		`INSERT INTO wiki_snaps (run_id, project_id, version) VALUES (41, 7, 1)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("the previous build's rows: %v", err)
		}
	}
	tables := []string{"wiki_pages", "wiki_revisions", "wiki_reads", "wiki_snaps", "wiki_sync", "wiki_dreams"}
	before := map[string]string{}
	for _, tb := range tables {
		before[tb] = tableShape(t, raw, tb)
	}
	var ftsBefore string
	_ = raw.QueryRow(`SELECT sql FROM sqlite_master WHERE name='wiki_fts'`).Scan(&ftsBefore)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	open := func() *DB {
		d, err := openDB(path)
		if err != nil {
			t.Fatalf("opening the previous build's database: %v", err)
		}
		return d
	}
	d := open()
	for _, tb := range tables {
		if got := tableShape(t, d.q, tb); got != before[tb] {
			t.Errorf("%s was altered:\n%s\nwas:\n%s", tb, got, before[tb])
		}
	}
	var ftsNow string
	_ = d.q.QueryRow(`SELECT sql FROM sqlite_master WHERE name='wiki_fts'`).Scan(&ftsNow)
	if ftsNow != ftsBefore {
		t.Errorf("wiki_fts was made again:\n%s\nwas:\n%s", ftsNow, ftsBefore)
	}

	// the project's owner key is the id its rows carry
	sid, err := d.knowID(projectKnow(7), false)
	if err != nil || sid != 7 {
		t.Fatalf("project 7's store id: %d %v", sid, err)
	}
	if o, ok := d.knowOwnerOf(7); !ok || o != projectKnow(7) || o.String() != "project:7" {
		t.Fatalf("store 7's owner: %v %v", o, ok)
	}
	var owners int
	_ = d.q.QueryRow(`SELECT count(*) FROM wiki_owners`).Scan(&owners)
	if owners != 0 {
		t.Fatalf("a project's wiki needs no owner row; wiki_owners has %d", owners)
	}
	pg, err := d.wikiPage(sid, "build")
	if err != nil || pg.Body != "run make build, then make test" || pg.Version != 1 || pg.At != "web@1234567" || pg.Reads != 1 {
		t.Fatalf("project 7's page: %+v %v", pg, err)
	}
	if list := d.wikiPages(sid, false); len(list) != 2 || list[0].Slug != "index" || list[0].Version != 2 {
		t.Fatalf("project 7's pages: %+v", list)
	}
	if hits, _, err := d.wikiSearch(sid, "make test", 5); err != nil || len(hits) != 1 || hits[0].Slug != "build" {
		t.Fatalf("searching project 7's wiki: %+v %v", hits, err)
	}
	if hist := d.wikiHistory(1, 10); len(hist) != 2 || hist[1].Note != "start" {
		t.Fatalf("the index's history: %+v", hist)
	}
	if v, body, ok := d.wikiIndexFor(sid, 41); !ok || v != 1 || body != "nothing yet" {
		t.Fatalf("run 41's index snapshot: v%d %q %v", v, body, ok)
	}
	// the read that build recorded is still the write's base; a run that
	// never read is still told to
	if _, err := d.wikiPut(sid, wikiActor{By: "task #2", Run: 42}, wikiChange{Slug: "build", Edit: true, Old: "make test", New: "make check", Note: "x"}); err == nil ||
		!strings.Contains(err.Error(), wikiUnread) {
		t.Fatalf("a write by a run that never read: %v", err)
	}
	won, err := d.wikiPut(sid, wikiActor{By: "task #1", Run: 41}, wikiChange{Slug: "build", Edit: true, Old: "make test", New: "make check", Note: "renamed"})
	if err != nil || won.Version != 2 || won.Body != "run make build, then make check" {
		t.Fatalf("run 41's write on the version it read before the upgrade: %+v %v", won, err)
	}
	if hits, _, _ := d.wikiSearch(sid, "check", 5); len(hits) != 1 {
		t.Fatalf("the search after the write: %+v", hits)
	}
	if other, err := d.wikiPage(9, "build"); err != nil || other.Body != "cargo build" || other.Version != 1 {
		t.Fatalf("project 9's page of the same slug: %+v %v", other, err)
	}

	// another kind of owner beside it: its own id, the same slugs
	var changed []string
	testKnowKind(t, &knowKind{Kind: "notebook", Name: "your notebook", Changed: func(_ *DB, o knowOwner) { changed = append(changed, o.String()) }})
	if _, err := d.knowID(knowOwner{Kind: "notebook", Key: "alice"}, false); !errors.Is(err, errKnowNone) {
		t.Fatalf("a store nobody made: %v", err)
	}
	nb, err := d.knowID(knowOwner{Kind: "notebook", Key: "alice"}, true)
	if err != nil || nb >= 0 {
		t.Fatalf("alice's notebook's store id: %d %v", nb, err)
	}
	if again, _ := d.knowID(knowOwner{Kind: "notebook", Key: "alice"}, true); again != nb {
		t.Fatalf("the same owner, another id: %d then %d", nb, again)
	}
	if _, err := d.wikiPut(nb, wikiActor{By: "alice"}, wikiChange{Slug: "build", Body: "what alice likes built"}); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "notebook:alice" {
		t.Fatalf("the notebook's kind was told of %v", changed)
	}
	if pg, _ := d.wikiPage(sid, "build"); pg == nil || pg.Body != "run make build, then make check" {
		t.Fatalf("project 7's page after the notebook's write: %+v", pg)
	}
	if hits, _, _ := d.wikiSearch(nb, "build", 5); len(hits) != 1 || hits[0].Snippet == "" {
		t.Fatalf("the notebook's search: %+v", hits)
	}
	if hits, _, _ := d.wikiSearch(sid, "alice", 5); len(hits) != 0 {
		t.Fatalf("project 7's search finds the notebook: %+v", hits)
	}
	if _, err := d.wikiPage(nb, "nope"); err == nil || !strings.Contains(err.Error(), "in your notebook") {
		t.Fatalf("a missing page of the notebook: %v", err)
	}
	if _, err := d.wikiPage(sid, "nope"); err == nil || !strings.Contains(err.Error(), "in this project's wiki") {
		t.Fatalf("a missing page of the project's wiki: %v", err)
	}
	_ = d.sql.Close()

	// opening it again changes nothing, and the notebook keeps its id
	d = open()
	defer d.sql.Close()
	if again, err := d.knowID(knowOwner{Kind: "notebook", Key: "alice"}, false); err != nil || again != nb {
		t.Fatalf("the notebook's id after a restart: %d %v (was %d)", again, err, nb)
	}
	for _, tb := range tables {
		if got := tableShape(t, d.q, tb); got != before[tb] {
			t.Errorf("%s was altered by the second open", tb)
		}
	}
	// dropping the notebook takes its rows and its owner, nothing of a project's
	if err := d.Tx(func(t *DB) error { knowDrop(t, nb); return nil }); err != nil {
		t.Fatal(err)
	}
	var left, mine int
	_ = d.q.QueryRow(`SELECT count(*) FROM wiki_pages WHERE project_id<0`).Scan(&left)
	_ = d.q.QueryRow(`SELECT count(*) FROM wiki_pages WHERE project_id IN (7, 9)`).Scan(&mine)
	_ = d.q.QueryRow(`SELECT count(*) FROM wiki_owners`).Scan(&owners)
	if left != 0 || mine != 3 || owners != 0 {
		t.Fatalf("after dropping the notebook: %d of its pages, %d of the projects', %d owners", left, mine, owners)
	}
	if next, _ := d.knowID(knowOwner{Kind: "notebook", Key: "alice"}, true); next == nb || next >= 0 {
		t.Fatalf("a store id was used again: %d (the dropped one: %d)", next, nb)
	}
}

// An owner is a registered kind and a key: an unknown kind, an empty key
// and a project key that is no id are refused, and a project's owner never
// takes a row.
func TestKnowOwnerKeys(t *testing.T) {
	d := newTestDB(t)
	for _, o := range []knowOwner{{Kind: "nobody", Key: "x"}, {Kind: knowProject, Key: ""}, {Kind: knowProject, Key: "web"},
		{Kind: knowProject, Key: "-3"}, {Kind: knowProject, Key: "0"}} {
		if id, err := d.knowID(o, true); err == nil {
			t.Errorf("%v got store id %d", o, id)
		}
	}
	if _, ok := d.knowOwnerOf(0); ok {
		t.Error("store 0 has an owner")
	}
	if _, ok := d.knowOwnerOf(-99); ok {
		t.Error("a store id nobody was given has an owner")
	}
	if d.knowName(12) != "this project's wiki" {
		t.Errorf("a project's store in words: %q", d.knowName(12))
	}
	testKnowKind(t, &knowKind{Kind: "notebook"})
	a, err := d.knowID(knowOwner{Kind: "notebook", Key: "alice"}, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.knowID(knowOwner{Kind: "notebook", Key: "bob"}, true)
	if err != nil || a == b || a >= 0 || b >= 0 {
		t.Fatalf("two owners' ids: %d %d %v", a, b, err)
	}
	if o, ok := d.knowOwnerOf(b); !ok || o.Kind != "notebook" || o.Key != "bob" {
		t.Fatalf("store %d's owner: %v %v", b, o, ok)
	}
}
