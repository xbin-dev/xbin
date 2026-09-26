// needs_push.go — "Needs you" reaching a person's phone.
//
// The moments GET /needs lists — the agent (or a subagent) asks a question
// or wants an approval, an automation's run failed — also go out as a push
// notification to the xbin app of each person who would see them there
// (xbin.NotifyUserWith → POST /api/xbin/notify, which delivers only to people
// who can read this tile, through the workspace's push relay, sealed to the
// device). Tapping one opens the conversation: the link is `#c=<run>`.
//
// Who gets one is what /needs says, from the run's ACL alone — never from a
// request, so an admin viewing as someone (D64) never causes or receives one:
//
//   - a question or an approval: the people who may answer it — the owner
//     and participant members (a team-wide participant role is not a list of
//     people: they see it under Needs you when they look);
//   - a failed automation run (schedule, watcher, channel, trigger): its owner.
//
// It is best-effort and quiet: the engine hands the moment over after its
// transaction commits and never waits. After a short grace the run is read
// again and nothing is sent if it moved on (answered at once, by someone
// looking). One push per run and state within dedupeFor — per question or
// approval, so a new question in the same run is news again, but a run that
// keeps failing is one push — and at most userBurst per
// person, refilled one per userEvery, over every run of this tile. xbind has
// its own limits on top (per tile, per person); a refusal is logged, never
// retried.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// The needs a push is sent for (GET /needs reasons).
const (
	needQuestion = "question"
	needApproval = "approval"
	needFailed   = "failed"
)

type needsPusher struct {
	send  func(ctx context.Context, n xbin.UserNotification) error
	grace time.Duration // wait this long, then send only if the run still needs it
	now   func() time.Time

	dedupeFor time.Duration // the same run+state+content is sent once in this window
	userBurst int           // per person: at most this many at once…
	userEvery time.Duration // …refilled one per this

	mu    sync.Mutex
	sent  map[string]time.Time  // run:state:fingerprint → when
	users map[string]*needsRate // person → their budget
}

type needsRate struct {
	tokens float64
	at     time.Time
}

func newNeedsPusher(send func(context.Context, xbin.UserNotification) error) *needsPusher {
	return &needsPusher{send: send, grace: 3 * time.Second, now: time.Now,
		dedupeFor: 6 * time.Hour, userBurst: 10, userEvery: 6 * time.Minute,
		sent: map[string]time.Time{}, users: map[string]*needsRate{}}
}

// needsMoment tells the pusher that run may need someone now (it started
// waiting, or failed). Called after the transaction that did it committed.
func (ag *Agent) needsMoment(runID int64) {
	if ag == nil || ag.needs == nil {
		return
	}
	p := ag.needs
	if p.grace <= 0 {
		go p.check(ag, runID)
		return
	}
	time.AfterFunc(p.grace, func() { p.check(ag, runID) })
}

// needsPush is one notification to one person.
type needsPush struct {
	state, user, title, body, link, fingerprint string
	run                                         int64
}

// check reads the run as it is now and pushes what it needs, to whom.
func (p *needsPusher) check(ag *Agent, runID int64) {
	for _, n := range ag.needsPushes(runID) {
		if !p.admit(n) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := p.send(ctx, xbin.UserNotification{User: n.user, Title: n.title, Body: n.body, Link: n.link,
			Kind: n.state, CollapseID: fmt.Sprintf("needs:%d", n.run)})
		cancel()
		if err != nil {
			log.Printf("needs push: run %d (%s) to %s: %v", n.run, n.state, n.user, err)
		}
	}
}

// needsPushes is what run needs right now, one entry per person to tell —
// nothing when it no longer waits or failed.
func (ag *Agent) needsPushes(runID int64) []needsPush {
	run, err := ag.db.getRun(runID)
	if err != nil {
		return nil
	}
	root := run
	if run.ParentID != 0 {
		if root, err = ag.db.getRun(rootOf(run)); err != nil {
			return nil
		}
	}
	var state, body, fp string
	switch {
	case run.Status == statusWaiting:
		pend := parsePending(run.Pending)
		if pend.Kind == "approval" {
			state = needApproval
			var names []string
			for _, c := range pend.ToolCalls {
				names = append(names, c.Function.Name)
				fp += c.ID + "\x00"
			}
			body = "Wants to run " + clip(strings.Join(names, ", "), 120) + " — approve or deny."
		} else {
			state = needQuestion
			body = clip(plainText(run.Result), 240)
			if body == "" {
				body = "The agent is waiting for your answer."
			}
			fp = run.Result
		}
	case run.Status == statusError && run.ParentID == 0 && automationOrigin(run.Origin):
		state = needFailed
		body = "The automation's run failed"
		if e := strings.TrimSpace(plainText(run.Result)); e != "" {
			body += ": " + clip(e, 200)
		}
		// no fingerprint: a run that keeps failing (a watcher, a session)
		// is one push per dedupe window, not one per failed turn
	default:
		return nil
	}
	acl, err := ag.db.loadACL(root.ID) // fresh: who may answer NOW
	if err != nil {
		return nil
	}
	var users []string
	if person(acl.owner) {
		users = append(users, acl.owner)
	}
	if state != needFailed {
		for u, role := range acl.members {
			if role == roleParticipant && person(u) && u != acl.owner {
				users = append(users, u)
			}
		}
	}
	title := clip(strings.TrimSpace(root.Title), 100)
	if title == "" {
		title = fmt.Sprintf("Conversation %d", root.ID)
	}
	sum := sha256.Sum256([]byte(fp))
	out := make([]needsPush, 0, len(users))
	for _, u := range users {
		out = append(out, needsPush{state: state, user: u, title: title, body: body, run: run.ID,
			link: fmt.Sprintf("#c=%d", run.ID), fingerprint: hex.EncodeToString(sum[:8])})
	}
	return out
}

// admit applies the per run+state dedupe and the per-person budget.
func (p *needsPusher) admit(n needsPush) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for k, at := range p.sent {
		if now.Sub(at) >= p.dedupeFor {
			delete(p.sent, k)
		}
	}
	key := fmt.Sprintf("%d:%s:%s:%s", n.run, n.state, n.fingerprint, n.user)
	if _, dup := p.sent[key]; dup {
		return false
	}
	r := p.users[n.user]
	if r == nil {
		r = &needsRate{tokens: float64(p.userBurst), at: now}
		p.users[n.user] = r
	}
	if p.userEvery > 0 {
		r.tokens += float64(now.Sub(r.at)) / float64(p.userEvery)
		if r.tokens > float64(p.userBurst) {
			r.tokens = float64(p.userBurst)
		}
	}
	r.at = now
	if r.tokens < 1 {
		log.Printf("needs push: %s is over %d per %s; dropped run %d (%s)", n.user, p.userBurst, p.userEvery, n.run, n.state)
		return false
	}
	r.tokens--
	p.sent[key] = now
	return true
}

// person: an owner or member that is a person (not unowned, not a component).
func person(u string) bool { return u != "" && !strings.HasPrefix(u, "el:") }

// automationOrigin: runs an automation started — their failures are news
// (GET /needs "failed").
func automationOrigin(o string) bool {
	switch o {
	case "", "chat", "api", "held":
		return false
	}
	return true
}

// plainText is markdown as a notification shows it: no emphasis marks or
// heading hashes, one line.
func plainText(s string) string {
	r := strings.NewReplacer("**", "", "__", "", "`", "")
	lines := strings.Fields(r.Replace(strings.TrimSpace(s)))
	return strings.TrimLeft(strings.Join(lines, " "), "# ")
}
