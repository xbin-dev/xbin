package term

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/confine"
)

// holdEnvWait bounds how long HoldEnv waits for killed sessions to let go.
var holdEnvWait = 5 * time.Second

// HoldEnv takes a component's persistent terminal layer (.xbin/term/<key>/)
// out of use so xbind can replace or remove it — a restore swaps a rebuilt
// layer in (broker.HoldTermEnv, WP-9), offload-full and ResetEnv remove it
// (WP-9b): every live session holding it is killed and,
// once they have torn down (their overlay unmounted), the layer is held until
// release, so a session opened meanwhile gets an ephemeral upper instead.
func (m *Manager) HoldEnv(rel string) (release func(), err error) {
	return m.holdLayer(termKey(rel), rel)
}

// holdLayer is HoldEnv for layer key (a tile's, or a person's on a
// partitioned tile); rel names it in the refusal.
func (m *Manager) holdLayer(key, rel string) (release func(), err error) {
	killed := map[*Session]bool{}
	deadline := time.Now().Add(holdEnvWait)
	for {
		if m.acquireEnv(key) {
			var once sync.Once
			return func() { once.Do(func() { m.releaseEnv(key) }) }, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("a terminal session on %s still holds its layer", rel)
		}
		var victims []*Session
		m.mu.Lock()
		for _, s := range m.sessions {
			if s.envKey == key && !killed[s] {
				killed[s] = true
				victims = append(victims, s)
			}
		}
		m.mu.Unlock()
		for _, s := range victims {
			s.kill()
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ResetEnv wipes a component's persistent terminal layer back to the base
// rootfs: the sessions holding it are killed and the layer held (HoldEnv) —
// their overlays unmounted, none mounting it anew while it goes — then it is
// removed (removeLayer) and released. A session that won't let go fails the
// reset with the layer untouched.
func (m *Manager) ResetEnv(rel string) error {
	return m.resetLayer(termKey(rel), rel)
}

// resetLayer is ResetEnv for layer key.
func (m *Manager) resetLayer(key, rel string) error {
	release, err := m.holdLayer(key, rel)
	if err != nil {
		return err
	}
	defer release()
	return m.removeLayer(m.layerDir(key))
}

// callerLayer is the layer p's sessions on rel mount: on a partitioned
// tile a person's own (PD-22), else the tile's (termKey). A person without
// a partition id yet has no layer there: "".
func (m *Manager) callerLayer(p auth.Principal, rel string) string {
	if m.TilePartitioned == nil || p.UserID == "" {
		return termKey(rel)
	}
	tile, on := m.TilePartitioned(rel)
	if !on {
		return termKey(rel)
	}
	if m.PersonPartitionKey == nil {
		return ""
	}
	if key := m.PersonPartitionKey(p.UserID); key != "" {
		return partLayerKey(tile, key)
	}
	return ""
}

// ResetEnvFor is ResetEnv for the layer p's sessions on rel mount: p's own
// on a partitioned tile (the tile's stays), else the tile's.
func (m *Manager) ResetEnvFor(p auth.Principal, rel string) error {
	key := m.callerLayer(p, rel)
	if key == "" {
		return nil // no layer of p's there yet: nothing to reset
	}
	return m.resetLayer(key, rel)
}

// EnvStatusFor is EnvStatus for the layer p's sessions on rel mount.
func (m *Manager) EnvStatusFor(p auth.Principal, rel string) (exists, outdated bool) {
	key := m.callerLayer(p, rel)
	if key == "" {
		return false, false
	}
	return m.envStatusOf(key)
}

// removeLayer removes a terminal layer — a tree the sandbox wrote, whose
// upper holds files other sub-uids own in range mode, which xbind can't
// unlink — in a confined run with the file capabilities (confine.RemoveAll;
// WP-9b, plans/tile-sandbox-runtime.md §8.3).
func (m *Manager) removeLayer(dir string) error {
	if m.rmTree != nil {
		return m.rmTree(dir)
	}
	return confine.RemoveAll(context.Background(), dir)
}
