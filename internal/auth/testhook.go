package auth

import "time"

// TestAgeWarmIP back-dates a warm-IP entry so tests (including other
// packages' handler tests) can exercise TTL expiry without sleeping.
// Test-only — never used by the daemon.
func (a *Auth) TestAgeWarmIP(ip string, age time.Duration) {
	a.mu.Lock()
	a.warm[ip] = time.Now().Add(-age)
	a.mu.Unlock()
}
