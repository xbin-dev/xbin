package builtins

// series.go — a builtin update rendered as a git patch series (Propose),
// for filing through the cross-tile PR flow: what it reads of the tile
// (never a link, a FIFO or a nested tile's file) and the series itself.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/confine"
)

// Proposal is a builtin update rendered as a git patch series, for filing
// through the cross-tile PR flow (update mode "pr" — where builtin-updates
// meets plans/code-prs.md). The series is base→theirs for tracked units and
// ours→theirs for adopted ones (no trusted base), so `git am --3way` in the
// tile's own terminal does the merge with real git machinery instead of
// conflict markers dropped into live files. Provenance is refreshed only
// when the PR closes merged (RecordApplied) — after resolution, not before.
type Proposal struct {
	ID          string
	InstallPath string
	Title       string
	Message     string
	Series      string // format-patch mbox; "" when upstream changed nothing but a partition (Notes say so)
	ToVersion   int
	ToHash      string // rollup of theirs; RecordApplied verifies the embed hasn't moved
	Notes       []string
}

// Propose renders a unit's pending update as a Proposal. Errors when the
// unit is unknown or already up to date.
func (u *Updater) Propose(id string) (*Proposal, error) {
	def, ok := u.defByID(id)
	if !ok {
		return nil, noSuchUnit(id)
	}
	o := u.load()
	state := o.Units[id]
	installPath := def.DefaultPath
	if state != nil {
		installPath = state.InstallPath
	}
	uu, err := u.compare(def, installPath, state)
	if err != nil {
		return nil, err
	}
	if !uu.HasUpdate {
		return nil, fmt.Errorf("%s is up to date — nothing to propose", id)
	}
	theirs, err := u.render(def, installPath)
	if err != nil {
		return nil, err
	}

	// The "from" side of the diff: the recorded base snapshot, or — adopted
	// units, no trusted base — the installed files as they are now.
	base := map[string][]byte{}
	if state != nil && !state.Adopted {
		snap := u.snapDir(id)
		for rel := range state.Files {
			if data, err := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel))); err == nil { // walk-ok: the base snapshot is xbind's own (.xbin/builtins)
				base[rel] = data
			}
		}
	} else {
		// Each file as it is at its path: a link, a FIFO, a file too large
		// is not read (its status says so) — the series, which the tile's
		// readers fetch, holds only what is there.
		for _, fs := range uu.Files {
			if fs.Note != "" {
				continue // a link, a FIFO, a nested tile's file: never read, never in the series
			}
			if tf := readTile(u.root, installPath, fs.Path); tf.present {
				base[fs.Path] = tf.data
			}
		}
	}

	title := fmt.Sprintf("builtin update: %s v%d→v%d", def.Name, uu.FromVersion, uu.ToVersion)
	if uu.FromVersion == 0 && state == nil {
		title = fmt.Sprintf("builtin update: %s v%d (adopted — no recorded base)", def.Name, uu.ToVersion)
	}
	var msg strings.Builder
	if def.Changelog != "" {
		msg.WriteString(def.Changelog + "\n\n")
	}
	msg.WriteString("File status (base / yours / upstream):\n")
	var unread []string
	for _, fs := range uu.Files {
		if fs.Status == stUpToDate {
			continue
		}
		fmt.Fprintf(&msg, "  %-9s %s\n", fs.Status, fs.Path)
		if fs.Note != "" {
			unread = append(unread, fs.Note)
		}
	}
	if len(unread) > 0 {
		msg.WriteString("\nNot read, and not in this series (each stays offered as an update):\n  " + strings.Join(unread, "\n  ") + "\n")
	}
	msg.WriteString("\nApply in this tile's terminal (clone-first keeps markers out of live files):\n" +
		"  git clone \"$XBIN_WORKSPACE/" + installPath + "\" /tmp/upd && cd /tmp/upd\n" +
		"  bx code pr fetch <n> | git am --3way    # resolve, build, test\n" +
		"  git -C \"$XBIN_WORKSPACE/" + installPath + "\" pull /tmp/upd && bx code pr close <n> --merged\n" +
		"Closing merged (a person's call, never the tile's own code) records the files that then hold\n" +
		"the update; it stays offered for the rest, as rejecting keeps it offered.")

	to, notes := u.proposalPartition(installPath, base, theirs) // the series never touches "partition"
	msg.WriteString(proposalNote(notes))
	notes = append(notes, unread...)
	for _, fs := range uu.Files { // a file that carries a note is never in the series (re-review, L2)
		if fs.Note != "" {
			delete(base, fs.Path)
			delete(to, fs.Path)
		}
	}
	series := "" // stays "" when upstream changed nothing else: nothing to propose
	if !sameFiles(base, to) {
		if series, err = patchSeries(base, to, title, msg.String()); err != nil {
			return nil, err
		}
	}
	hashes := make(map[string]string, len(theirs))
	for rel, data := range theirs {
		hashes[rel] = sha(data)
	}
	return &Proposal{
		ID: id, InstallPath: installPath, Title: title, Message: msg.String(),
		Series: series, ToVersion: uu.ToVersion, ToHash: rollup(hashes), Notes: notes,
	}, nil
}

// patchSeries renders old→new as a one-commit git format-patch mbox: a temp
// repo, the old tree as a baseline commit, the new tree as the update commit
// (subject + body = the PR title + message). git computes renames/deletions;
// `git am --3way` on the receiving side gets index blob ids to merge from.
func patchSeries(from, to map[string][]byte, subject, body string) (string, error) {
	dir, err := os.MkdirTemp("", "xbin-propose-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	// confined (D78): the trees are an installed tile's files
	git := func(args ...string) (string, error) {
		full := append([]string{"-c", "user.email=builtins@xbin", "-c", "user.name=xbin builtins"}, args...)
		out, err := confine.Git(context.Background(), dir, nil, full...)
		if err != nil {
			return "", fmt.Errorf("git %v: %s", args, strings.TrimSpace(err.Error()))
		}
		return out, nil
	}
	writeTree := func(files map[string][]byte) error {
		ents, err := os.ReadDir(dir) // walk-ok: xbind's own temporary repository
		if err != nil {
			return err
		}
		for _, e := range ents {
			if e.Name() != ".git" {
				if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
					return err
				}
			}
		}
		for rel, data := range files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := git("init", "-q", "-b", "main"); err != nil {
		return "", err
	}
	if err := writeTree(from); err != nil {
		return "", err
	}
	if _, err := git("add", "-A"); err != nil {
		return "", err
	}
	if _, err := git("commit", "-q", "--allow-empty", "-m", "builtin base"); err != nil {
		return "", err
	}
	if err := writeTree(to); err != nil {
		return "", err
	}
	if _, err := git("add", "-A"); err != nil {
		return "", err
	}
	if _, err := git("commit", "-q", "-m", subject+"\n\n"+body); err != nil {
		return "", err
	}
	series, err := git("format-patch", "--stdout", "-1", "HEAD")
	if err != nil {
		return "", err
	}
	if !strings.Contains(series, "diff --git") {
		return "", fmt.Errorf("update produced an empty diff")
	}
	return series, nil
}
