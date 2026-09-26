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

// TestAgeSession back-dates a login session: its sign-in by loginAge and its
// last activity by idleAge (0 leaves one as is), so tests can reach the
// step-up and idle windows without sleeping. Test-only.
func (a *Auth) TestAgeSession(id string, loginAge, idleAge time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[id]; s != nil {
		if loginAge > 0 {
			s.created = time.Now().Add(-loginAge)
		}
		if idleAge > 0 {
			s.lastActive = time.Now().Add(-idleAge)
		}
	}
}

// TestExpireWebTickets ages every pending browser sign-in ticket and
// confirmation past its TTL (webticket.go), so tests reach expiry without
// sleeping. Test-only.
func (a *Auth) TestExpireWebTickets() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range []map[string]*webTicket{a.dev.webTickets, a.dev.webConfirms} {
		for _, t := range m {
			t.expires = time.Now().Add(-time.Second)
		}
	}
}
