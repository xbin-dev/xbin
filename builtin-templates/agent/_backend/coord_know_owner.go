// coord_know_owner.go — whose knowledge store it is (coord_know.go). The
// store began as a project's wiki, keyed by the project's id; an owner is
// now a kind and a key — a project ("project", its id), and whatever
// another coordinator profile keeps notes for (a person's notebook) — and
// DB.knowID turns one into the store id every table of the store carries
// in its project_id column.
//
// Nothing stored moved for this. A project's store id is its own id, so
// every row a project's wiki ever wrote is keyed as it was, the indexes
// and the search table are the ones an instance already has, and a build
// from before owners reads the same database (it never meets another
// owner's rows: it only asks for project ids). Any other owner gets a
// negative store id: the negated row id of its entry in wiki_owners, made
// on first use and never reused. So the two can't collide, and no table
// was altered.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func init() { schemaAdds = append(schemaAdds, (*DB).addKnowOwners) }

// knowOwner is whose store it is.
type knowOwner struct {
	Kind string // a registered knowKind
	Key  string // the owner within its kind: a project's id; a person
}

func (o knowOwner) String() string { return o.Kind + ":" + o.Key }

// knowProject is the kind of a project's wiki: its key and its store id
// are the project's id.
const knowProject = "project"

// projectKnow is project pid's wiki as an owner.
func projectKnow(pid int64) knowOwner {
	return knowOwner{Kind: knowProject, Key: strconv.FormatInt(pid, 10)}
}

// knowKind is what the store needs to know about a kind of owner.
type knowKind struct {
	Kind string
	// Name is the store in a sentence ("this project's wiki"): the words of
	// a refusal.
	Name string
	// Changed runs in the transaction of every write to a store of this
	// kind (nil: nobody watches): a project's wiki tells the project's
	// stream.
	Changed func(t *DB, o knowOwner)
}

// knowKinds is every registered kind.
var knowKinds = map[string]*knowKind{}

// registerKnowKind adds a kind of owner (at init).
func registerKnowKind(k *knowKind) {
	if k.Kind == "" || strings.Contains(k.Kind, ":") || knowKinds[k.Kind] != nil {
		panic("knowledge store: owner kind " + k.Kind + " is empty, has a colon or is registered twice")
	}
	knowKinds[k.Kind] = k
}

const knowOwnersSchema = `
-- the owners of knowledge stores that aren't projects: store id = -id
CREATE TABLE IF NOT EXISTS wiki_owners (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT    NOT NULL,
  key        TEXT    NOT NULL,
  created_ms INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_wiki_owners ON wiki_owners(kind, key);
`

// addKnowOwners makes wiki_owners (schemaAdds: additive, idempotent, never
// on team).
func (d *DB) addKnowOwners() error {
	_, err := d.q.Exec(knowOwnersSchema)
	return err
}

// errKnowNone: the owner has no store yet (knowID without create).
var errKnowNone = errors.New("no knowledge store yet")

// knowID is the store id of o's knowledge store — what coord_know.go's
// functions take. A project's is its id (it needs no row, and exists as
// soon as the project does); any other owner's is made on first use when
// create is set, errKnowNone otherwise.
func (d *DB) knowID(o knowOwner, create bool) (int64, error) {
	switch {
	case knowKinds[o.Kind] == nil:
		return 0, fmt.Errorf("knowledge store: no owner kind %q", o.Kind)
	case o.Key == "":
		return 0, errors.New("knowledge store: an owner needs a key")
	}
	if o.Kind == knowProject {
		pid, err := strconv.ParseInt(o.Key, 10, 64)
		if err != nil || pid <= 0 {
			return 0, fmt.Errorf("knowledge store: %q isn't a project's id", o.Key)
		}
		return pid, nil
	}
	var id int64
	err := d.q.QueryRow(`SELECT id FROM wiki_owners WHERE kind=? AND key=?`, o.Kind, o.Key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if !create {
			return 0, errKnowNone
		}
		if _, err := d.q.Exec(`INSERT INTO wiki_owners (kind, key, created_ms) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, o.Kind, o.Key, nowMs()); err != nil {
			return 0, err
		}
		err = d.q.QueryRow(`SELECT id FROM wiki_owners WHERE kind=? AND key=?`, o.Kind, o.Key).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	return -id, nil
}

// knowOwnerOf is the owner of store id (ok false: there is none).
func (d *DB) knowOwnerOf(id int64) (o knowOwner, ok bool) {
	switch {
	case id > 0:
		return projectKnow(id), true
	case id == 0:
		return knowOwner{}, false
	}
	if d.q.QueryRow(`SELECT kind, key FROM wiki_owners WHERE id=?`, -id).Scan(&o.Kind, &o.Key) != nil {
		return knowOwner{}, false
	}
	return o, true
}

// knowKindOf is the kind of store id's owner (nil: unknown).
func (d *DB) knowKindOf(id int64) (*knowKind, knowOwner) {
	o, ok := d.knowOwnerOf(id)
	if !ok {
		return nil, o
	}
	return knowKinds[o.Kind], o
}

// knowName is store id in a sentence: its kind's Name.
func (d *DB) knowName(id int64) string {
	if k, _ := d.knowKindOf(id); k != nil && k.Name != "" {
		return k.Name
	}
	return "this wiki"
}

// knowChanged tells whoever watches store id that a page changed (in the
// write's transaction).
func (t *DB) knowChanged(id int64) {
	if k, o := t.knowKindOf(id); k != nil && k.Changed != nil {
		k.Changed(t, o)
	}
}
