package push

import (
	"slices"
	"time"
)

// The workspace agent's Live Activity (D241; plans/wsagent/ios-stage2.md
// §5). One card per person and workspace while their agent works or
// something waits for them: its phase (running, waiting, paused, idle),
// since when, how many things wait, how many tile agents it started are
// working — counts only, like the tile agents' cards (activity.go), and
// through the same registrations, store, sender, limits and relay endpoint.
// It is the Activity of session AgentSession on a device; its push-to-start
// handle is the device's AgentStartHandle (its own ActivityKit type: the
// app's WorkspaceAgentActivityAttributes).
//
// What the person's agent is doing is the server's to say
// (Options.AgentPicture); xbind reads it again when something may have
// changed (AgentChanged: a delegate event, a status change of a session
// their agent started) and when the holder calls its model (AgentWorked: a
// turn runs), coalesced per person. A stretch begins when the agent works
// or something waits, and ends Options.AgentIdleGrace after neither — the
// card shows idle meanwhile — or at once when the person pauses their agent
// (the last state paused, on a relay that lists activity-agent), when it is
// no longer theirs to act (workspace agents off, taken away, held) or push
// goes off.

// AgentSession is the Activity.Session of the workspace agent's card: no
// agent session id has an '@' (sessionIDRe).
const AgentSession = "@agent"

// agentKind is the relay's activity kind of the workspace agent's card.
const agentKind = "agent"

// AgentPicture is what a person's workspace agent is doing (the server's
// AgentPictureOf): Has — they have one that may act now; Paused; Pending —
// what waits for them; Agents — tile agents it started that are working.
type AgentPicture struct {
	Has, Paused     bool
	Pending, Agents int
}

const (
	agentTurnWindow = 90 * time.Second // a model call this recent: its turn runs
	agentDismiss    = 5 * time.Minute  // an ended card stays on the lock screen
	agentPausedShow = time.Minute      // …a paused one
)

// agentLive is what xbind knows of one person's card.
type agentLive struct {
	active    bool          // a stretch is on (working or waiting, or idle within the grace)
	stretch   int           // stretches seen begin (a push-to-start timer names its own)
	since     int64         // unix seconds the stretch began
	worked    time.Time     // the holder's last model call
	state     ActivityState // what the card should show
	lastTS    int64
	sent      map[string]ActivityState // deviceID → what xbind last sent (or started) there
	dismissed map[string]int           // deviceID → the stretch the person swiped its card away in
	coalesce  *time.Timer
	idle      *time.Timer // ends the stretch after the grace
	turnEnd   *time.Timer // reads again when the turn window closes
	start     *time.Timer // push-to-start
	flush     *time.Timer // what the per-handle limit held back
}

func (al *agentLive) ts(now time.Time) int64 {
	t := max(now.Unix(), al.lastTS+1)
	al.lastTS = t
	return t
}

// agentOf is user's record (amu held).
func (s *Service) agentOf(user string) *agentLive {
	al := s.agents[user]
	if al == nil {
		al = &agentLive{sent: map[string]ActivityState{}}
		s.agents[user] = al
	}
	return al
}

// AgentChanged reads user's picture again shortly (changes inside the
// coalescing window are one read).
func (s *Service) AgentChanged(user string) {
	if s == nil || user == "" || s.o.AgentPicture == nil {
		return
	}
	wait := s.o.AgentCoalesce
	if wait == 0 {
		wait = 500 * time.Millisecond
	}
	if wait < 0 {
		s.agentRecompute(user)
		return
	}
	s.amu.Lock()
	al := s.agentOf(user)
	if al.coalesce == nil {
		al.coalesce = time.AfterFunc(wait, func() {
			s.amu.Lock()
			al.coalesce = nil
			s.amu.Unlock()
			s.agentRecompute(user)
		})
	}
	s.amu.Unlock()
}

// AgentWorked notes that user's agent holder called its model: its turn
// runs (for agentTurnWindow).
func (s *Service) AgentWorked(user string) {
	if s == nil || user == "" || s.o.AgentPicture == nil {
		return
	}
	s.amu.Lock()
	al := s.agentOf(user)
	was := !al.worked.IsZero() && s.o.Now().Sub(al.worked) < agentTurnWindow
	al.worked = s.o.Now()
	if al.turnEnd != nil {
		al.turnEnd.Stop()
	}
	al.turnEnd = time.AfterFunc(agentTurnWindow+time.Second, func() { s.AgentChanged(user) })
	s.amu.Unlock()
	if !was {
		s.AgentChanged(user)
	}
}

// agentHasCard reports whether any of user's devices shows or could be
// given the card.
func (s *Service) agentHasCard(user string) bool {
	for _, d := range s.devices(user) {
		if d.AgentStartHandle != "" || slices.ContainsFunc(d.Activities, func(a Activity) bool { return a.Session == AgentSession }) {
			return true
		}
	}
	return false
}

// agentRecompute reads user's picture and pushes what changed.
func (s *Service) agentRecompute(user string) {
	if !s.Enabled() || !s.agentHasCard(user) {
		s.amu.Lock()
		if al := s.agents[user]; al != nil {
			s.stopAgentTimers(al)
			delete(s.agents, user) // nothing to show: a later stretch starts afresh
		}
		s.amu.Unlock()
		return
	}
	pic := s.o.AgentPicture(user)
	_, live := s.account(user)
	now := s.o.Now()
	var jobs []liveJob
	s.amu.Lock()
	al := s.agentOf(user)
	working := pic.Agents > 0 || (!al.worked.IsZero() && now.Sub(al.worked) < agentTurnWindow)
	waiting := pic.Pending > 0
	switch {
	case !pic.Has || !live:
		// no agent that may act: the card goes now
		if al.active {
			jobs = s.agentEndLocked(user, al, ActivityState{Phase: PhaseIdle, Since: al.since}, now, now)
		}
	case pic.Paused:
		if al.active {
			jobs = s.agentEndLocked(user, al, ActivityState{Phase: PhasePaused, Since: al.since, Pending: min(pic.Pending, 99)},
				now, now.Add(agentPausedShow))
		}
	case working || waiting:
		if !al.active { // a stretch begins
			al.active, al.since = true, now.Unix()
			al.stretch++
			al.dismissed = nil
			s.agentArmStart(user, al)
		}
		if al.idle != nil {
			al.idle.Stop()
			al.idle = nil
		}
		st := ActivityState{Phase: PhaseRunning, Since: al.since, Pending: min(pic.Pending, 99), Agents: min(pic.Agents, 99)}
		if waiting {
			st.Phase = PhaseWaiting
		}
		if st != al.state {
			al.state = st
			jobs = s.agentUpdateLocked(user, al, now)
		}
	case al.active:
		// nothing works or waits: the card says so, and goes after the grace
		st := ActivityState{Phase: PhaseIdle, Since: al.since}
		if st != al.state {
			al.state = st
			jobs = s.agentUpdateLocked(user, al, now)
		}
		if al.idle == nil {
			grace := s.o.AgentIdleGrace
			if grace == 0 {
				grace = 2 * time.Minute
			}
			stretch := al.stretch
			al.idle = time.AfterFunc(grace, func() { s.agentIdleOver(user, stretch) })
		}
	}
	s.amu.Unlock()
	s.enqueueLive(jobs)
}

// agentIdleOver ends a stretch that stayed idle for the grace.
func (s *Service) agentIdleOver(user string, stretch int) {
	now := s.o.Now()
	s.amu.Lock()
	al := s.agents[user]
	if al == nil || !al.active || al.stretch != stretch || al.state.Phase != PhaseIdle {
		s.amu.Unlock()
		return
	}
	al.idle = nil
	jobs := s.agentEndLocked(user, al, al.state, now, now.Add(agentDismiss))
	s.amu.Unlock()
	s.enqueueLive(jobs)
}

func (s *Service) stopAgentTimers(al *agentLive) {
	for _, t := range []*time.Timer{al.coalesce, al.idle, al.turnEnd, al.start, al.flush} {
		if t != nil {
			t.Stop()
		}
	}
	al.coalesce, al.idle, al.turnEnd, al.start, al.flush = nil, nil, nil, nil, nil
}

// agentArmStart schedules the push-to-start of this stretch (amu held).
func (s *Service) agentArmStart(user string, al *agentLive) {
	if al.start != nil {
		al.start.Stop()
		al.start = nil
	}
	after := s.o.StartAfter
	if after < 0 {
		return
	}
	if after == 0 {
		after = 30 * time.Second
	}
	stretch := al.stretch
	al.start = time.AfterFunc(after, func() { s.agentPushToStart(user, stretch) })
}

// agentPushToStart starts the card by push on each device that can, shows
// none and didn't swipe this stretch's away — on a relay that knows the
// card's type (an older one would draw it as a tile agent's turn).
func (s *Service) agentPushToStart(user string, stretch int) {
	if !s.relayHas(featureActivityAgent) {
		return
	}
	now := s.o.Now()
	var jobs []liveJob
	s.amu.Lock()
	al := s.agents[user]
	if al == nil || !al.active || al.stretch != stretch || al.state.Phase == PhaseIdle {
		s.amu.Unlock()
		return
	}
	al.start = nil
	e := s.currentEpoch()
	var ts int64
	for _, d := range s.devices(user) {
		if d.AgentStartHandle == "" || d.stale(e) || !kindAllowed(d.Kinds, KindAgentWSLive) ||
			slices.ContainsFunc(d.Activities, func(a Activity) bool { return a.Session == AgentSession }) {
			continue
		}
		if t, ok := al.dismissed[d.DeviceID]; ok && t == stretch {
			continue
		}
		ref := randomRef()
		if !s.st.setActivity(user, d.DeviceID, Activity{Session: AgentSession, Ref: ref, Created: now.Unix()}) {
			continue
		}
		if ts == 0 {
			ts = al.ts(now)
		}
		al.sent[d.DeviceID] = al.state
		jobs = append(jobs, liveJob{user: user, deviceID: d.DeviceID, session: AgentSession, handle: d.AgentStartHandle, start: true,
			priority: 10, act: activityPush{Kind: agentKind, Event: "start", Timestamp: ts, State: al.state,
				StaleDate: now.Add(activityStale).Unix(), WS: s.Workspace(), Ref: ref}})
	}
	s.amu.Unlock()
	s.enqueueLive(jobs)
}

// agentUpdateLocked pushes al.state to user's registered cards (amu held):
// waiting at priority 10, the rest 5; a handle over its limit gets the
// then-current state once the limit allows (agentFlush).
func (s *Service) agentUpdateLocked(user string, al *agentLive, now time.Time) []liveJob {
	prio := 5
	if al.state.Phase == PhaseWaiting {
		prio = 10
	}
	var jobs []liveJob
	var ts int64
	held := time.Duration(-1)
	for _, d := range s.devices(user) {
		for _, a := range d.Activities {
			if a.Session != AgentSession || a.Handle == "" || al.sent[d.DeviceID] == al.state {
				continue
			}
			if ok, wait := s.actPush.allow(a.Handle); !ok {
				s.snd.limited.Add(1)
				if held < 0 || wait < held {
					held = wait
				}
				continue
			}
			if ts == 0 {
				ts = al.ts(now)
			}
			al.sent[d.DeviceID] = al.state
			jobs = append(jobs, liveJob{user: user, deviceID: d.DeviceID, session: AgentSession, handle: a.Handle, priority: prio,
				act: activityPush{Kind: agentKind, Event: "update", Timestamp: ts, State: al.state, StaleDate: now.Add(activityStale).Unix()}})
		}
	}
	if held >= 0 && al.flush == nil {
		al.flush = time.AfterFunc(max(held, 10*time.Millisecond), func() { s.agentFlush(user, al) })
	}
	return jobs
}

func (s *Service) agentFlush(user string, of *agentLive) {
	now := s.o.Now()
	s.amu.Lock()
	al := s.agents[user]
	if al != of || !al.active {
		s.amu.Unlock()
		return
	}
	al.flush = nil
	jobs := s.agentUpdateLocked(user, al, now)
	s.amu.Unlock()
	s.enqueueLive(jobs)
}

// agentEndLocked ends user's cards on final, dismissed at dismiss, and
// drops their registrations: a card is one stretch (amu held).
func (s *Service) agentEndLocked(user string, al *agentLive, final ActivityState, now, dismiss time.Time) []liveJob {
	s.stopAgentTimers(al)
	al.active, al.state = false, ActivityState{}
	var jobs []liveJob
	var ts int64
	for _, d := range s.devices(user) {
		for _, a := range d.Activities {
			if a.Session != AgentSession {
				continue
			}
			s.st.removeActivity(user, d.DeviceID, AgentSession, a.Handle)
			if a.Handle == "" {
				if a.Ref != "" { // started by push, its token not here yet: a registration by its ref gets the end
					s.lmu.Lock()
					s.rememberEnded(user, d.DeviceID, a.Ref, endedRef{session: AgentSession, since: al.since, lastTS: al.lastTS,
						expires: now.Add(endedRefTTL).Unix()}, now)
					s.lmu.Unlock()
				}
				continue
			}
			if ts == 0 {
				ts = al.ts(now)
			}
			jobs = append(jobs, liveJob{user: user, deviceID: d.DeviceID, session: AgentSession, handle: a.Handle, priority: 10,
				act: activityPush{Kind: agentKind, Event: "end", Timestamp: ts, State: final, DismissalDate: dismiss.Unix()}})
		}
	}
	clear(al.sent)
	return jobs
}

// agentRegistered brings a newly registered card up to date: one started by
// push gets what changed since, one whose stretch is over its end — ended
// reports that (amu not held).
func (s *Service) agentRegistered(user, deviceID, handle string, pushStarted bool, since int64) (ended bool) {
	now := s.o.Now()
	var jobs []liveJob
	s.amu.Lock()
	al := s.agentOf(user)
	switch {
	case !al.active:
		// a card the app started from what it saw, before xbind did: read
		// the picture now — it may be on already, or the app saw it end
		s.amu.Unlock()
		s.agentRecompute(user)
		s.amu.Lock()
		al = s.agentOf(user)
		if !al.active {
			ended = true
			s.st.removeActivity(user, deviceID, AgentSession, handle)
			if since <= 0 || since > now.Unix()+60 {
				since = 0
			}
			jobs = append(jobs, liveJob{user: user, deviceID: deviceID, session: AgentSession, handle: handle, priority: 10,
				act: activityPush{Kind: agentKind, Event: "end", Timestamp: al.ts(now), State: ActivityState{Phase: PhaseIdle, Since: since},
					DismissalDate: now.Add(agentDismiss).Unix()}})
			break
		}
		fallthrough
	default:
		if since > 0 && since < al.since && since > now.Unix()-24*3600 {
			al.since = since // the card's clock, for a stretch it saw begin first
			al.state.Since = since
		}
		if pushStarted || al.sent[deviceID] != al.state {
			al.sent[deviceID] = al.state
			jobs = append(jobs, liveJob{user: user, deviceID: deviceID, session: AgentSession, handle: handle, priority: 10,
				act: activityPush{Kind: agentKind, Event: "update", Timestamp: al.ts(now), State: al.state, StaleDate: now.Add(activityStale).Unix()}})
		}
	}
	s.amu.Unlock()
	s.enqueueLive(jobs)
	return ended
}

// agentDismissed notes that the person swiped the card away on a device:
// no card comes back there by push for the rest of the stretch. False when
// no stretch is on.
func (s *Service) agentDismissed(user, deviceID string) bool {
	s.amu.Lock()
	defer s.amu.Unlock()
	al := s.agents[user]
	if al == nil || !al.active {
		return false
	}
	if al.dismissed == nil {
		al.dismissed = map[string]int{}
	}
	al.dismissed[deviceID] = al.stretch
	delete(al.sent, deviceID)
	return true
}

// agentLiveState reports the card's state for user (tests).
func (s *Service) agentLiveState(user string) (ActivityState, bool) {
	s.amu.Lock()
	defer s.amu.Unlock()
	if al := s.agents[user]; al != nil {
		return al.state, al.active
	}
	return ActivityState{}, false
}

// clearAgentStartHandle drops a device's workspace-agent push-to-start
// handle the relay refused (unless the app registered another meanwhile).
func (s *store) clearAgentStartHandle(user, deviceID, handle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User == user && x.DeviceID == deviceID && x.AgentStartHandle == handle && handle != "" {
			x.AgentStartHandle = ""
			_ = s.saveLocked()
			return true
		}
	}
	return false
}
