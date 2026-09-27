package tilesbx

// defs.go — sandbox definitions and their store. data/sandboxes.json is
// xbind's alone: data/ is masked from terminals and never bound into a
// backend, so no tile code can forge a mount, an egress class or a mode.
// Everything read back is validated again against the tile's current reach
// at every start.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// Mount is one filesystem a sandbox gets: a filesystem resource of its tile
// (res, at an optional sub-path), or the tile's own code (source, always
// read-only), at an absolute path inside the sandbox.
type Mount struct {
	Res    string `json:"res,omitempty"`
	Path   string `json:"path,omitempty"` // a clean relative sub-path of the resource
	Source bool   `json:"source,omitempty"`
	At     string `json:"at"`
	RO     bool   `json:"ro,omitempty"`
}

// NetDef is a sandbox's egress: "none", or one of its tile's sandbox-net
// classes ("class:<slot>").
type NetDef struct {
	Egress string `json:"egress"`
}

// Defaults are what every command in the sandbox gets unless it says
// otherwise.
type Defaults struct {
	Cwd   string            `json:"cwd,omitempty"`
	UID   *uint32           `json:"uid,omitempty"`
	GID   *uint32           `json:"gid,omitempty"`
	Shell string            `json:"shell,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
}

// From names a clone's source: a sandbox of the same tile, at a snapshot.
type From struct {
	Sandbox  string `json:"sandbox"`
	Snapshot string `json:"snapshot,omitempty"`
}

// Def is one sandbox's definition, as stored: the validated request, with
// its sizes resolved (defaults filled, clamped to the caps at create).
type Def struct {
	Name        string            `json:"name"`
	Mode        string            `json:"mode"` // namespace | vm
	MemMiB      int               `json:"memMiB"`
	VCPUs       int               `json:"vcpus"`
	DiskGiB     int               `json:"diskGiB"`
	Net         NetDef            `json:"net"`
	Mounts      []Mount           `json:"mounts,omitempty"`
	Defaults    Defaults          `json:"defaults"`
	Labels      map[string]string `json:"labels,omitempty"`
	For         string            `json:"for,omitempty"`         // a claim: the consumer tile
	ForUser     string            `json:"forUser,omitempty"`     // a claim: the person
	IdleStopMin int               `json:"idleStopMin,omitempty"` // 0 = the policy's
	AutoStart   bool              `json:"autoStart"`
	Created     int64             `json:"created"` // unix ms
	Version     int64             `json:"version"` // +1 on every change
	Base        string            `json:"base,omitempty"`
	ClientID    string            `json:"clientId,omitempty"`
	ReqHash     string            `json:"reqHash,omitempty"` // the create request's, for a clientId repeat
}

// clone deep-copies d.
func (d *Def) clone() *Def {
	c := *d
	c.Mounts = append([]Mount(nil), d.Mounts...)
	c.Labels = cloneMap(d.Labels)
	c.Defaults.Env = cloneMap(d.Defaults.Env)
	if d.Defaults.UID != nil {
		u := *d.Defaults.UID
		c.Defaults.UID = &u
	}
	if d.Defaults.GID != nil {
		g := *d.Defaults.GID
		c.Defaults.GID = &g
	}
	return &c
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// defsFile is data/sandboxes.json.
type defsFile struct {
	Version int                  `json:"version"`
	Tiles   map[string]*tileDefs `json:"tiles"`
}

type tileDefs struct {
	Sandboxes map[string]*Def `json:"sandboxes"`
}

const defsVersion = 1

// defStore holds the definitions in memory and writes them through,
// atomically (a temp file, fsync, rename), mode 0600. Callers hold m.mu.
type defStore struct {
	path string
	file defsFile
	err  error // unreadable or from a newer xbind: nothing is written over it
}

func loadDefs(path string) *defStore {
	s := &defStore{path: path, file: defsFile{Version: defsVersion, Tiles: map[string]*tileDefs{}}}
	b, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return s
	case err != nil:
		s.err = fmt.Errorf("reading %s: %w", path, err)
		return s
	}
	var f defsFile
	if err := json.Unmarshal(b, &f); err != nil {
		s.err = fmt.Errorf("%s is unreadable (%v); tile sandbox definitions are read-only until it is fixed", path, err)
		return s
	}
	if f.Version > defsVersion {
		s.err = fmt.Errorf("%s is version %d, newer than this xbind's %d; tile sandbox definitions are read-only", path, f.Version, defsVersion)
	}
	if f.Tiles == nil {
		f.Tiles = map[string]*tileDefs{}
	}
	for tile, td := range f.Tiles {
		if td == nil || len(td.Sandboxes) == 0 {
			delete(f.Tiles, tile)
			continue
		}
		for name, d := range td.Sandboxes {
			if d == nil || d.Name != name || validName(name) != nil {
				delete(td.Sandboxes, name) // never serve an entry no create could have made
			}
		}
	}
	s.file = f
	return s
}

// list is the key's definitions, by name (copies).
func (s *defStore) list(k Key) []*Def {
	td := s.file.Tiles[k.Tile]
	if !k.Main() || td == nil {
		return nil
	}
	out := make([]*Def, 0, len(td.Sandboxes))
	for _, d := range td.Sandboxes {
		out = append(out, d.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// get is one definition (a copy).
func (s *defStore) get(k Key, name string) (*Def, bool) {
	if !k.Main() {
		return nil, false
	}
	if td := s.file.Tiles[k.Tile]; td != nil {
		if d, ok := td.Sandboxes[name]; ok {
			return d.clone(), true
		}
	}
	return nil, false
}

// count is how many definitions the key has.
func (s *defStore) count(k Key) int {
	if td := s.file.Tiles[k.Tile]; k.Main() && td != nil {
		return len(td.Sandboxes)
	}
	return 0
}

// tiles are the tiles with definitions, sorted.
func (s *defStore) tiles() []string {
	out := make([]string, 0, len(s.file.Tiles))
	for t := range s.file.Tiles {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// put stores d (a copy) and writes the file; on a failed write nothing changed.
func (s *defStore) put(k Key, d *Def) error {
	if err := s.writable(k); err != nil {
		return err
	}
	td := s.file.Tiles[k.Tile]
	if td == nil {
		td = &tileDefs{Sandboxes: map[string]*Def{}}
		s.file.Tiles[k.Tile] = td
	}
	old, had := td.Sandboxes[d.Name]
	td.Sandboxes[d.Name] = d.clone()
	if err := s.save(); err != nil {
		if had {
			td.Sandboxes[d.Name] = old
		} else {
			delete(td.Sandboxes, d.Name)
			if len(td.Sandboxes) == 0 {
				delete(s.file.Tiles, k.Tile)
			}
		}
		return err
	}
	return nil
}

// del forgets a definition and writes the file; on a failed write nothing changed.
func (s *defStore) del(k Key, name string) error {
	if err := s.writable(k); err != nil {
		return err
	}
	td := s.file.Tiles[k.Tile]
	if td == nil {
		return nil
	}
	old, had := td.Sandboxes[name]
	if !had {
		return nil
	}
	delete(td.Sandboxes, name)
	if len(td.Sandboxes) == 0 {
		delete(s.file.Tiles, k.Tile)
	}
	if err := s.save(); err != nil {
		s.file.Tiles[k.Tile] = td
		td.Sandboxes[name] = old
		return err
	}
	return nil
}

func (s *defStore) writable(k Key) error {
	if !k.Main() {
		return errDeployment()
	}
	if s.err != nil {
		e := refuse(RefUnavailable, "%v", s.err)
		return e
	}
	return nil
}

func (s *defStore) save() error {
	s.file.Version = defsVersion
	b, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomicIn(s.path, append(b, '\n'), 0o600)
}
