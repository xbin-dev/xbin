package term

import (
	"fmt"
	"sync"
	"time"
)

// holdEnvWait bounds how long HoldEnv waits for killed sessions to let go.
var holdEnvWait = 5 * time.Second

// HoldEnv takes a component's persistent terminal layer (.xbin/term/<key>/)
// out of use so xbind can replace it — a restore swaps a rebuilt layer in
// (broker.HoldTermEnv, WP-9): every live session holding it is killed and,
// once they have torn down (their overlay unmounted), the layer is held until
// release, so a session opened meanwhile gets an ephemeral upper instead.
func (m *Manager) HoldEnv(rel string) (release func(), err error) {
	key := termKey(rel)
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
