package term

// A session's purpose (the D178 security review, L11): the Agent tab's guided
// sign-in runs a CLI's login in a shell session of its own that no tab
// shows. It used to hide by its name (xbin:sign-in), which anyone who may
// rename a session — a terminal token included — or an agent's own title
// could set, hiding any session from the tabs. Now the server marks it:
// /ws/term?purpose=signin opens a shell with purpose "signin", the listing
// carries it, clients hide shell rows with that purpose only, and the
// reserved name is never given to any other session (clients older than the
// purpose still hide by the name, so a sign-in session keeps it). A sign-in
// session ends by itself after Manager.SigninLife: nothing should outlive
// the sign-in it was opened for.

import (
	"errors"
	"log/slog"
	"strings"
	"time"
)

const (
	// PurposeSignin marks a guided sign-in's shell session.
	PurposeSignin = "signin"
	// SigninName is the name a sign-in session carries, for clients older
	// than its purpose; no other session takes it.
	SigninName = "xbin:sign-in"
	// signinLifeDefault bounds a sign-in session (Manager.SigninLife).
	signinLifeDefault = 15 * time.Minute
)

// errPurpose is an unknown ?purpose= (400).
var errPurpose = errors.New(`purpose: "signin" (a guided sign-in's shell) or none`)

// checkPurpose validates a /ws/term purpose.
func checkPurpose(p string) error {
	if p == "" || p == PurposeSignin {
		return nil
	}
	return errPurpose
}

// nameFor is the name a new session of this purpose starts with.
func nameFor(purpose string) string {
	if purpose == PurposeSignin {
		return SigninName
	}
	return ""
}

// ReservedName reports whether name is xbind's to give (SigninName, in any
// case, untrimmed or not): no rename and no agent's title sets it.
func ReservedName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), SigninName)
}

// RenameRefused says why renaming session id to name is refused (0 = it
// isn't): the reserved name (400), or a sign-in session (409; its name is
// how older clients hide it). An unknown id passes: the rename answers 404.
func (m *Manager) RenameRefused(id, name string) (status int, why string) {
	if ReservedName(name) {
		return 400, "the name " + SigninName + " is xbind's own (a guided sign-in's session)"
	}
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s != nil && s.purpose == PurposeSignin {
		return 409, "a guided sign-in's session can't be renamed"
	}
	return 0, ""
}

// PurposeOf is a live session's purpose ("" = none, or no such session):
// the `term` event carries it beside the deployment.
func (m *Manager) PurposeOf(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil {
		return s.purpose
	}
	return ""
}

// armPurpose starts what s's purpose asks of a session just registered: a
// sign-in session ends after SigninLife (default 15 minutes), unless it
// ended first.
func (m *Manager) armPurpose(s *Session) {
	if s.purpose != PurposeSignin {
		return
	}
	life := m.SigninLife
	if life <= 0 {
		life = signinLifeDefault
	}
	time.AfterFunc(life, func() {
		m.mu.Lock()
		live := m.sessions[s.ID] == s
		m.mu.Unlock()
		if live {
			slog.Info("ending a guided sign-in's session at its time", "id", s.ID, "after", life)
			s.kill() // pump's exit path removes it and tells the directory
		}
	})
}
