package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

// webTicketAuth: an Auth with ann (a user) and a device session of hers.
func webTicketAuth(t *testing.T) (*Auth, BearerSession, Principal) {
	t.Helper()
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "ann"}, "password123"); err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	bs := a.NewBearerSession("ann", "dev-1", "10.0.0.1")
	p, ok := a.bearerSessionPrincipal(bs.Token, "10.0.0.1")
	if !ok || p.Via != "device" || p.DeviceID != "dev-1" {
		t.Fatalf("device principal: %+v %v", p, ok)
	}
	return a, bs, p
}

// Only a live device-key session mints; the ticket is single use and short.
func TestWebTicketMint(t *testing.T) {
	a, bs, dev := webTicketAuth(t)
	app := a.NewBearerSession("ann", "", "")
	appP, _ := a.bearerSessionPrincipal(app.Token, "")
	cookie := a.NewSession("ann", "")
	sv, _ := a.sessionUser(cookie, "", false)
	for name, p := range map[string]Principal{
		"the app's password session": appP,
		"a browser session":          {UserID: "ann", Via: "session", Gen: sv.gen},
		"a frame of the device":      {UserID: "ann", Component: "apps/x", Via: "frame", Gen: dev.Gen},
		"a forged device principal":  {UserID: "ann", Via: "device", DeviceID: "dev-1", Gen: "s.nope"},
		"another device's id":        {UserID: "ann", Via: "device", DeviceID: "dev-2", Gen: dev.Gen},
		"the owner":                  {Owner: true, Via: "bearer"},
	} {
		if _, _, err := a.MintWebTicket(p, "/"); !errors.Is(err, ErrWebTicketNotDevice) {
			t.Errorf("%s minted a web ticket: %v", name, err)
		}
	}
	tk, exp, err := a.MintWebTicket(dev, "/c/apps/x/")
	if err != nil || time.Until(exp) > WebTicketTTL || time.Until(exp) < WebTicketTTL-5*time.Second {
		t.Fatalf("mint: %v (expires in %v)", err, time.Until(exp))
	}
	got, ok := a.ConsumeWebTicket(tk)
	if !ok || got.UserID != "ann" || got.DeviceID != "dev-1" || got.Next != "/c/apps/x/" {
		t.Fatalf("consume: %+v %v", got, ok)
	}
	if _, ok := a.ConsumeWebTicket(tk); ok {
		t.Fatal("a ticket redeemed twice")
	}
	// expiry
	tk, _, _ = a.MintWebTicket(dev, "/")
	a.TestExpireWebTickets()
	if _, ok := a.ConsumeWebTicket(tk); ok {
		t.Fatal("an expired ticket redeemed")
	}
	// A device session past its idle window mints nothing.
	a.TestAgeSession(bs.Token, 0, 13*time.Hour)
	if _, _, err := a.MintWebTicket(dev, "/"); !errors.Is(err, ErrWebTicketNotDevice) {
		t.Fatalf("an expired device session minted: %v", err)
	}
}

// Outstanding tickets per session are capped (the oldest goes) and mints
// per device are rate-limited.
func TestWebTicketLimits(t *testing.T) {
	a, _, dev := webTicketAuth(t)
	var toks []string
	for i := 0; i < maxWebTicketsPerSession+1; i++ {
		tk, _, err := a.MintWebTicket(dev, "/")
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, tk)
		time.Sleep(time.Millisecond) // distinct creation times
	}
	if _, ok := a.ConsumeWebTicket(toks[0]); ok {
		t.Fatal("the oldest ticket survived past the per-session cap")
	}
	if _, ok := a.ConsumeWebTicket(toks[len(toks)-1]); !ok {
		t.Fatal("the newest ticket was dropped")
	}
	for i := len(toks); i < webTicketRate; i++ {
		if _, _, err := a.MintWebTicket(dev, "/"); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	_, _, err := a.MintWebTicket(dev, "/")
	var rate *WebTicketRateError
	if !errors.As(err, &rate) || rate.RetryAfter <= 0 || rate.RetryAfter > webTicketWindow {
		t.Fatalf("mint past the rate: %v", err)
	}
}

// The browser session a ticket opens is bound to the device login: its login
// time and cap are the device session's (no freshness laundering for the
// enrollment step-up), and it ends with the device, the app's sign-out and
// sign-out-everywhere.
func TestWebSessionBinding(t *testing.T) {
	a, bs, dev := webTicketAuth(t)
	a.TestAgeSession(bs.Token, EnrollFreshLogin+time.Minute, 0)
	open := func(notAfter time.Time) string {
		t.Helper()
		tk, _, err := a.MintWebTicket(dev, "/")
		if err != nil {
			t.Fatal(err)
		}
		wt, ok := a.ConsumeWebTicket(tk)
		if !ok {
			t.Fatal("consume")
		}
		sid, err := a.OpenWebSession(wt, "10.0.0.2", notAfter)
		if err != nil {
			t.Fatal(err)
		}
		return sid
	}
	sid := open(time.Time{})
	a.mu.RLock()
	s, parent := a.sessions[sid], a.sessions[bs.Token]
	a.mu.RUnlock()
	if s == nil || s.bearer || s.userID != "ann" || s.deviceID != "dev-1" || !s.created.Equal(parent.created) || s.via() != "session" {
		t.Fatalf("web session: %+v (parent created %v)", s, parent.created)
	}
	sv, ok := a.sessionUser(sid, "", false)
	if !ok {
		t.Fatal("the web session doesn't resolve as a cookie")
	}
	if _, ok := a.sessionUser(sid, "", true); ok {
		t.Fatal("the web session resolves as a bearer")
	}
	if at, ok := a.LoginTime(Principal{UserID: "ann", Gen: sv.gen}); !ok || time.Since(at) < EnrollFreshLogin {
		t.Fatalf("the handoff laundered the login time: %v %v", at, ok)
	}
	// A cap: the earlier of the device session's and the caller's.
	capped := open(time.Now().Add(time.Hour))
	a.mu.RLock()
	na := a.sessions[capped].notAfter
	a.mu.RUnlock()
	if na.IsZero() || time.Until(na) > time.Hour {
		t.Fatalf("notAfter not applied: %v", na)
	}

	// Signing the app out ends the browser sessions it opened.
	if _, _, ok := a.DropBearerSession(bs.Token); !ok {
		t.Fatal("drop")
	}
	if _, ok := a.sessionUser(sid, "", false); ok {
		t.Fatal("the web session outlived the app's sign-out")
	}
	// A ticket minted before the sign-out can't open one after it.
	bs2 := a.NewBearerSession("ann", "dev-1", "")
	dev2, _ := a.bearerSessionPrincipal(bs2.Token, "")
	tk, _, _ := a.MintWebTicket(dev2, "/")
	wt, _ := a.ConsumeWebTicket(tk)
	a.DropBearerSession(bs2.Token)
	if _, err := a.OpenWebSession(wt, "", time.Time{}); err == nil {
		t.Fatal("a ticket opened a session after its device session ended")
	}
	// Removing the device ends them; so does sign-out-everywhere, which also
	// voids pending tickets.
	bs3 := a.NewBearerSession("ann", "dev-1", "")
	dev3, _ := a.bearerSessionPrincipal(bs3.Token, "")
	tk, _, _ = a.MintWebTicket(dev3, "/")
	wt, _ = a.ConsumeWebTicket(tk)
	sid, _ = a.OpenWebSession(wt, "", time.Time{})
	a.DropDeviceSessions("dev-1")
	if _, ok := a.sessionUser(sid, "", false); ok {
		t.Fatal("the web session outlived its device")
	}
	bs4 := a.NewBearerSession("ann", "dev-1", "")
	dev4, _ := a.bearerSessionPrincipal(bs4.Token, "")
	tk, _, _ = a.MintWebTicket(dev4, "/")
	a.DropUserSessions("ann")
	if _, ok := a.ConsumeWebTicket(tk); ok {
		t.Fatal("sign-out-everywhere left a web ticket redeemable")
	}
}
