package server

// delegateaccounts.go — POST /delegate/accounts/lookup (D246;
// plans/wsagent/access-and-status.md §1 "The lookup"): whether an account
// of a name exists, for a person's agent that is about to propose a line of
// that person's access — and nothing else.
//
// The smallest thing that works without making the agent a directory:
//
//   - only a person who already sees every account's id and name in xbin
//     (a workspace admin: GET /users; an org admin: GET /users-directory)
//     gets an answer. For anyone else every name answers alike ("unseen"):
//     the agent proposes the line with the name as given, and an admin's
//     review resolves it — so the lookup tells them nothing a person who
//     sees no accounts couldn't see;
//   - exact: the account whose id the name is, else the ones whose display
//     name equals it (case and the space around it aside). Never a prefix,
//     never a list: two by display name is "ask your person which";
//   - an act of the gate (counted in its 30 a minute, a row in the person's
//     log — the path, never the name), and a share of its own per
//     conversation (lookupShare).

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// AccountResolver resolves a name as viewer may see it (the broker's
// ResolveAccount).
type AccountResolver interface {
	ResolveAccount(viewer auth.Principal, name string) delegation.AccountMatch
}

// A conversation's share of lookups: a few at once, then one every 6 s.
const (
	lookupShareBurst  = 10
	lookupShareRefill = 6 * time.Second
)

// lookupUnseen is the one answer a person who sees no accounts gets.
const lookupUnseen = "your person can't see the workspace's accounts, so xbin says nothing of any name: propose the line with the name as they gave it — an admin's review resolves it, and shows them who it is"

func (s *Server) registerAccountLookup() {
	s.RegisterAPI("POST /delegate/accounts/lookup", s.apiAccountLookup)
}

// allowLookup takes one lookup from the conversation's share.
func (d *delegateRuntime) allowLookup(user, conv string) (ok bool, wait time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.net == nil {
		d.net = map[string]*actBucket{}
	}
	key := "lookup\x00" + user + "\x00" + conv
	share := d.net[key]
	if share == nil {
		share = &actBucket{}
		d.net[key] = share
	}
	return share.take(d.clock(), lookupShareBurst, lookupShareRefill)
}

// apiAccountLookup — POST /delegate/accounts/lookup {name}: the person's
// agent, under its session → {name, exists?, id?, displayName?,
// ambiguous?, unseen?, why?} (the file comment).
func (s *Server) apiAccountLookup(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	if a.p.AgentSession == "" {
		WriteError(w, http.StatusForbidden, "a lookup is made by a conversation: send its agent session ("+HeaderAgentSession+")", delegationDocs)
		return
	}
	res, ok := s.ChangeSets.(AccountResolver)
	if !ok {
		WriteError(w, http.StatusNotImplemented, "this xbind looks up no accounts", delegationDocs)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(r, &in); err != nil || !lookupNameOK(in.Name) {
		WriteError(w, http.StatusBadRequest, "need {name}: an account id or a display name, at most 100 characters", delegationDocs)
		return
	}
	if ok, wait := s.delegateRT.allowLookup(a.p.UserID, a.p.AgentSession); !ok {
		secs := int(wait/time.Second) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		WriteError(w, http.StatusTooManyRequests, fmt.Sprintf("this conversation is looking up accounts too fast (%d at once, one more every %d s): it may try again in %d s",
			lookupShareBurst, int(lookupShareRefill/time.Second), secs), delegationDocs)
		return
	}
	name := strings.TrimSpace(in.Name)
	m := res.ResolveAccount(personOf(a.p), name)
	out := map[string]any{"name": name}
	switch m.State {
	case "one":
		out["exists"], out["id"], out["displayName"] = true, m.ID, m.Name
	case "none":
		out["exists"] = false
		out["why"] = "no account has that id or display name (exact match only)"
	case "several":
		out["exists"], out["ambiguous"] = true, true
		out["why"] = "more than one account has that display name: ask your person which one (its account id)"
	default:
		out["unseen"], out["why"] = true, lookupUnseen
	}
	WriteJSON(w, http.StatusOK, out)
}

// lookupNameOK: a name to look up — 1 to 100 characters, no control or
// format character.
func lookupNameOK(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || len([]rune(t)) > 100 {
		return false
	}
	for _, c := range t {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return false
		}
	}
	return true
}
