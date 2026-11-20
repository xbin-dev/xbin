package delegation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A refused ticket says what differed, and spends nothing (D250): the
// owner's session lost four rounds to "the approval no longer covered the
// call" — the agent guessed a 15-minute expiry, then a brief edit.
func TestRedeemSaysWhatDiffered(t *testing.T) {
	s := OpenStore(t.TempDir())
	clock := time.Date(2026, 11, 21, 10, 47, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	start := `{"cwd":"apps/eui","kind":"agent","prompt":"Goal: a tic tac toe game"}`
	file := func(kind, method, target, tile, body string) Request {
		t.Helper()
		r, err := s.NewRequest(Request{User: "ada", Agent: "xbin/wsagent", Kind: kind, Method: method, Target: target, Tile: tile, ContentType: "application/json"}, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	approve := func(r Request) {
		t.Helper()
		if _, _, err := s.Decide("ada", r.ID, GrantOnce, "", "session"); err != nil {
			t.Fatal(err)
		}
	}
	redeem := func(r Request, method, target, tile, body string) error {
		_, err := s.Redeem("ada", r.ID, method, target, tile, "application/json", []byte(body))
		return err
	}
	expect := func(err error, reason, words string, is error) {
		t.Helper()
		var re *RedeemError
		if !errors.As(err, &re) || re.Reason != reason || !strings.Contains(re.Msg, words) || is != nil && !errors.Is(err, is) {
			t.Fatalf("want %s %q (is %v), got %v", reason, words, is, err)
		}
	}

	// a start: the agent swaps the brief after the person approved it
	r := file(KindSession, "POST", "/api/xbin/term/sessions", "apps/eui", start)
	expect(redeem(r, "POST", "/api/xbin/term/sessions", "", start), ReasonPending, "still waits for the person's decision", ErrNoTicket)
	approve(r)
	swapped := `{"cwd":"apps/eui","kind":"agent","prompt":"Goal: delete everything"}`
	expect(redeem(r, "POST", "/api/xbin/term/sessions", "", swapped), ReasonBrief, "the brief's text changed after approval", ErrTicketBound)
	expect(redeem(r, "POST", "/api/xbin/term/sessions", "", `{"cwd":"apps/other","kind":"agent","prompt":"Goal: a tic tac toe game"}`), ReasonTile, "another tile", ErrTicketBound)
	expect(redeem(r, "POST", "/api/xbin/term/sessions", "", `{"cwd":"apps/eui","kind":"agent","prompt":"Goal: a tic tac toe game","net":"internet"}`), ReasonBound, "(net)", ErrTicketBound)
	expect(redeem(r, "PUT", "/api/xbin/term/sessions", "", start), ReasonBound, "approved POST, made PUT", ErrTicketBound)
	// …and nothing of that spent it
	if err := redeem(r, "POST", "/api/xbin/term/sessions", "", start); err != nil {
		t.Fatalf("the approved start after four refusals: %v", err)
	}
	expect(redeem(r, "POST", "/api/xbin/term/sessions", "", start), ReasonUsed, "an approval runs once", ErrNoTicket)

	// a tile call: its query, its type, the tile it reaches
	c := file(KindCall, "POST", "/api/apps/eui/scores/reset?all=1", "apps/eui", `{"all":true}`)
	approve(c)
	expect(redeem(c, "POST", "/api/apps/eui/scores/reset?all=0", "apps/eui", `{"all":true}`), ReasonBound, "query changed", ErrTicketBound)
	expect(redeem(c, "POST", "/api/apps/eui/scores/reset?all=1", "apps/eui/inner", `{"all":true}`), ReasonTile, "the tile moved", ErrTicketBound)
	expect(redeem(c, "POST", "/api/apps/eui/scores/reset?all=1", "apps/eui", `{"all":false}`), ReasonBound, "the body changed after approval", ErrTicketBound)
	if _, err := s.Redeem("ada", c.ID, "POST", "/api/apps/eui/scores/reset?all=1", "apps/eui", "text/plain", []byte(`{"all":true}`)); err == nil || !strings.Contains(err.Error(), "Content-Type changed") {
		t.Fatalf("another type: %v", err)
	}

	// expiry: a pending request's 24 hours, and a ticket's 15 minutes after the approval — said apart, with when
	p := file(KindCall, "POST", "/api/apps/eui/a", "apps/eui", "")
	q := file(KindCall, "POST", "/api/apps/eui/b", "apps/eui", "")
	approve(q)
	clock = clock.Add(16 * time.Minute)
	expect(redeem(q, "POST", "/api/apps/eui/b", "apps/eui", ""), ReasonExpired, "given at 10:47 UTC, expired at 11:02 UTC", ErrNoTicket)
	clock = clock.Add(24 * time.Hour)
	expect(redeem(p, "POST", "/api/apps/eui/a", "apps/eui", ""), ReasonExpired, "after 24 hours without a decision", ErrNoTicket)

	// denied, withdrawn, voided by a pause, missing
	d := file(KindCall, "POST", "/api/apps/eui/d", "apps/eui", "")
	if _, _, err := s.Decide("ada", d.ID, "", "", "session"); err != nil {
		t.Fatal(err)
	}
	expect(redeem(d, "POST", "/api/apps/eui/d", "apps/eui", ""), ReasonDenied, "the person denied request", ErrNoTicket)
	w := file(KindCall, "POST", "/api/apps/eui/w", "apps/eui", "")
	if _, err := s.Withdraw("ada", w.ID); err != nil {
		t.Fatal(err)
	}
	expect(redeem(w, "POST", "/api/apps/eui/w", "apps/eui", ""), ReasonWithdrawn, "was withdrawn at", ErrNoTicket)
	v := file(KindCall, "POST", "/api/apps/eui/v", "apps/eui", "")
	approve(v)
	if n, _ := s.VoidTickets("ada"); n != 1 {
		t.Fatalf("voided %d", n)
	}
	expect(redeem(v, "POST", "/api/apps/eui/v", "apps/eui", ""), ReasonVoided, "voided", ErrNoTicket)
	expect(redeem(Request{ID: "nope"}, "POST", "/api/apps/eui/v", "apps/eui", ""), ReasonMissing, "no request nope", ErrNoTicket)

	// a check of the caller's refuses before the ticket is spent
	k := file(KindSession, "POST", "/api/xbin/term/sessions", "apps/eui", start)
	approve(k)
	_, err := s.RedeemCall("ada", k.ID, TicketCall{Method: "POST", Target: "/api/xbin/term/sessions", ContentType: "application/json", Body: []byte(start)},
		func(Request) *RedeemError {
			return &RedeemError{ReasonChoice, "the coding agent the person picked is no longer allowed"}
		})
	expect(err, ReasonChoice, "no longer allowed", nil)
	if errors.Is(err, ErrNoTicket) || errors.Is(err, ErrTicketBound) {
		t.Fatalf("a choice's refusal is neither of the old sentinels: %v", err)
	}
	if got, _ := s.Request("ada", k.ID); got.State != Approved {
		t.Fatalf("a refused check spent the ticket: %s", got.State)
	}
}

// A start that failed to open for a reason that isn't the agent's is
// Failed, not used up: its person tries it again, within its 24 hours, and
// it is a ticket once more (D250).
func TestFailedStartRetry(t *testing.T) {
	s := OpenStore(t.TempDir())
	clock := time.Date(2026, 11, 21, 10, 47, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	body := []byte(`{"cwd":"apps/eui","kind":"agent","prompt":"go"}`)
	r, _ := s.NewRequest(Request{User: "ada", Agent: "xbin/wsagent", Kind: KindSession, Method: "POST", Target: "/api/xbin/term/sessions", Tile: "apps/eui", ContentType: "application/json"}, body)
	if _, err := s.Retry("ada", r.ID); !errors.Is(err, ErrNotRetryable) {
		t.Fatalf("a pending request tried again: %v", err)
	}
	if _, _, err := s.Decide("ada", r.ID, GrantOnce, "", "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkFailed("ada", r.ID, "x", true); err == nil {
		t.Fatal("an approved, unused request marked failed")
	}
	if _, err := s.Redeem("ada", r.ID, "POST", "/api/xbin/term/sessions", "", "application/json", body); err != nil {
		t.Fatal(err)
	}
	f, err := s.MarkFailed("ada", r.ID, "the coding agent didn't start: exit status 1", true)
	if err != nil || f.State != Failed || !f.Failure.Retry {
		t.Fatalf("failed: %+v %v", f, err)
	}
	_, err = s.Redeem("ada", r.ID, "POST", "/api/xbin/term/sessions", "", "application/json", body)
	var re *RedeemError
	if !errors.As(err, &re) || re.Reason != ReasonFailed || !strings.Contains(re.Msg, "the person can try it again") || !strings.Contains(re.Msg, "exit status 1") {
		t.Fatalf("a failed start used again by the agent: %v", err)
	}
	again, err := s.Retry("ada", r.ID)
	if err != nil || again.State != Approved || again.Retries != 1 || !again.Expires.Equal(clock.Add(TicketTTL)) {
		t.Fatalf("Try again: %+v %v", again, err)
	}
	if _, err := s.Redeem("ada", r.ID, "POST", "/api/xbin/term/sessions", "", "application/json", body); err != nil {
		t.Fatalf("the ticket again: %v", err)
	}
	// not retryable: a refusal of the open itself, or past the request's 24 hours
	if _, err := s.MarkFailed("ada", r.ID, "refused", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry("ada", r.ID); !errors.Is(err, ErrNotRetryable) {
		t.Fatalf("a refusal tried again: %v", err)
	}
	r2, _ := s.NewRequest(Request{User: "ada", Agent: "xbin/wsagent", Kind: KindSession, Method: "POST", Target: "/api/xbin/term/sessions", Tile: "apps/eui", ContentType: "application/json"}, body)
	clock = clock.Add(23 * time.Hour) // approved late in its 24 hours, then failed
	_, _, _ = s.Decide("ada", r2.ID, GrantOnce, "", "session")
	_, _ = s.Redeem("ada", r2.ID, "POST", "/api/xbin/term/sessions", "", "application/json", body)
	_, _ = s.MarkFailed("ada", r2.ID, "the sandbox didn't start", true)
	clock = clock.Add(2 * time.Hour)
	if _, err := s.Retry("ada", r2.ID); !errors.Is(err, ErrNotRetryable) {
		t.Fatalf("tried again 25 h after it was asked: %v", err)
	}
}
