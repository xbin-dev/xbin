package delegation

// sessions.go — agent sessions: the handles xbind issues to a person's
// workspace agent (D228 part C0; docs/protocol/delegation.md §Agent
// sessions). A handle says, in xbind's own record, what one conversation
// of the agent is: its SCOPE (the orgs and tiles its person let it see;
// nil: everything they can) and its MODEL — which the delegate gate judges
// org policy with and the LLM connector enforces, so the model is no
// longer something the agent says about itself — and its TAINT: the orgs
// whose tiles' data the conversation has touched. A handle belongs to one
// person and one holder; another's id is no handle.

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// Limits.
const (
	MaxAgentSessions = 32             // live handles a person (per holder); the least recently used go first
	AgentSessionIdle = 24 * time.Hour // a handle no call used this long is gone
	maxScopeEntries  = 2000
	sessionTouch     = time.Minute // how stale a handle's Used may be on disk
)

// SessionScope is what a conversation may see: orgs (every tile under
// them; "workspace" for the tiles in no org) and single tiles.
type SessionScope struct {
	Orgs  []string `json:"orgs"`
	Tiles []string `json:"tiles"`
}

// AgentSession is one handle.
type AgentSession struct {
	ID    string        `json:"id"`
	User  string        `json:"user"`
	Agent string        `json:"agent"` // the holder
	Scope *SessionScope `json:"scope"` // nil: everything the person can see
	Model string        `json:"model"` // "<upstream>/<model>"
	// Tainted: the orgs whose tiles a call under this handle touched
	// ("workspace" for tiles in no org). It only grows.
	Tainted []string  `json:"tainted"`
	Created time.Time `json:"created"`
	Used    time.Time `json:"used"`
}

type sessionsDoc struct {
	Schema   int            `json:"schema"`
	Sessions []AgentSession `json:"sessions"`
}

// Agent-session refusals.
var (
	ErrNoAgentSession = errors.New("no such agent session: it doesn't exist, expired, or is another agent's")
	ErrBadModel       = errors.New(`a session's model is "<upstream>/<model>"`)
	ErrBadScope2      = fmt.Errorf("a scope names at most %d orgs and tiles, each a name", maxScopeEntries)
)

func (s *Store) sessionsPath(user string) string {
	return filepath.Join(userDir(s.dir, user), "agent-sessions.json")
}

func modelOK(m string) bool {
	if len(m) < 3 || len(m) > 200 || !strings.Contains(m, "/") || strings.HasPrefix(m, "/") || strings.HasSuffix(m, "/") {
		return false
	}
	for _, c := range m {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func normScope(sc *SessionScope) (*SessionScope, error) {
	if sc == nil {
		return nil, nil
	}
	out := &SessionScope{Orgs: []string{}, Tiles: []string{}}
	if len(sc.Orgs)+len(sc.Tiles) > maxScopeEntries {
		return nil, ErrBadScope2
	}
	clean := func(in []string, trim bool) ([]string, error) {
		res := []string{}
		for _, v := range in {
			if trim {
				v = strings.Trim(v, "/")
			}
			if v == "" || len(v) > 200 || strings.ContainsAny(v, " \t\r\n\x00") {
				return nil, ErrBadScope2
			}
			if !slices.Contains(res, v) {
				res = append(res, v)
			}
		}
		slices.Sort(res)
		return res, nil
	}
	var err error
	if out.Orgs, err = clean(sc.Orgs, false); err != nil {
		return nil, err
	}
	if out.Tiles, err = clean(sc.Tiles, true); err != nil {
		return nil, err
	}
	return out, nil
}

// sessions loads user's handles and drops the expired; the caller holds
// p.mu. changed reports that some were dropped.
func (s *Store) sessions(p *person, user string) (sessionsDoc, bool, error) {
	doc, err := load(&p.sess, s.sessionsPath(user))
	if err != nil {
		return sessionsDoc{}, false, err
	}
	now := s.now()
	n := len(doc.Sessions)
	live := make([]AgentSession, 0, n)
	for _, a := range doc.Sessions {
		if used := p.sessUsed[a.ID]; used.After(a.Used) {
			a.Used = used
		}
		if now.Sub(a.Used) < AgentSessionIdle {
			live = append(live, a)
		}
	}
	doc.Sessions = live
	return doc, len(live) != n, nil
}

func (s *Store) saveSessions(p *person, user string, doc sessionsDoc) error {
	doc.Schema = 1
	return save(&p.sess, s.sessionsPath(user), doc)
}

// NewAgentSession issues a handle for user's agent holder.
func (s *Store) NewAgentSession(user, holder string, scope *SessionScope, model string) (AgentSession, error) {
	if s == nil {
		return AgentSession{}, ErrNoStore
	}
	if !modelOK(model) {
		return AgentSession{}, ErrBadModel
	}
	sc, err := normScope(scope)
	if err != nil {
		return AgentSession{}, err
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.sessions(p, user)
	if err != nil {
		return AgentSession{}, err
	}
	// past the cap the least recently used of this holder go
	var mine []int
	for i, a := range doc.Sessions {
		if a.Agent == holder {
			mine = append(mine, i)
		}
	}
	for len(mine) >= MaxAgentSessions {
		oldest := mine[0]
		for _, i := range mine {
			if doc.Sessions[i].Used.Before(doc.Sessions[oldest].Used) {
				oldest = i
			}
		}
		doc.Sessions = slices.Delete(doc.Sessions, oldest, oldest+1)
		mine = mine[:0]
		for i, a := range doc.Sessions {
			if a.Agent == holder {
				mine = append(mine, i)
			}
		}
	}
	now := s.now().UTC().Truncate(time.Second)
	a := AgentSession{ID: util.RandomToken(16), User: user, Agent: holder, Scope: sc, Model: model, Tainted: []string{}, Created: now, Used: now}
	doc.Sessions = append(doc.Sessions, a)
	return a, s.saveSessions(p, user, doc)
}

// AgentSession is user's live handle id of holder; using it keeps it alive.
func (s *Store) AgentSession(user, holder, id string) (AgentSession, bool) {
	if s == nil || id == "" || len(id) > 64 {
		return AgentSession{}, false
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, changed, err := s.sessions(p, user)
	if err != nil {
		return AgentSession{}, false
	}
	i := slices.IndexFunc(doc.Sessions, func(a AgentSession) bool { return a.ID == id && a.Agent == holder })
	if i < 0 {
		if changed {
			_ = s.saveSessions(p, user, doc)
		}
		return AgentSession{}, false
	}
	now := s.now().UTC()
	stale := now.Sub(doc.Sessions[i].Used) > sessionTouch
	doc.Sessions[i].Used = now
	if p.sessUsed == nil {
		p.sessUsed = map[string]time.Time{}
	}
	p.sessUsed[id] = now
	if changed || stale { // the time of use reaches the disk at most once a minute
		_ = s.saveSessions(p, user, doc)
	}
	return doc.Sessions[i], true
}

// AgentSessions lists user's live handles (holder "": every holder's).
func (s *Store) AgentSessions(user, holder string) []AgentSession {
	if s == nil {
		return nil
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, _ := s.sessions(p, user)
	out := []AgentSession{}
	for _, a := range doc.Sessions {
		if holder == "" || a.Agent == holder {
			out = append(out, a)
		}
	}
	return out
}

// UpdateAgentSession applies change to the handle and stores it; change's
// error leaves it as it was. Tainted can't be made smaller.
func (s *Store) UpdateAgentSession(user, holder, id string, change func(*AgentSession) error) (AgentSession, error) {
	if s == nil {
		return AgentSession{}, ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.sessions(p, user)
	if err != nil {
		return AgentSession{}, err
	}
	i := slices.IndexFunc(doc.Sessions, func(a AgentSession) bool { return a.ID == id && a.Agent == holder })
	if i < 0 {
		return AgentSession{}, ErrNoAgentSession
	}
	a := doc.Sessions[i]
	a.Tainted = slices.Clone(a.Tainted)
	if err := change(&a); err != nil {
		return doc.Sessions[i], err
	}
	if !modelOK(a.Model) {
		return doc.Sessions[i], ErrBadModel
	}
	if a.Scope, err = normScope(a.Scope); err != nil {
		return doc.Sessions[i], err
	}
	for _, o := range doc.Sessions[i].Tainted {
		if !slices.Contains(a.Tainted, o) {
			a.Tainted = append(a.Tainted, o)
		}
	}
	slices.Sort(a.Tainted)
	a.ID, a.User, a.Agent, a.Created, a.Used = id, user, holder, doc.Sessions[i].Created, s.now().UTC()
	doc.Sessions[i] = a
	return a, s.saveSessions(p, user, doc)
}

// TaintAgentSession adds orgs to the handle's tainted set; it writes only
// when one is new.
func (s *Store) TaintAgentSession(user, holder, id string, orgs []string) error {
	if s == nil || len(orgs) == 0 {
		return nil
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.sessions(p, user)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(doc.Sessions, func(a AgentSession) bool { return a.ID == id && a.Agent == holder })
	if i < 0 {
		return ErrNoAgentSession
	}
	added := false
	for _, o := range orgs {
		if o != "" && !slices.Contains(doc.Sessions[i].Tainted, o) {
			doc.Sessions[i].Tainted = append(slices.Clone(doc.Sessions[i].Tainted), o)
			added = true
		}
	}
	if !added {
		return nil
	}
	slices.Sort(doc.Sessions[i].Tainted)
	return s.saveSessions(p, user, doc)
}

// DeleteAgentSession ends a handle; false when there is none.
func (s *Store) DeleteAgentSession(user, holder, id string) (bool, error) {
	if s == nil {
		return false, ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.sessions(p, user)
	if err != nil {
		return false, err
	}
	n := len(doc.Sessions)
	doc.Sessions = slices.DeleteFunc(doc.Sessions, func(a AgentSession) bool { return a.ID == id && a.Agent == holder })
	if len(doc.Sessions) == n {
		return false, nil
	}
	delete(p.sessUsed, id)
	return true, s.saveSessions(p, user, doc)
}
