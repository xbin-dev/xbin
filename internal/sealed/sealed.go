// Package sealed is the sealed-component facility (spike S1 of the
// workspace agent, issue #51): components whose code is xbind's own.
//
// A sealed component is declared by a compiled-in table (Table) — a reserved
// path under xbin/, an embedded source tree, a partition mode — and by
// nothing a workspace holds. Its code is never copied into the workspace
// tree: at boot the running binary renders its embedded tree (the same
// transforms a template instance gets: go.mod.tile → go.mod, the authored
// path rewritten to the reserved one, the template block stripped) and
// materializes it read-only under .xbin/sealed/<name>/<digest>, xbind's own
// data that no sandbox writes. The registry registers it from there
// (registry.SealedCode). The code is therefore always the running release's:
// a new binary renders a new tree with a new digest, and the old one is
// removed at that boot.
package sealed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// Spec is one row of the compiled-in table.
type Spec struct {
	// Name is the component's name: its path is xbin/<Name>. Lowercase
	// letters, digits and "-"; never a name xbind's own identities or API
	// use beneath xbin/ (reservedNames).
	Name string
	// Source is the component's embedded tree, rooted at the component.
	Source fs.FS
	// Authored is the path the source calls itself by ("apps/agent" for the
	// agent template): its text files are rewritten to the reserved path,
	// exactly as an instance's are (builtins.RenderTree). "" = no rewrite.
	Authored string
	// Template says the source is a template blueprint: its "template"
	// block is stripped, as for an instance.
	Template bool
	// Partition is the table's partition mode: true keeps each person's data
	// apart (the template's own "partition" request is kept), false makes
	// one unpartitioned instance.
	Partition bool
}

// Table is the compiled-in table of the running xbind. Empty in the spike:
// the facility ships no component yet, so no workspace sees a change.
func Table() []Spec { return nil }

// reservedNames are names under xbin/ that are already something else:
// xbind's synthetic caller identities (util.ReservedTop's note) and the
// first segments of its own /api/xbin/ routes that a sealed component's
// API prefix would shadow (the server refuses the rest at boot,
// Server.SealedAPIClash).
var reservedNames = map[string]bool{"cron": true, "bus": true, "mail": true}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// Component is a rendered sealed component.
type Component struct {
	Spec
	Path   string            // xbin/<Name>
	Files  map[string][]byte // its tree as installed: slash-separated rel path → bytes
	Digest string            // sha256 over the rendered tree, hex
	Dir    string            // where Materialize put it ("" before)
}

// Set is the sealed components of one xbind.
type Set struct {
	comps []*Component
}

// Load renders every spec. An invalid table is a programming error of the
// release, reported at boot.
func Load(specs []Spec) (*Set, error) {
	s := &Set{}
	seen := map[string]bool{}
	for _, sp := range specs {
		if !nameRe.MatchString(sp.Name) || reservedNames[sp.Name] || seen[sp.Name] {
			return nil, fmt.Errorf("sealed component %q: invalid, reserved or repeated name", sp.Name)
		}
		seen[sp.Name] = true
		p := util.SealedPrefix + sp.Name
		var tpl *builtins.InstanceOpts
		if sp.Template {
			tpl = &builtins.InstanceOpts{Partition: sp.Partition}
		}
		files, err := builtins.RenderTree(sp.Source, ".", p, sp.Authored, tpl)
		if err != nil {
			return nil, fmt.Errorf("sealed component %s: %w", p, err)
		}
		if _, ok := files["xbin.json"]; !ok {
			return nil, fmt.Errorf("sealed component %s: its source has no xbin.json", p)
		}
		s.comps = append(s.comps, &Component{Spec: sp, Path: p, Files: files, Digest: digest(files)})
	}
	return s, nil
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
		dir := filepath.Join(base, c.Digest[:24])
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
// after Materialize.
func (s *Set) Registry() []registry.SealedCode {
	out := make([]registry.SealedCode, 0, len(s.comps))
	for _, c := range s.comps {
		out = append(out, registry.SealedCode{Path: c.Path, Dir: c.Dir, Manifest: c.Files["xbin.json"], Scope: c.Files["scope.json"]})
	}
	return out
}

// Components lists the set, in table order.
func (s *Set) Components() []*Component { return s.comps }

// Names lists the components' names (the first path segment after xbin/).
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.comps))
	for _, c := range s.comps {
		out = append(out, c.Name)
	}
	return out
}
