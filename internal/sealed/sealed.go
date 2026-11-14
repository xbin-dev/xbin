// Package sealed is the sealed-component facility (D227): components whose
// code is always the running xbind release's.
//
// A sealed component is declared by a compiled-in table (Table) — a
// reserved path under xbin/, an embedded source tree, a partition mode, the
// interface slots an admin may bind, its capabilities — and by nothing a
// workspace holds. Its code is never copied into the workspace tree: at
// boot the running binary renders its embedded tree (the transforms a
// template instance gets: go.mod.tile → go.mod, the authored path rewritten
// to the reserved one, the template block stripped), hashes it, and
// materializes it read-only under .xbin/sealed/<name>/<digest>, xbind's own
// data that no sandbox writes. The registry registers it from there
// (registry.SealedCode). A new binary renders a new tree with a new digest,
// and the old one is removed at that boot.
//
// Its backend is the release's own build, shipped beside xbind as
// bin/sealed/<name>-<digest> (Build, `xbind sealed-build`); only a
// development xbind compiles it from the materialized source. A component
// that can't run says why (Hold): no build of it in this install, data at
// its key it didn't write, data newer than its code (stamp.go), or people's
// partitions without sandboxes.
//
// "Sealed" here has nothing to do with the vault's sealed state; what
// people see is called a system component.
package sealed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// Spec is one row of the compiled-in table.
type Spec struct {
	// Name is the component's name: its path is xbin/<Name>. Lowercase
	// letters, digits and "-"; never a name something else under xbin/
	// already has (reservedNames, and the route check at boot:
	// server.SealedAPIClash).
	Name string
	// Title is what people see it called ("" = its manifest's title).
	Title string
	// Source is the component's embedded tree, rooted at the component.
	Source fs.FS
	// Authored is the path the source calls itself by ("apps/agent" for the
	// agent template): its text files are rewritten to the reserved path,
	// exactly as an instance's are (builtins.RenderTree). "" = no rewrite.
	Authored string
	// Template says the source is a template blueprint: its "template"
	// block is stripped, as for an instance.
	Template bool
	// Partition is the component's partition mode, the table's alone:
	// nil for one instance, {"user"} or {"user", "global"} for one backend
	// per person (docs/partitions.md). The source's own "partition" request
	// is never read. A released row's mode never changes: switching the
	// mode of a component that holds data deletes it.
	Partition []string
	// Slots names the interface slots of the source's manifest that the
	// component has: the ones an admin may bind, outward. A slot the
	// manifest declares and the table doesn't name does not exist for it.
	// A sealed component is never a target: whatever roles, provided
	// interfaces or public endpoints its source declares are dropped
	// (registry.addSealed), so no tile is granted a call into it and no
	// slot binds to it.
	Slots []string
	// Defaults names, for some of Slots, the provider a slot is bound to
	// while no admin has bound it: slot → a builtin provider's ref
	// ("xbin:llm", never a tile of the workspace — the table knows none).
	// It applies while that provider is available (the LLM connector:
	// configured); an admin's binding of the slot, or their unbinding it,
	// always wins and is never overwritten (registry.sealedView).
	Defaults map[string]string
	// Grants are the cross-scope grants the release gives it (its own
	// scope's resources need none). No admin adds to or removes from them.
	Grants []registry.Use
	// Capabilities are what the release lets it do beyond a tile.
	Capabilities Capabilities
	// Data is the component's data format: a number a release raises when
	// its migrations leave a store its predecessor can't read (stamp.go).
	// 0 means 1.
	Data int
}

// Capabilities are a sealed component's powers, the table's alone: nothing
// in a workspace grants, approves or widens one.
type Capabilities struct {
	// Delegate lets the component act for the person whose partition it is
	// (the delegation primitive reads it through Lookup; nothing in this
	// package does).
	Delegate bool
}

// Lookup answers the compiled-in table's row for the sealed component at
// path. A booted xbind asks its Set (which a test's table replaces):
// (*Set).Lookup.
func Lookup(path string) (Spec, bool) {
	for _, sp := range Table() {
		if util.SealedPrefix+sp.Name == path {
			return sp, true
		}
	}
	return Spec{}, false
}

// Lookup answers the row of the set's component at path.
func (s *Set) Lookup(path string) (Spec, bool) {
	if c, ok := s.Component(path); ok {
		return c.Spec, true
	}
	return Spec{}, false
}

// Table is the compiled-in table of the running xbind (table.go). Empty:
// the facility ships no component yet, so no workspace sees a change.
func Table() []Spec { return table() }

// reservedNames are names under xbin/ that are already something else:
// xbind's synthetic caller identities (util.ReservedTop's note; the
// partition-mail doorbell's). The first segments of xbind's own /api/xbin/
// routes are refused where the routes are known (server.SealedAPIClash).
var reservedNames = map[string]bool{"cron": true, "bus": true, "mail": true}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// Options is what a boot knows that decides how the table's components run.
type Options struct {
	Workspace string // the workspace root
	// Isolate says backends run in sandboxes (--isolate): people's
	// partitions need them.
	Isolate bool
	// Dev says this is a development xbind (--dev or --no-auth): it
	// compiles a component whose build the install lacks, and runs a
	// partitioned component as one instance when it has no sandboxes.
	Dev bool
	// BinDir is the release's directory of prebuilt backends (bin/sealed
	// beside xbind); "" = none.
	BinDir string
	// Release is the running xbind's version, recorded in the stamp.
	Release string
}

// Component is a sealed component of the running xbind.
type Component struct {
	Spec
	Path    string            // xbin/<Name>
	Files   map[string][]byte // its tree as installed: slash-separated rel path → bytes
	Digest  string            // sha256 over the rendered tree, hex
	Dir     string            // its materialized tree ("" before Open materializes it)
	Runtime string            // its manifest's runtime ("" = static)
	// Prebuilt is the release's build of its backend ("" = none in this
	// install).
	Prebuilt string
	// Partitioned says it runs one backend per person in this boot: the
	// table's mode, unless a development xbind without sandboxes runs it as
	// one instance.
	Partitioned bool
}

// ID is the short digest that names its tree and its prebuilt backend.
func (c *Component) ID() string { return c.Digest[:24] }

// data is the row's data format (0 means 1).
func (c *Component) data() int { return max(c.Data, 1) }

// Set is the sealed components of one xbind.
type Set struct {
	opts  Options
	comps []*Component

	mu     sync.Mutex
	holds  map[string]Hold  // path → why it can't run (hold.go)
	stamps map[string]Stamp // path → its data stamp as last read or written (stamp.go)
}

// Load renders every spec, without touching a workspace. An invalid table
// is a programming error of the release, reported at boot.
func Load(specs []Spec) (*Set, error) {
	s := &Set{holds: map[string]Hold{}, stamps: map[string]Stamp{}}
	seen := map[string]bool{}
	for _, sp := range specs {
		if !nameRe.MatchString(sp.Name) || reservedNames[sp.Name] || seen[sp.Name] {
			return nil, fmt.Errorf("sealed component %q: invalid, reserved or repeated name", sp.Name)
		}
		seen[sp.Name] = true
		p := util.SealedPrefix + sp.Name
		if _, err := registry.ParsePartition(registry.PartitionList(sp.Partition)); err != nil {
			return nil, fmt.Errorf("sealed component %s: partition: %w", p, err)
		}
		var tpl *builtins.InstanceOpts
		if sp.Template {
			tpl = &builtins.InstanceOpts{} // its partition request is the table's, never the template's
		}
		files, err := builtins.RenderTree(sp.Source, ".", p, sp.Authored, tpl)
		if err != nil {
			return nil, fmt.Errorf("sealed component %s: %w", p, err)
		}
		var man registry.Manifest
		b, ok := files["xbin.json"]
		if !ok {
			return nil, fmt.Errorf("sealed component %s: its source has no xbin.json", p)
		}
		if err := jsonc.Unmarshal(b, &man); err != nil {
			return nil, fmt.Errorf("sealed component %s: xbin.json: %w", p, err)
		}
		for _, slot := range sp.Slots {
			if _, ok := man.Interfaces[slot]; !ok {
				return nil, fmt.Errorf("sealed component %s: the table names slot %q, which its manifest doesn't declare", p, slot)
			}
		}
		for slot, ref := range sp.Defaults {
			if !slices.Contains(sp.Slots, slot) {
				return nil, fmt.Errorf("sealed component %s: a default binding for slot %q, which the table doesn't give it", p, slot)
			}
			if ref == "" || strings.Contains(ref, "/") {
				return nil, fmt.Errorf("sealed component %s: slot %q defaults to %q — a default is a builtin provider's ref, never a tile's path", p, slot, ref)
			}
		}
		for _, g := range sp.Grants {
			if g.Target == "" || g.Role == "" {
				return nil, fmt.Errorf("sealed component %s: a table grant needs a target and a role", p)
			}
		}
		s.comps = append(s.comps, &Component{Spec: sp, Path: p, Files: files, Digest: digest(files), Runtime: man.Runtime})
	}
	return s, nil
}

// Open is Load for a booting xbind: the trees are materialized in the
// workspace, the install's prebuilt backends found, the data stamps read
// and claimed, and each component's hold decided (hold.go).
func Open(specs []Spec, o Options) (*Set, error) {
	s, err := Load(specs)
	if err != nil {
		return nil, err
	}
	s.opts = o
	if err := s.Materialize(o.Workspace); err != nil {
		return nil, err
	}
	for _, c := range s.comps {
		c.Partitioned = len(c.Partition) > 0 && (o.Isolate || !o.Dev)
		if o.BinDir != "" {
			if p := filepath.Join(o.BinDir, BuildName(c)); executable(p) {
				c.Prebuilt = p
			}
		}
		s.settle(c)
	}
	return s, nil
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// digest hashes a rendered tree: each file's path and bytes, in path order.
func digest(files map[string][]byte) string {
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	h := sha256.New()
	for _, rel := range rels {
		fmt.Fprintf(h, "%d:%s\n%d:", len(rel), rel, len(files[rel]))
		h.Write(files[rel])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Dir is where component name's trees live in workspace ws.
func Dir(ws, name string) string { return filepath.Join(ws, ".xbin", "sealed", name) }

// Materialize writes every component's tree to <ws>/.xbin/sealed/<name>/
// <digest> and removes the trees of other digests (an earlier release's).
// A tree already there is checked file by file against the rendered one and
// rewritten when it differs, so what is served and run is the running
// binary's code even after the directory was tampered with or half-written.
func (s *Set) Materialize(ws string) error {
	for _, c := range s.comps {
		base := Dir(ws, c.Name)
		dir := filepath.Join(base, c.ID())
		if !intact(dir, c.Files) {
			if err := writeTree(base, dir, c.Files); err != nil {
				return fmt.Errorf("sealed component %s: %w", c.Path, err)
			}
		}
		c.Dir = dir
		ents, _ := os.ReadDir(base)
		for _, e := range ents {
			if old := filepath.Join(base, e.Name()); old != dir {
				_ = os.RemoveAll(old) // an earlier release's tree, or a stale temp dir
			}
		}
	}
	return nil
}

// intact reports whether dir holds exactly files.
func intact(dir string, files map[string][]byte) bool {
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		want, ok := files[filepath.ToSlash(rel)]
		if !ok || !d.Type().IsRegular() {
			return fs.ErrInvalid
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, want) {
			return fs.ErrInvalid
		}
		n++
		return nil
	})
	return err == nil && n == len(files)
}

// writeTree writes files to a temp dir beside dir and renames it into place.
func writeTree(base, dir string, files map[string][]byte) error {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(base, ".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for rel, b := range files {
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o444); err != nil {
			return err
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

// Registry is the set as the registry registers it (registry.Sealed); call
// after Materialize. A component held for the data at its key (hold.go)
// declares no resources, so nothing of that data is provisioned, mounted,
// backed up or removed.
func (s *Set) Registry() []registry.SealedCode {
	out := make([]registry.SealedCode, 0, len(s.comps))
	for _, c := range s.comps {
		sc := registry.SealedCode{
			Path: c.Path, Dir: c.Dir, Title: c.Title,
			Manifest: c.Files["xbin.json"], Scope: c.Files["scope.json"],
			Slots: slices.Clone(c.Slots), Grants: slices.Clone(c.Grants), Defaults: maps.Clone(c.Defaults),
		}
		if c.Partitioned {
			sc.Partition = slices.Clone(c.Partition)
		}
		if h, held := s.Hold(c.Path); held && h.keepsData() {
			sc.NoData = "system component " + c.Path + " " + h.Reason
		}
		out = append(out, sc)
	}
	return out
}

// Components lists the set, in table order.
func (s *Set) Components() []*Component {
	if s == nil {
		return nil
	}
	return s.comps
}

// Component is the set's component at path.
func (s *Set) Component(path string) (*Component, bool) {
	if s == nil || !util.SealedPath(path) {
		return nil, false
	}
	for _, c := range s.comps {
		if c.Path == path {
			return c, true
		}
	}
	return nil, false
}
