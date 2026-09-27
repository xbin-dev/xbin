package tilesbx

// sweep.go — what a restart leaves, and the boot that clears it
// (plans/tile-sandbox-runtime.md §7 "An xbind restart or crash"). xbind's
// death kills every tile sandbox (the factory's EOF), so after a boot every
// sandbox is stopped — nothing of the old runs is kept in memory — and an
// exec id of the old boot answers lost. At boot the tile sandboxes' cgroup
// parent is swept (every sbx- leaf a previous xbind left is killed and
// removed, nothing beside it), every staging dir (tmp/) goes to .trash, and
// .trash is queued for the confined remover again. The per-sandbox lock
// (state.go) keeps a new start from mounting an upper an orphan still has
// mounted; the orphan's leaf, still populated, is waited for the same way.

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/layers"
)

// bootSweep is the boot's share, under --isolate.
func (m *Manager) bootSweep() {
	if m.cg != nil {
		if swept, err := m.cg.Sweep("sbx-"); err != nil {
			slog.Warn("tile sandboxes: clearing a previous xbind's leaves", "err", err)
		} else if len(swept) > 0 {
			slog.Info("tile sandboxes: cleared a previous xbind's leaves", "leaves", swept)
		}
	}
	m.requeueTrash()
}

// newBootID is this start's exec-id prefix: 6 random hex characters.
func newBootID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("tilesbx: no randomness for the boot id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// execLost is the answer to an exec id of another boot: its exec ended
// with the xbind that ran it (410 lost). nil for an id of this boot.
func (m *Manager) execLost(id string) error {
	if id == "" || strings.HasPrefix(id, m.bootID+"-") {
		return nil
	}
	return refuse(RefLost, "exec %s ran before xbind restarted: it is lost (its sandbox stopped with it)", id)
}

// requeueTrash is the boot's share: every .trash entry of every key is
// queued again, and every sandbox's staging dir (tmp/<rand>) goes to its
// key's .trash first (a copy a restart cut short).
func (m *Manager) requeueTrash() {
	root := filepath.Join(m.root, ".xbin", "sbx")
	keys, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var queue []string
	for _, kd := range keys {
		if !kd.IsDir() {
			continue
		}
		base := filepath.Join(root, kd.Name())
		trash := filepath.Join(base, ".trash")
		sbs, _ := os.ReadDir(base)
		for _, sd := range sbs {
			if !sd.IsDir() || sd.Name() == ".trash" {
				continue
			}
			tmp := filepath.Join(base, sd.Name(), "tmp")
			ents, _ := os.ReadDir(tmp)
			for _, e := range ents {
				_, uid, ok := layers.SplitStateDir(sd.Name())
				if !ok {
					continue
				}
				if err := os.MkdirAll(trash, 0o700); err != nil {
					continue
				}
				_ = os.Rename(filepath.Join(tmp, e.Name()), filepath.Join(trash, uid+"."+randSuffix()))
			}
		}
		ents, _ := os.ReadDir(trash)
		for _, e := range ents {
			queue = append(queue, filepath.Join(trash, e.Name()))
		}
	}
	if len(queue) > 0 {
		slog.Info("tile sandboxes: removing state put aside before the restart", "entries", len(queue))
	}
	m.trash.put(queue...)
}
