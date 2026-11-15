package sealed

// stamp.go — a sealed component's data stamp: data/sealed/<name>.json,
// xbind's own file (data/ is masked from every sandbox). It says two things
// about the data at the component's keys:
//
//   - that it is the component's. The stamp is written (claimed) the first
//     time a release that has the component boots on the workspace, and
//     only while nothing sits at its resource data key. Data found there
//     without a stamp was written by something else — a workspace scope
//     whose path maps to the same key — and holds the component
//     (HoldForeignData).
//   - which data format it is in: the highest Spec.Data of any release that
//     was admitted to start the component here (Admit raises it). A stamp
//     above the running release's format holds the component
//     (HoldNewerData): after a downgrade the older code never opens a store
//     a newer migration rewrote.
//
// The stamp is one mark for the whole component — its global instance and
// every person's partition — because a partition migrates when it next
// starts, and any of them may have.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
	"strconv"
	"strings"
)

// Stamp is a component's data stamp.
type Stamp struct {
	Data    int    `json:"data"`    // the data format (Spec.Data)
	Release string `json:"release"` // the xbind that wrote this stamp
	Digest  string `json:"digest"`  // the code that release ran
	At      string `json:"at"`      // RFC3339
	// Mode is the partition mode the data was made in (modeName): "one",
	// "user" or "user+global". Recorded when the stamp is claimed and never
	// changed: a release whose table gives the component another mode holds
	// it (HoldMode). "" in a stamp from before this was recorded: the
	// running table's mode is taken down then.
	Mode string `json:"mode,omitempty"`
}

// modeName names a row's partition mode for the stamp.
func modeName(partition []string) string {
	if len(partition) == 0 {
		return "one"
	}
	return strings.Join(partition, "+")
}

// modeWords says a mode as a hold's message does.
func modeWords(mode string) string {
	switch mode {
	case "one":
		return "one backend for everyone"
	case "user":
		return "one backend per person"
	case "user+global":
		return "one backend per person and a shared one"
	}
	return "the mode " + strconv.Quote(mode)
}

func stampPath(ws, name string) string {
	return filepath.Join(ws, "data", "sealed", name+".json")
}

// DataDirs are the directories that hold the main resource data of the
// scope a component at path roots, by its data key: what a workspace scope
// with the same key (util.ScopeKey isn't injective) would share with it.
// People's partitions and other deployments are keyed injectively
// elsewhere and can't collide.
func DataDirs(ws, path string) []string {
	key := util.ScopeKey(path)
	return []string{
		filepath.Join(ws, "data", "resources", key),
		filepath.Join(ws, "data", "resources-enc", key),
	}
}

// settleData reads c's stamp, claims it for a component new to the
// workspace, and answers the data hold, if any. Called with s.mu held.
func (s *Set) settleData(c *Component) (Hold, bool) {
	ws := s.opts.Workspace
	b, err := os.ReadFile(stampPath(ws, c.Name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		for _, d := range DataDirs(ws, c.Path) {
			if ents, _ := os.ReadDir(d); len(ents) > 0 {
				return Hold{HoldForeignData, fmt.Sprintf("is held: the workspace already holds data at %s that this system component didn't write (a tile whose path maps to the same data key, such as %s, owned it). It was not read, moved or deleted — move it away, then restart xbin",
					rel(ws, d), util.ScopeKey(c.Path))}, true
			}
		}
		if err := s.writeStamp(c, c.data()); err != nil {
			return Hold{HoldStamp, "is held: its data stamp can't be written (" + err.Error() + ")"}, true
		}
		return Hold{}, false
	case err != nil:
		return Hold{HoldStamp, "is held: its data stamp can't be read (" + err.Error() + "); nothing of its data was touched"}, true
	}
	var st Stamp
	if err := json.Unmarshal(b, &st); err != nil || st.Data < 1 {
		return Hold{HoldStamp, fmt.Sprintf("is held: its data stamp %s is damaged; nothing of its data was touched", rel(ws, stampPath(ws, c.Name)))}, true
	}
	s.stamps[c.Path] = st
	switch want := modeName(c.Partition); {
	case st.Mode == "" && st.Data > c.data():
		// a newer release's stamp without a mode: not this release's to write
	case st.Mode == "": // a stamp from before modes were recorded: this table's is taken down
		st.Mode = want
		if b, err := json.MarshalIndent(st, "", "  "); err == nil && fsutil.WriteFileAtomic(stampPath(ws, c.Name), append(b, '\n'), 0o600) == nil {
			s.stamps[c.Path] = st
		}
	case st.Mode != want:
		// the registry is given the mode the data has, so nothing asks
		// for a switch of it (Registry)
		c.dataMode = st.Mode
		return Hold{HoldMode, fmt.Sprintf("is held: its data was made with %s, and this xbin's table gives it %s. A system component's partition mode is fixed for the life of the component — switching it would delete its data — so this release can't run it: install one whose table has the mode its data was made in. Nothing was changed",
			modeWords(st.Mode), modeWords(want))}, true
	}
	if st.Data > c.data() {
		return newerData(st, c.data(), s.opts.Release), true
	}
	return Hold{}, false
}

func newerData(st Stamp, reads int, running string) Hold {
	by := st.Release
	if by == "" {
		by = "a newer xbin"
	} else {
		by = "xbin " + by
	}
	return Hold{HoldNewerData, fmt.Sprintf("is held: its data was written by %s (data format %d), and this xbin (%s) reads up to format %d — install that release or a later one. Nothing was changed",
		by, st.Data, running, reads)}
}

func rel(ws, p string) string {
	if r, err := filepath.Rel(ws, p); err == nil {
		return r
	}
	return p
}

// writeStamp records data as c's data format, written by the running
// release. Called with s.mu held.
func (s *Set) writeStamp(c *Component, data int) error {
	st := Stamp{Data: data, Release: s.opts.Release, Digest: c.Digest, At: time.Now().UTC().Format(time.RFC3339), Mode: modeName(c.Partition)}
	if old, ok := s.stamps[c.Path]; ok && old.Mode != "" {
		st.Mode = old.Mode // the mode its data was made in never changes
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	p := stampPath(s.opts.Workspace, c.Name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	s.stamps[c.Path] = st
	return nil
}

// Stamp is the data stamp of the component at path, as last read or
// written; false when it has none (a component held before it could claim
// one).
func (s *Set) Stamp(path string) (Stamp, bool) {
	if s == nil {
		return Stamp{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stamps[path]
	return st, ok
}

// DataFormat is the data format a backup of the component at path records:
// its stamp's (0 for a path that is no sealed component, or one without a
// stamp).
func (s *Set) DataFormat(path string) int {
	st, _ := s.Stamp(path)
	return st.Data
}

// RestoreRefusal says why a backup whose data is in format data (0 means
// 1), written by release, can't be restored into the component at path:
// this release's code can't read it. nil when it can, or when path is no
// sealed component. An older format needs nothing: the backend migrates
// its store forward at its next start, and the stamp stays the high-water
// mark it is.
//
// A component held for its data — foreign data at its key, a store newer
// than the code, an unreadable stamp — takes no restore either: the hold
// says the data there is not this code's to overwrite. And a path in the
// sealed namespace that is no component of this release takes none.
func (s *Set) RestoreRefusal(path string, data int, release string) error {
	c, ok := s.Component(path)
	if !ok {
		if util.SealedPath(path) {
			return fmt.Errorf("%s is no system component of this xbin release: nothing is restored there", path)
		}
		return nil
	}
	if h, held := s.Hold(path); held && h.keepsData() {
		return fmt.Errorf("%s %s — nothing is restored while its data is held", path, h.Reason)
	}
	if data <= c.data() {
		return nil
	}
	h := newerData(Stamp{Data: data, Release: release}, c.data(), s.opts.Release)
	return fmt.Errorf("this backup of %s can't be restored here: %s", path, h.Reason[len("is held: "):])
}
