package builtins

// series.go — a builtin update rendered as a git patch series (Propose),
// for filing through the cross-tile PR flow.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/confine"
)

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
