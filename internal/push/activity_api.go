package push

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
)

// The Live Activity routes (docs/protocol.md "Push"; activity.go).

var (
	sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	refRe       = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

// ownDevice resolves the caller's registration for deviceId under the
// rules of POST /devices/push: a device session acts for its own device
// id only, any other login for the registrations it made. Answered when
// false.
func (s *Service) ownDevice(w http.ResponseWriter, r *http.Request, user, deviceID string) (Device, bool) {
	p := auth.PrincipalOf(r)
	if p.Via == "device" && deviceID != p.DeviceID {
		fail(w, http.StatusForbidden, fmt.Sprintf("a device session acts for its own device id (%q)", p.DeviceID))
		return Device{}, false
	}
	d, ok := s.st.device(user, deviceID)
	switch {
	case !ok:
		fail(w, http.StatusNotFound, "no push registration for that device: POST /devices/push first")
		return Device{}, false
	case p.Via != "device" && d.Session != p.Gen:
		fail(w, http.StatusForbidden, "that registration belongs to another sign-in")
		return Device{}, false
	}
	return d, true
}

// APIActivity is POST /devices/push/activities {deviceId, session | ref,
// handle}: a Live Activity the device shows for one of the caller's agent
// sessions — handle is the relay's Live Activity handle of its update
// token. ref names one xbind started by push (the attributes' ref); the
// answer says which session it is. xbind then pushes the turn's state
// changes and its end to it.
func (s *Service) APIActivity(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	var body struct {
		DeviceID string `json:"deviceId"`
		Session  string `json:"session"`
		Ref      string `json:"ref"`
		Handle   string `json:"handle"`
	}
	if decode(r, &body) != nil {
		fail(w, http.StatusBadRequest, "need {deviceId, session | ref, handle}")
		return
	}
	switch {
	case !deviceIDRe.MatchString(body.DeviceID):
		fail(w, http.StatusBadRequest, "deviceId: 1–128 of A–Z a–z 0–9 . _ : -")
		return
	case !handleRe.MatchString(body.Handle):
		fail(w, http.StatusBadRequest, "handle: the relay's Live Activity handle (base64url)")
		return
	case (body.Session == "") == (body.Ref == ""):
		fail(w, http.StatusBadRequest, "session or ref: one of them")
		return
	case body.Session != "" && !sessionIDRe.MatchString(body.Session):
		fail(w, http.StatusBadRequest, "session: an agent session id")
		return
	case body.Ref != "" && !refRe.MatchString(body.Ref):
		fail(w, http.StatusBadRequest, "ref: the ref of an activity xbind started")
		return
	}
	if ok, wait := s.actReg.allow(user); !ok {
		tooMany(w, wait, "too many Live Activity registrations; try later")
		return
	}
	d, ok := s.ownDevice(w, r, user, body.DeviceID)
	if !ok {
		return
	}
	if body.Handle == d.Handle || body.Handle == d.StartHandle {
		fail(w, http.StatusBadRequest, "handle: an activity's own Live Activity handle, not the device's or its push-to-start handle")
		return
	}
	session, pushStarted := body.Session, false
	if body.Ref != "" {
		for _, a := range d.Activities {
			if a.Ref == body.Ref {
				session, pushStarted = a.Session, true
			}
		}
		if session == "" {
			fail(w, http.StatusNotFound, "no activity with that ref (it ended)")
			return
		}
	}
	// the session must be the caller's: its turns are nobody else's to watch
	if s.o.Session == nil {
		fail(w, http.StatusNotFound, "no such agent session")
		return
	}
	if info, ok := s.o.Session(session); !ok || info.Owner != user {
		fail(w, http.StatusNotFound, "no such agent session")
		return
	}
	a := Activity{Session: session, Handle: body.Handle, Ref: body.Ref, Created: s.o.Now().Unix()}
	if !s.st.setActivity(user, body.DeviceID, a) {
		fail(w, http.StatusNotFound, "no push registration for that device: POST /devices/push first")
		return
	}
	if s.Enabled() {
		s.activityRegistered(user, body.DeviceID, session, body.Handle, pushStarted)
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"activity": map[string]any{"session": session, "created": a.Created}})
}

// APIActivityDelete is DELETE /devices/push/{deviceId}/activities/{session}:
// the device stopped showing it (the user dismissed it); xbind stops
// pushing to it.
func (s *Service) APIActivityDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	dev, session := r.PathValue("deviceId"), r.PathValue("session")
	if _, ok := s.ownDevice(w, r, user, dev); !ok {
		return
	}
	if !s.st.removeActivity(user, dev, session, "") {
		fail(w, http.StatusNotFound, "no Live Activity registered for that session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
