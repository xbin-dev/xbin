package push

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"slices"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
)

// Live Activities (native/spec/push.md §7; the relay: relay/README.md §Live
// Activities). The app shows an agent turn on the lock screen and in the
// Dynamic Island. While it runs, the app updates the activity itself from
// the session's events; for the rest of the time xbind does, through the
// relay: each device registers the relay handle of every activity's
// ActivityKit update token (POST /devices/push/activities) and of its
// push-to-start token (POST /devices/push {startHandle}), and xbind pushes
// the turn's state — running / waiting / idle, since when, how many
// requests wait — to them. Nothing else: an ActivityKit payload can't be
// sealed, so no title, name or text goes this way (the relay would refuse
// it).
//
// xbind follows each agent session of a user with a registered device from
// its events (status, permission and question requests and answers, turn
// ends): a state change updates the session's activities; a turn's end
// ends them (and the registrations go — an activity is one turn); a turn
// still busy after Options.StartAfter starts one by push on each device
// with a push-to-start handle that shows none for the session yet.

// Activity is one Live Activity a device shows for an agent session: the
// relay handle of its update token. Handle "" = xbind started it by push
// and the app has not registered its token yet; the app registers it under
// Ref, the reference the start carried.
type Activity struct {
	Session string `json:"session"`
	Handle  string `json:"handle,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Created int64  `json:"created"`
}

// ActivityState is a Live Activity's content state (the relay's
// ActivityState, the app's AgentActivityState).
type ActivityState struct {
	Phase   string `json:"phase"`   // running | waiting | idle
	Since   int64  `json:"since"`   // unix seconds the turn started (0 = unknown)
	Pending int    `json:"pending"` // permission requests and questions waiting (0–99)
}

// Phases.
const (
	PhaseRunning = "running"
	PhaseWaiting = "waiting"
	PhaseIdle    = "idle"
)

// KindAgentActivity is the kind a push-to-start counts as: a device whose
// registration kinds leave out "agent" gets no Live Activity started by
// push (the ones the app starts itself are its own choice).
const KindAgentActivity = "agent.activity"

const (
	maxActivitiesPerDevice = 16
	// activityStale marks a running activity stale if xbind says nothing
	// for this long (a stopped xbind must not leave "running" up for good).
	activityStale = 4 * time.Hour
	// activityDismiss is how long an ended activity stays on the lock screen.
	activityDismiss = 10 * time.Minute
)

// activityPush is the relay's "activity" (relay.ActivityPush).
type activityPush struct {
	Event         string        `json:"event"`
	Timestamp     int64         `json:"timestamp"`
	State         ActivityState `json:"state"`
	StaleDate     int64         `json:"staleDate,omitempty"`
	DismissalDate int64         `json:"dismissalDate,omitempty"`
	WS            string        `json:"ws,omitempty"`
	Ref           string        `json:"ref,omitempty"`
}

// liveJob is one Live Activity push to one device.
type liveJob struct {
	user, deviceID, session string
	handle                  string
	start                   bool // to the device's push-to-start handle
	act                     activityPush
	priority                int
}

// liveSession is what xbind knows of one agent session's turn.
type liveSession struct {
	user     string
	busy     bool
	status   string
	turn     int   // turns seen start (a push-to-start timer names its turn)
	since    int64 // unix seconds the turn started
	promptMS int64 // the last user prompt (unix ms): a turn's start when its running status follows
	pids     map[string]bool
	eids     map[string]bool
	state    ActivityState
	lastTS   int64                    // the last activity timestamp (strictly increasing)
	sent     map[string]ActivityState // deviceID → what xbind last sent (or started) there
	timer    *time.Timer              // push-to-start
}

func busyStatus(s string) bool {
	return s == agent.StatusRunning || s == agent.StatusWaiting || s == agent.StatusCancelling
}

func (ls *liveSession) compute() ActivityState {
	if !ls.busy {
		return ActivityState{Phase: PhaseIdle, Since: ls.since}
	}
	n := min(len(ls.pids)+len(ls.eids), 99)
	ph := PhaseRunning
	if ls.status == agent.StatusWaiting || n > 0 {
		ph = PhaseWaiting
	}
	return ActivityState{Phase: ph, Since: ls.since, Pending: n}
}

// ts is the next activity timestamp: now, but always after the last one
// (the device drops an update no newer than what it shows).
func (ls *liveSession) ts(now time.Time) int64 {
	t := max(now.Unix(), ls.lastTS+1)
	ls.lastTS = t
	return t
}

// activityEvent follows an agent session for its Live Activities (the
// AgentEvent hook; cheap for the events it does not need).
func (s *Service) activityEvent(user, session string, ev agent.Event) {
	switch ev.Type {
	case agent.EvStatus, agent.EvPermissionRequest, agent.EvPermissionResolved,
		agent.EvElicitRequest, agent.EvElicitResolved, agent.EvTurnEnd:
	case agent.EvMessageDelta:
		if !bytes.Contains(ev.Data, []byte(`"user"`)) {
			return // the agent's own text, streaming: not a prompt
		}
	default:
		return
	}
	if user == "" || session == "" || !s.Enabled() || !s.st.hasDevices(user) {
		return
	}
	var d struct {
		Status string          `json:"status"`
		Role   string          `json:"role"`
		Parent string          `json:"parent"`
		PID    json.RawMessage `json:"pid"`
		EID    json.RawMessage `json:"eid"`
	}
	_ = json.Unmarshal(ev.Data, &d)
	now := s.o.Now()
	var jobs []liveJob
	s.lmu.Lock()
	ls := s.turns[session]
	if ls == nil {
		ls = &liveSession{user: user, pids: map[string]bool{}, eids: map[string]bool{}, sent: map[string]ActivityState{}}
		s.turns[session] = ls
	}
	was := ls.busy
	switch ev.Type {
	case agent.EvMessageDelta:
		if d.Role == "user" && d.Parent == "" {
			ls.promptMS = ev.TS
		}
	case agent.EvStatus:
		if d.Status != "" { // a partial status (commands, usage, title) says nothing of the turn
			ls.status, ls.busy = d.Status, busyStatus(d.Status)
		}
	case agent.EvPermissionRequest:
		ls.pids[rawID(d.PID)] = true
	case agent.EvPermissionResolved:
		delete(ls.pids, rawID(d.PID))
	case agent.EvElicitRequest:
		ls.eids[rawID(d.EID)] = true
	case agent.EvElicitResolved:
		delete(ls.eids, rawID(d.EID))
	case agent.EvTurnEnd:
		ls.busy = false
	}
	switch {
	case ls.busy && !was: // a turn starts
		ls.turn++
		start := ev.TS
		if ls.promptMS != 0 && ev.TS-ls.promptMS < 10_000 {
			start = ls.promptMS // the prompt that began it
		}
		if start <= 0 {
			start = now.UnixMilli()
		}
		ls.since = start / 1000
		ls.state = ls.compute()
		s.armStart(session, ls)
	case !ls.busy && was: // the turn ended
		jobs = s.endLocked(session, ls, now)
		ls.pids, ls.eids = map[string]bool{}, map[string]bool{}
	case ls.busy:
		if st := ls.compute(); st != ls.state {
			ls.state = st
			jobs = s.updateLocked(session, ls, now)
		}
	}
	if !ls.busy && (ls.status == agent.StatusError || ls.status == agent.StatusExited) {
		delete(s.turns, session) // over for good
	}
	s.lmu.Unlock()
	s.enqueueLive(jobs)
}

// armStart schedules the push-to-start of this turn (lmu held).
func (s *Service) armStart(session string, ls *liveSession) {
	if ls.timer != nil {
		ls.timer.Stop()
		ls.timer = nil
	}
	after := s.o.StartAfter
	if after < 0 {
		return
	}
	if after == 0 {
		after = 30 * time.Second
	}
	turn := ls.turn
	ls.timer = time.AfterFunc(after, func() { s.pushToStart(session, turn) })
}

// pushToStart starts the turn's activity by push on each device that can
// and shows none for the session yet.
func (s *Service) pushToStart(session string, turn int) {
	now := s.o.Now()
	var jobs []liveJob
	s.lmu.Lock()
	ls := s.turns[session]
	if ls == nil || !ls.busy || ls.turn != turn {
		s.lmu.Unlock()
		return
	}
	ls.timer = nil
	if _, ok := s.account(ls.user); !ok {
		s.lmu.Unlock()
		return
	}
	e := s.currentEpoch()
	var ts int64
	for _, d := range s.devices(ls.user) {
		if d.StartHandle == "" || d.stale(e) || !kindAllowed(d.Kinds, KindAgentActivity) ||
			slices.ContainsFunc(d.Activities, func(a Activity) bool { return a.Session == session }) {
			continue
		}
		ref := randomRef()
		if !s.st.setActivity(ls.user, d.DeviceID, Activity{Session: session, Ref: ref, Created: now.Unix()}) {
			continue
		}
		if ts == 0 {
			ts = ls.ts(now)
		}
		ls.sent[d.DeviceID] = ls.state
		jobs = append(jobs, liveJob{user: ls.user, deviceID: d.DeviceID, session: session, handle: d.StartHandle, start: true,
			priority: 10, act: activityPush{Event: "start", Timestamp: ts, State: ls.state,
				StaleDate: now.Add(activityStale).Unix(), WS: s.Workspace(), Ref: ref}})
	}
	s.lmu.Unlock()
	s.enqueueLive(jobs)
}

// updateLocked pushes ls.state to the session's registered activities
// (lmu held). Waiting asks for the user: priority 10; the rest 5.
func (s *Service) updateLocked(session string, ls *liveSession, now time.Time) []liveJob {
	prio := 5
	if ls.state.Phase == PhaseWaiting {
		prio = 10
	}
	var jobs []liveJob
	var ts int64
	for _, d := range s.devices(ls.user) {
		for _, a := range d.Activities {
			if a.Session != session || a.Handle == "" || ls.sent[d.DeviceID] == ls.state {
				continue
			}
			if ok, _ := s.actPush.allow(a.Handle); !ok {
				s.snd.limited.Add(1)
				continue
			}
			if ts == 0 {
				ts = ls.ts(now)
			}
			ls.sent[d.DeviceID] = ls.state
			jobs = append(jobs, liveJob{user: ls.user, deviceID: d.DeviceID, session: session, handle: a.Handle, priority: prio,
				act: activityPush{Event: "update", Timestamp: ts, State: ls.state, StaleDate: now.Add(activityStale).Unix()}})
		}
	}
	return jobs
}

// endLocked ends the session's activities — idle, with the turn's start —
// and drops their registrations: an activity is one turn (lmu held).
func (s *Service) endLocked(session string, ls *liveSession, now time.Time) []liveJob {
	if ls.timer != nil {
		ls.timer.Stop()
		ls.timer = nil
	}
	ls.state = ActivityState{Phase: PhaseIdle, Since: ls.since}
	var jobs []liveJob
	var ts int64
	for _, d := range s.devices(ls.user) {
		for _, a := range d.Activities {
			if a.Session != session {
				continue
			}
			s.st.removeActivity(ls.user, d.DeviceID, session, a.Handle)
			if a.Handle == "" {
				continue // started by push, its token never came: nothing to reach
			}
			if ts == 0 {
				ts = ls.ts(now)
			}
			jobs = append(jobs, liveJob{user: ls.user, deviceID: d.DeviceID, session: session, handle: a.Handle, priority: 10,
				act: activityPush{Event: "end", Timestamp: ts, State: ls.state, DismissalDate: now.Add(activityDismiss).Unix()}})
		}
	}
	clear(ls.sent)
	return jobs
}

// SessionClosed ends what a closed session's activities show (the term
// directory's close: a session may go without a last status event).
func (s *Service) SessionClosed(session string) {
	now := s.o.Now()
	s.lmu.Lock()
	ls := s.turns[session]
	delete(s.turns, session)
	var jobs []liveJob
	if ls != nil {
		ls.busy = false
		jobs = s.endLocked(session, ls, now)
	}
	s.lmu.Unlock()
	s.enqueueLive(jobs)
}

// EndActivities ends every registered Live Activity and drops the
// registrations: at start, since agent sessions do not outlive xbind.
func (s *Service) EndActivities() {
	now := s.o.Now()
	var jobs []liveJob
	for _, d := range s.st.all() {
		for _, a := range d.Activities {
			s.st.removeActivity(d.User, d.DeviceID, a.Session, a.Handle)
			if a.Handle != "" && s.Enabled() {
				jobs = append(jobs, liveJob{user: d.User, deviceID: d.DeviceID, session: a.Session, handle: a.Handle, priority: 5,
					act: activityPush{Event: "end", Timestamp: now.Unix(), State: ActivityState{Phase: PhaseIdle},
						DismissalDate: now.Add(activityDismiss).Unix()}})
			}
		}
	}
	s.enqueueLive(jobs)
}

// activityRegistered brings a newly registered activity up to date: a
// push-started one gets what changed since its start, one whose turn
// already ended gets its end (lmu not held). since is the card's turn
// start, taken only when xbind did not see the turn begin.
func (s *Service) activityRegistered(user, deviceID, session, handle string, pushStarted bool, since int64) {
	now := s.o.Now()
	var jobs []liveJob
	s.lmu.Lock()
	ls := s.turns[session]
	if ls == nil && s.o.Session != nil {
		// a turn xbind did not follow (the user had no device when it
		// began): what the session directory says, pending counts unknown
		if info, ok := s.o.Session(session); ok && busyStatus(info.Status) {
			ls = &liveSession{user: user, busy: true, status: info.Status, turn: 1, pids: map[string]bool{},
				eids: map[string]bool{}, sent: map[string]ActivityState{}}
			s.turns[session] = ls
		}
	}
	if ls != nil && ls.busy && ls.since == 0 && since > 0 && since <= now.Unix()+60 {
		ls.since = since // the card's clock, for a turn xbind did not see begin
		ls.state = ls.compute()
	} else if ls != nil && ls.busy && ls.state.Phase == "" {
		ls.state = ls.compute()
	}
	switch {
	case ls == nil || !ls.busy:
		since := int64(0)
		if ls != nil {
			since = ls.since
		}
		s.st.removeActivity(user, deviceID, session, handle)
		ts := now.Unix()
		if ls != nil {
			ts = ls.ts(now)
		}
		jobs = append(jobs, liveJob{user: user, deviceID: deviceID, session: session, handle: handle, priority: 10,
			act: activityPush{Event: "end", Timestamp: ts, State: ActivityState{Phase: PhaseIdle, Since: since},
				DismissalDate: now.Add(activityDismiss).Unix()}})
	case pushStarted && ls.sent[deviceID] != ls.state:
		ls.sent[deviceID] = ls.state
		jobs = append(jobs, liveJob{user: user, deviceID: deviceID, session: session, handle: handle, priority: 10,
			act: activityPush{Event: "update", Timestamp: ls.ts(now), State: ls.state, StaleDate: now.Add(activityStale).Unix()}})
	default:
		ls.sent[deviceID] = ls.state // the app started it with what it saw
	}
	s.lmu.Unlock()
	s.enqueueLive(jobs)
}

func (s *Service) enqueueLive(jobs []liveJob) {
	for i := range jobs {
		s.snd.enqueue(job{live: &jobs[i]})
	}
}

// liveState reports what xbind knows of a session's turn (tests, docs).
func (s *Service) liveState(session string) (ActivityState, bool) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	if ls := s.turns[session]; ls != nil {
		return ls.state, ls.busy
	}
	return ActivityState{}, false
}

func randomRef() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return b64.EncodeToString(b)
}

// --- the store's side ---

// device returns a copy of one registration.
func (s *store) device(user, deviceID string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User == user && x.DeviceID == deviceID {
			return x.clone(), true
		}
	}
	return Device{}, false
}

// setActivity registers a device's activity for a session (replacing the
// session's earlier one); the oldest go past maxActivitiesPerDevice.
// False when the device has no registration.
func (s *store) setActivity(user, deviceID string, a Activity) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User != user || x.DeviceID != deviceID {
			continue
		}
		acts := slices.DeleteFunc(slices.Clone(x.Activities), func(o Activity) bool { return o.Session == a.Session })
		acts = append(acts, a)
		if len(acts) > maxActivitiesPerDevice {
			slices.SortStableFunc(acts, func(p, q Activity) int { return int(p.Created - q.Created) })
			acts = acts[len(acts)-maxActivitiesPerDevice:]
		}
		x.Activities = acts
		_ = s.saveLocked()
		return true
	}
	return false
}

// removeActivity drops a device's activity for a session (handle "" or
// its handle; a newer registration for the session stays).
func (s *store) removeActivity(user, deviceID, session, handle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User != user || x.DeviceID != deviceID {
			continue
		}
		n := len(x.Activities)
		x.Activities = slices.DeleteFunc(slices.Clone(x.Activities), func(a Activity) bool {
			return a.Session == session && (handle == "" || a.Handle == handle)
		})
		if len(x.Activities) == 0 {
			x.Activities = nil
		}
		if len(x.Activities) != n {
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// clearStartHandle drops a device's push-to-start handle the relay
// refused (unless the app registered another meanwhile).
func (s *store) clearStartHandle(user, deviceID, handle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User == user && x.DeviceID == deviceID && x.StartHandle == handle && handle != "" {
			x.StartHandle = ""
			_ = s.saveLocked()
			return true
		}
	}
	return false
}
