// store.go — the manager's own table, in its sqlite resource (XBIN_RES_DB).
// It keeps what the substrate doesn't: each sandbox's contract id and the
// runtime name behind it, its owner, people, shares, labels and version;
// the clientIds of creates and snapshots; the built images; the operators'
// config. Execs and their output are the substrate's (lost with it, as the
// contract allows) — exec clientIds live in memory.
//
// Rows are JSON documents keyed by id: the manager holds every record in
// memory and writes each change through, so the schema stays one table per
// kind and a new field is a new JSON key, never a migration.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, no cgo
)

// record is one sandbox as the manager keeps it.
type record struct {
	ID         string            `json:"id"`      // the contract's id (sb-…)
	Runtime    string            `json:"runtime"` // the substrate's name — never shown to consumers
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Size       string            `json:"size"`
	Egress     string            `json:"egress"`         // the contract egress last asked for (the runtime says what applies)
	Mode       string            `json:"mode,omitempty"` // the substrate's mode it was made in (vm | namespace): its isolation
	Owner      owner             `json:"owner"`
	Visibility string            `json:"visibility"`
	Members    []string          `json:"members"`
	Shares     []share           `json:"shares"`
	Labels     map[string]string `json:"labels"`
	Workdir    string            `json:"workdir"`
	Home       string            `json:"home"`
	User       string            `json:"user"`
	UID        int               `json:"uid"`
	GID        int               `json:"gid"`
	Shell      string            `json:"shell"`
	Created    int64             `json:"created"`
	Version    int               `json:"version"`
	// Overlay is a state of the manager's own that hides the substrate's:
	// creating (an image builds, the clone or the first start runs),
	// deleting, or error (Detail says why).
	Overlay string `json:"overlay,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Prepared: the workdir and home were made (once, at the first start).
	Prepared bool `json:"prepared,omitempty"`
	// Plan is a creation still under way — what a restarted manager resumes.
	Plan *createPlan `json:"plan,omitempty"`
}

// owner is the contract's owner: {user, via, asserted}.
type owner struct {
	User     string `json:"user"`
	Via      string `json:"via"`
	Asserted bool   `json:"asserted"`
}

// share is a sandbox shared with another consumer: all the people it serves
// ("*") or a list.
type share struct {
	Consumer string `json:"consumer"`
	Users    users  `json:"users"`
}

// users is a share's people: "*" or a list of user ids.
type users struct {
	All  bool
	List []string
}

func (u users) MarshalJSON() ([]byte, error) {
	if u.All {
		return []byte(`"*"`), nil
	}
	if u.List == nil {
		return []byte(`[]`), nil
	}
	return json.Marshal(u.List)
}

func (u *users) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		if s != "*" {
			return errors.New(`users is "*" or a list of user ids`)
		}
		*u = users{All: true}
		return nil
	}
	*u = users{}
	return json.Unmarshal(b, &u.List)
}

func (u users) has(user string) bool {
	if u.All {
		return true
	}
	for _, x := range u.List {
		if x == user {
			return true
		}
	}
	return false
}

// createPlan is how a sandbox is being made.
type createPlan struct {
	Start       bool     `json:"start"`
	FromRuntime string   `json:"fromRuntime,omitempty"` // a clone's source sandbox (runtime name)
	FromID      string   `json:"fromId,omitempty"`      // …and its contract id (what an error says instead)
	FromSnap    string   `json:"fromSnap,omitempty"`
	Build       bool     `json:"build,omitempty"` // from an image that needs (re)building first
	AutoStopMin int      `json:"autoStopMin,omitempty"`
	Size        sizeSpec `json:"size"`
}

type sizeSpec struct {
	MemMiB  int `json:"memMiB"`
	VCPUs   int `json:"vcpus"`
	DiskGiB int `json:"diskGiB"`
}

// builtImage is an image's template sandbox and the snapshot its sandboxes
// clone.
type builtImage struct {
	ID        string `json:"id"`
	Runtime   string `json:"runtime"`
	Snapshot  string `json:"snapshot,omitempty"`
	SetupHash string `json:"setupHash"`
	Mode      string `json:"mode"`
	State     string `json:"state"` // building | ready | error
	Detail    string `json:"detail,omitempty"`
	Log       string `json:"log,omitempty"` // the setup's last output
	Started   int64  `json:"started,omitempty"`
	Built     int64  `json:"built,omitempty"`
	// Previous is the last good build while a newer one isn't ready (it is
	// building, or it failed): its template sandbox stays until a build
	// succeeds, and sandboxes of the image clone it while it is current for
	// the script and the mode (images.go usableBuild).
	Previous *builtImage `json:"previous,omitempty"`
}

// runtimes are the template sandboxes b names: its own, and its previous
// build's.
func (b *builtImage) runtimes() []string {
	out := []string{b.Runtime}
	if b.Previous != nil && b.Previous.Runtime != "" {
		out = append(out, b.Previous.Runtime)
	}
	return out
}

// store is the sqlite file.
type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	dsn := path
	if path != "" && !strings.HasPrefix(path, ":memory:") && !strings.Contains(path, "?") {
		dsn = path + "?_pragma=busy_timeout(5000)&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS sandboxes (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS idem (key TEXT PRIMARY KEY, target TEXT NOT NULL, hash TEXT NOT NULL, created INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idem_target ON idem(target)`,
		`CREATE TABLE IF NOT EXISTS images (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *store) close() error { return s.db.Close() }

func (s *store) records() ([]*record, error) {
	rows, err := s.db.Query(`SELECT data FROM sandboxes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*record
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var r record
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *store) putRecord(r *record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO sandboxes (id, data) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, r.ID, string(b))
	return err
}

// deleteRecord forgets a sandbox and the clientIds that named it.
func (s *store) deleteRecord(id string) error {
	if _, err := s.db.Exec(`DELETE FROM sandboxes WHERE id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM idem WHERE target = ? OR target LIKE ?`, id, id+"/%")
	return err
}

// idem finds a clientId: what it made (a sandbox id, or <id>/<snapshot>)
// and the hash of the request that made it.
func (s *store) idem(key string) (target, hash string, ok bool, err error) {
	err = s.db.QueryRow(`SELECT target, hash FROM idem WHERE key = ?`, key).Scan(&target, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return target, hash, err == nil, err
}

func (s *store) putIdem(key, target, hash string) error {
	_, err := s.db.Exec(`INSERT INTO idem (key, target, hash, created) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET target = excluded.target, hash = excluded.hash, created = excluded.created`,
		key, target, hash, time.Now().UnixMilli())
	return err
}

func (s *store) deleteIdem(key string) error {
	_, err := s.db.Exec(`DELETE FROM idem WHERE key = ?`, key)
	return err
}

func (s *store) images() ([]*builtImage, error) {
	rows, err := s.db.Query(`SELECT data FROM images`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*builtImage
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var im builtImage
		if err := json.Unmarshal([]byte(data), &im); err != nil {
			return nil, err
		}
		out = append(out, &im)
	}
	return out, rows.Err()
}

func (s *store) putImage(im *builtImage) error {
	b, err := json.Marshal(im)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO images (id, data) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, im.ID, string(b))
	return err
}

func (s *store) deleteImage(id string) error {
	_, err := s.db.Exec(`DELETE FROM images WHERE id = ?`, id)
	return err
}

func (s *store) setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *store) putSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// config is the stored config, or the default one.
func (s *store) config() (Config, error) {
	v, err := s.setting("config")
	if err != nil || v == "" {
		return defaultConfig(), err
	}
	c := defaultConfig()
	if err := json.Unmarshal([]byte(v), &c); err != nil {
		return defaultConfig(), err
	}
	if err := c.validate(); err != nil {
		return defaultConfig(), err
	}
	return c, nil
}

func (s *store) putConfig(c Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.putSetting("config", string(b))
}
