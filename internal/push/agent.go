package push

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
)

// The workspace agent's notifications (D241; native/spec/push.md §2,
// plans/wsagent/ios-stage2.md §3). The server decides what waits and
// writes the words (internal/server delegatepush.go); this is the plane's
// side: the kinds, the sealed `agent` member, the background push that
// takes notifications back, the relay's features, and the words for what
// a person's agent holder notifies through POST /notify.

// The kinds (all under "agent": a registration of "agent" takes them).
const (
	KindAgentApproval  = "agent.approval"   // a request waits for the person
	KindAgentChangeSet = "agent.changeset"  // a change set (a plan, admin changes, an admin's ask) waits
	KindAgentAsk       = "agent.ask"        // the agent asks its person something
	KindAgentDone      = "agent.done"       // a request of the person's is finished
	KindAgentClear     = "agent.clear"      // background: notifications that no longer stand
	KindAgentWSLive    = "agent.wsactivity" // a push-to-start of the workspace agent's Live Activity
)

// AgentNote is one notification about a person's workspace agent: the
// server's words and the payload's `agent` member (the plane adds its
// version).
type AgentNote struct {
	Kind, Title, Body, Link, Collapse string
	Agent                             map[string]any
	Priority                          int
}

// AgentNotice sends n to user's devices that take its kind. sent: it went
// to at least one; limited: over the person's agent push budget (dropped).
func (s *Service) AgentNotice(user string, n AgentNote) (sent, limited bool) {
	if s == nil || user == "" || !s.Enabled() {
		return false, false
	}
	if _, ok := s.account(user); !ok {
		return false, false
	}
	posts := s.posts(user, n.Kind)
	if posts == 0 {
		return false, false
	}
	if ok, _ := s.agent.allowN(user, posts); !ok {
		s.snd.limited.Add(1)
		return false, true
	}
	raw, err := agentMember(n.Agent)
	if err != nil {
		return false, false
	}
	return s.enqueue(note{user: user, kind: n.Kind, title: n.Title, body: n.Body, link: n.Link, collapse: n.Collapse,
		priority: n.Priority, agent: raw}), false
}

// maxClear bounds the collapse ids one agent.clear names.
const maxClear = 24

// AgentClear takes back user's notifications with these collapse ids — the
// things they were about no longer wait — with a background push the app
// opens like any other (it removes them, and sets the badge). A relay
// without background pushes gets nothing: the app sweeps on foreground and
// background refresh by itself.
func (s *Service) AgentClear(user string, collapses []string, badge int) {
	if s == nil || user == "" || len(collapses) == 0 || !s.Enabled() {
		return
	}
	if _, ok := s.account(user); !ok {
		return
	}
	for len(collapses) > 0 {
		part := collapses[:min(len(collapses), maxClear)]
		collapses = collapses[len(part):]
		raw, err := agentMember(map[string]any{"clear": part, "badge": badge})
		if err != nil || s.posts(user, KindAgentClear) == 0 {
			return
		}
		s.enqueue(note{user: user, kind: KindAgentClear, agent: raw, background: true})
	}
}

func agentMember(m map[string]any) (RawJSON, error) {
	out := map[string]any{"v": 1}
	for k, v := range m {
		out[k] = v
	}
	b, err := json.Marshal(out)
	return RawJSON(b), err
}

// ---- the relay's features (GET /v1/workspace → features) ----

const (
	featureBackground    = "background"     // POST /v1/push {type: "background"}
	featureActivityAgent = "activity-agent" // activity.kind "agent", phase "paused", agents
	// featureTTL is how long a reading of the relay's features stands.
	featureTTL = time.Hour
)

// relayHas reports whether the relay in force lists feature, reading its
// features when the last reading is stale (callers are the sender's
// workers and timers, never a request handler).
func (s *Service) relayHas(feature string) bool {
	c, _ := s.relayConfig()
	if c == nil {
		return false
	}
	s.fmu.Lock()
	stale := s.featAt.IsZero() || s.o.Now().Sub(s.featAt) > featureTTL || s.featKey != c.Key
	s.fmu.Unlock()
	if stale {
		s.readFeatures(c)
	}
	s.fmu.Lock()
	defer s.fmu.Unlock()
	return s.feats[feature]
}

// readFeatures asks the relay what it can do (an older relay lists
// nothing: everything new is then left out).
func (s *Service) readFeatures(c *RelayConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out struct {
		WorkspaceID string   `json:"workspaceId"`
		Features    []string `json:"features"`
	}
	code, err := s.relayCall(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/v1/workspace", c.Key, &out)
	feats := map[string]bool{}
	for _, f := range out.Features {
		feats[f] = true
	}
	s.fmu.Lock()
	defer s.fmu.Unlock()
	if err != nil && code != http.StatusOK {
		// unknown: keep what was read before, and ask again in a minute
		s.featAt = s.o.Now().Add(-featureTTL + time.Minute)
		return
	}
	s.feats, s.featAt, s.featKey = feats, s.o.Now(), c.Key
}

// ---- a holder's notifications (the person's own workspace agent) ----

// convRe is the conversation a holder's link names (`#c=<run>`).
var convRe = regexp.MustCompile(`^#c=([0-9]{1,18})$`)

// holderNotice is what xbind sends for a delegation holder's POST /notify
// — the sealed workspace agent's backend notifying its person (its kinds:
// question, done). The holder's title and body never reach a screen: a
// conversation's title is the model's, so xbind writes the words, and the
// question is carried apart (`agent.ask`) for the app to draw quoted, as
// the agent's own. false: nothing to send for that kind.
func holderNotice(user, holder, kind, body, link string, badge int) (AgentNote, bool) {
	conv := ""
	if m := convRe.FindStringSubmatch(link); m != nil {
		conv = m[1]
	}
	agent := map[string]any{"badge": badge, "holder": holder}
	if conv != "" {
		agent["conversation"] = map[string]any{"id": conv}
	}
	route := "xbin/agent"
	collapse := "agent-conv:" + holder
	if conv != "" {
		route += "#c=" + conv
		collapse = "agent-conv:" + conv
	}
	switch kind {
	case "question":
		agent["ask"] = cutRunes(strings.TrimSpace(body), 200)
		return AgentNote{Kind: KindAgentAsk, Title: "Question from your workspace agent", Body: "Open the agent to answer, or reply here.",
			Link: route, Collapse: collapse, Agent: agent, Priority: 10}, true
	case "done":
		return AgentNote{Kind: KindAgentDone, Title: "Workspace agent finished", Body: "The report is ready.",
			Link: route, Collapse: collapse, Agent: agent, Priority: 5}, true
	}
	return AgentNote{}, false
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// notifyHolder answers a delegation holder's POST /notify (notify, api.go):
// the kind mapped to xbind's own notification for the person the holder's
// partition is, sent within the agent budget. handled false: the caller is
// no holder, and POST /notify goes on as for any tile.
func (s *Service) notifyHolder(w http.ResponseWriter, p auth.Principal, tile, user, kind, body, link string) (handled bool) {
	if !auth.HoldsDelegation(tile) {
		return false
	}
	badge := 0
	if s.o.AgentBadge != nil {
		badge = s.o.AgentBadge(user)
	}
	n, ok := holderNotice(user, tile, kind, body, link, badge)
	if !ok {
		server.WriteJSON(w, http.StatusAccepted, map[string]any{"ok": true, "suppressed": true})
		return true
	}
	s.AgentNotice(user, n)
	server.WriteJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	return true
}
