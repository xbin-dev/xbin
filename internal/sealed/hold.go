package sealed

// hold.go — why a sealed component can't run. A hold is decided at boot
// (Open) and read by the runner's gates on every start, of the component
// and of each person's partition of it; the text is what the caller of its
// API, the system page and `bx` show. A held component is never started,
// and for the two data holds nothing of its data is provisioned or mounted.

import (
	"cmp"
	"fmt"
	"log/slog"
	"path/filepath"
)

// Hold codes.
const (
	// HoldForeignData: data sits at the component's resource data key that
	// it never claimed — a workspace scope held the same key before
	// (xbin~<name> beside xbin/<name>). Never read, moved or deleted.
	HoldForeignData = "foreign-data"
	// HoldNewerData: its data was written by a release whose data format
	// this release's code can't read (a downgrade, or a restore of a newer
	// backup's stamp). Left as it is.
	HoldNewerData = "newer-data"
	// HoldStamp: its data stamp can't be read or written, so neither of the
	// two above can be ruled out.
	HoldStamp = "stamp"
	// HoldNoBuild: this install has no build of its backend, and only a
	// development xbind compiles one.
	HoldNoBuild = "no-build"
	// HoldNeedsSandboxes: it keeps each person's data apart, and this xbind
	// runs backends without sandboxes.
	HoldNeedsSandboxes = "needs-sandboxes"
)

// Hold is why a component can't run: a code for programs, a reason for
// people. The reason completes "component xbin/<name> …".
type Hold struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// keepsData reports the holds under which the component's data must not be
// touched at all: its scope then declares no resources.
func (h Hold) keepsData() bool {
	return h.Code == HoldForeignData || h.Code == HoldNewerData || h.Code == HoldStamp
}

// Hold answers why the component at path can't run; false when it can, or
// when path is no sealed component.
func (s *Set) Hold(path string) (Hold, bool) {
	if s == nil {
		return Hold{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.holds[path]
	return h, ok
}

// Admit is the gate of a start of the component at path, or of a person's
// partition of it: the hold's reason, or "" when it may start — and then
// its data stamp says this release's data format from now on, since this
// release's code is about to open its stores (stamp.go). "" for a path that
// is no sealed component.
func (s *Set) Admit(path string) string {
	c, ok := s.Component(path)
	if !ok {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, held := s.holds[path]; held {
		return h.Reason
	}
	if h, bad := s.verify(c); bad {
		s.holds[path] = h
		return h.Reason
	}
	if st := s.stamps[path]; st.Data < c.data() {
		if err := s.writeStamp(c, c.data()); err != nil {
			h := Hold{HoldStamp, "is held: its data stamp can't be written (" + err.Error() + "), so a later release couldn't tell what wrote its data"}
			s.holds[path] = h
			return h.Reason
		}
	}
	return ""
}

// verify is run before every start of c (Admit): what is about to run is
// the release's. The tree its backend runs in is compared with the
// release's bytes, file by file, and written again when it differs (the
// copy under .xbin/sealed/ is xbind's own, never mounted into a sandbox;
// what a browser is sent never comes from it at all, server/sealedfile.go).
// The prebuilt backend's hash is compared with the install's manifest: a
// file that is no longer the one the release shipped is not run, and the
// component is held until xbind restarts on a sound install.
func (s *Set) verify(c *Component) (Hold, bool) {
	if c.Dir != "" && !intact(c.Dir, c.Files) {
		slog.Warn("sealed: a system component's files on disk were not the release's: rewritten", "component", c.Path, "dir", c.Dir)
		if err := writeTree(filepath.Dir(c.Dir), c.Dir, c.Files); err != nil {
			return Hold{HoldNoBuild, "is held: its files under .xbin/sealed were changed and can't be written again (" + err.Error() + ")"}, true
		}
	}
	if c.Prebuilt != "" && !(s.opts.Dev && c.prebuiltSum == "") {
		if sum, _, err := FileSum(c.Prebuilt); err != nil || sum != c.prebuiltSum {
			return Hold{HoldNoBuild, fmt.Sprintf("is held: its build %s is no longer the file this xbin install shipped (its SHA-256 differs from %s) — reinstall the release bundle", c.Prebuilt, ManifestName)}, true
		}
	}
	return Hold{}, false
}

// settle decides c's hold at boot: its data first (the stamp, claimed when
// the component is new to this workspace), then what this xbind can run.
func (s *Set) settle(c *Component) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.holds, c.Path)
	if h, held := s.settleData(c); held {
		s.holds[c.Path] = h
		return
	}
	switch {
	case c.Partitioned && !s.opts.Isolate:
		s.holds[c.Path] = Hold{HoldNeedsSandboxes, "is held: it keeps each person's data apart, which needs sandboxes — run xbind with --isolate"}
	case c.Runtime == "go" && c.Prebuilt == "" && !s.opts.Dev:
		want := c.Name + "-" + c.ID()
		if s.opts.BinDir != "" {
			want = filepath.Join(s.opts.BinDir, want)
		}
		why := cmp.Or(c.noBuild, "is missing")
		s.holds[c.Path] = Hold{HoldNoBuild, fmt.Sprintf("is held: this xbin install has no build of it (%s %s) — reinstall the release bundle; xbin never compiles a system component on the host", want, why)}
	}
}
