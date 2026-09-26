package relay

import (
	"encoding/json"
	"regexp"
	"sort"
	"time"
)

// Live Activities (native/spec/push.md §7). ActivityKit gives the app one
// push token per activity (its updates) and one for the app (push-to-start).
// The app registers each as a Live Activity handle hanging off the device
// handle it made for the same workspace (POST /v1/handles {pushType:
// "liveactivity", parent}); the workspace then pushes content states to it
// (POST /v1/push {type: "liveactivity", activity}).
//
// An ActivityKit payload can't be sealed: the system decodes the content
// state and draws it before any code of the app's runs. So the relay does
// not forward a payload at all — it builds the APNs body itself from a few
// enumerated and numeric fields, and the only strings that pass are two
// opaque ids of a push-to-start. No title, text or path reaches APNs this
// way; what a workspace can say through it is "running / waiting / idle,
// since, N pending".

// PushTypeLiveActivity is the handle type and push type of Live Activities.
const PushTypeLiveActivity = "liveactivity"

// ActivityAttributesType is the app's ActivityAttributes type — the
// "attributes-type" of a push-to-start (native/ios: XbinAgent's
// AgentActivityAttributes).
const ActivityAttributesType = "AgentActivityAttributes"

const (
	// maxChildren is how many Live Activity handles one device handle
	// holds; a new one evicts the least recently used.
	maxChildren = 16
	maxPending  = 99
	// liveTopicSuffix makes the APNs topic of a Live Activity push.
	liveTopicSuffix = ".push-type.liveactivity"
)

var liveRefRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ActivityState is a Live Activity's content state — the app's
// AgentActivityState, key for key.
type ActivityState struct {
	Phase   string `json:"phase"`   // running | waiting | idle
	Since   int64  `json:"since"`   // unix seconds the turn started (0 = unknown)
	Pending int    `json:"pending"` // requests waiting for the user (0–99)
}

// ActivityPush is the "activity" of a liveactivity push.
type ActivityPush struct {
	Event         string         `json:"event"`     // update | end | start
	Timestamp     int64          `json:"timestamp"` // unix seconds; the device ignores anything older than what it shows
	State         *ActivityState `json:"state"`
	StaleDate     int64          `json:"staleDate,omitempty"`     // unix seconds
	DismissalDate int64          `json:"dismissalDate,omitempty"` // end only
	// start only: opaque ids the app maps back (xbind's push id for the
	// workspace, and the reference the app registers the new activity's
	// token under)
	WS  string `json:"ws,omitempty"`
	Ref string `json:"ref,omitempty"`
}

// check validates a with the relay's clock; "" = fine.
func (a *ActivityPush) check(now time.Time) string {
	soon := now.Add(time.Hour).Unix()
	later := now.Add(24 * time.Hour).Unix()
	switch {
	case a.Event != "update" && a.Event != "end" && a.Event != "start":
		return "activity.event: update | end | start"
	case a.Timestamp <= 0 || a.Timestamp > soon:
		return "activity.timestamp: unix seconds, not in the future"
	case a.State == nil:
		return "activity.state: {phase, since, pending}"
	case a.State.Phase != "running" && a.State.Phase != "waiting" && a.State.Phase != "idle":
		return "activity.state.phase: running | waiting | idle"
	case a.State.Since < 0 || a.State.Since > soon:
		return "activity.state.since: unix seconds"
	case a.State.Pending < 0 || a.State.Pending > maxPending:
		return "activity.state.pending: 0–99"
	case a.StaleDate < 0 || a.StaleDate > later:
		return "activity.staleDate: unix seconds, within a day"
	case a.DismissalDate < 0 || a.DismissalDate > later || (a.DismissalDate != 0 && a.Event != "end"):
		return "activity.dismissalDate: unix seconds within a day, end only"
	}
	if a.Event == "start" {
		if !liveRefRe.MatchString(a.WS) || !liveRefRe.MatchString(a.Ref) {
			return "activity.ws, activity.ref: 1–64 of A–Z a–z 0–9 _ - (start)"
		}
	} else if a.WS != "" || a.Ref != "" {
		return "activity.ws, activity.ref: start only"
	}
	return ""
}

// startAlert is the generic alert of a push-to-start (ActivityKit shows one
// when an activity starts by push), by phase.
func startAlert(phase string) map[string]string {
	body := "An agent is working."
	switch phase {
	case "waiting":
		body = "An agent is waiting for you."
	case "idle":
		body = "An agent finished."
	}
	return map[string]string{"title": "xbin", "body": body}
}

// activityBody builds the APNs body of a liveactivity push from checked
// fields only.
func activityBody(a *ActivityPush) ([]byte, error) {
	aps := map[string]any{
		"timestamp":     a.Timestamp,
		"event":         a.Event,
		"content-state": ActivityState{Phase: a.State.Phase, Since: a.State.Since, Pending: a.State.Pending},
	}
	if a.StaleDate != 0 {
		aps["stale-date"] = a.StaleDate
	}
	if a.DismissalDate != 0 {
		aps["dismissal-date"] = a.DismissalDate
	}
	if a.Event == "start" {
		aps["attributes-type"] = ActivityAttributesType
		// the display names are the app's to fill in (from its own records
		// of ws); a push never carries them
		aps["attributes"] = map[string]string{"ws": a.WS, "ref": a.Ref, "workspace": "", "session": "",
			"appWorkspace": "", "sessionID": ""}
		aps["alert"] = startAlert(a.State.Phase)
		// ask for the new activity's update token (the app registers it)
		aps["input-push-token"] = 1
	}
	return json.Marshal(map[string]any{"aps": aps})
}

// newChild stores a Live Activity handle under the device handle parent.
// The same token under the same parent is the same handle (the app
// re-registers after a restart); a parent holds at most maxChildren.
func (s *store) newChild(parent, token, topic, env string, now time.Time) (string, error) {
	s.mu.Lock()
	recs := s.sweepLocked(now, false)
	p := s.st.Handles[parent]
	switch {
	case p == nil:
		s.mu.Unlock()
		_ = s.write(recs)
		return "", errNoHandle
	case p.Type != "" || p.Topic != topic || p.Env != env:
		s.mu.Unlock()
		_ = s.write(recs)
		return "", errHandleType
	}
	for c := range s.children[parent] {
		if h := s.st.Handles[c]; h != nil && h.Token == token {
			s.mu.Unlock()
			return c, s.write(recs)
		}
	}
	if len(s.st.Handles) >= s.lim.maxHandles {
		recs = append(recs, s.sweepLocked(now, true)...)
		if len(s.st.Handles) >= s.lim.maxHandles || s.st.Handles[parent] == nil {
			s.mu.Unlock()
			_ = s.write(recs)
			return "", errFull
		}
	}
	if kids := s.children[parent]; len(kids) >= maxChildren {
		ids := make([]string, 0, len(kids))
		for c := range kids {
			ids = append(ids, c)
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := s.st.Handles[ids[i]], s.st.Handles[ids[j]]
			return max(a.Used, a.Created) < max(b.Used, b.Created)
		})
		for _, c := range ids[:len(ids)-maxChildren+1] {
			recs = append(recs, s.delTree(c)...)
		}
	}
	id := randomID(16)
	s.st.Handles[id] = &Handle{Token: token, Topic: topic, Env: env, Type: PushTypeLiveActivity, Parent: parent, Created: now.Unix()}
	s.link(parent, id)
	recs = append(recs, s.putH(id))
	s.mu.Unlock()
	return id, s.write(recs)
}

// handleType reports a handle's type ("" or "liveactivity").
func (s *store) handleType(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.st.Handles[id]; h != nil {
		return h.Type, true
	}
	return "", false
}
