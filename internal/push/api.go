package push

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
)

// The HTTP surface (docs/protocol.md §/api/xbin, "Push"). The routes are
// mounted by internal/boot/push.go.

const docs = "/docs/protocol.md"

var (
	deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	handleRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	kindRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
	tileKindRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	collapseRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

func fail(w http.ResponseWriter, code int, msg string) { server.WriteError(w, code, msg, docs) }

func tooMany(w http.ResponseWriter, wait time.Duration, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
	fail(w, http.StatusTooManyRequests, msg)
}

// decode reads a JSON body leniently: a newer app or SDK may send fields an
// older xbind does not know.
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v)
}

func (s *Service) human(w http.ResponseWriter, r *http.Request) (string, bool) {
	user, ok := UserKey(auth.PrincipalOf(r))
	if !ok {
		fail(w, http.StatusForbidden, "push registrations and preferences belong to a signed-in person (the app's device session), not a tile")
	}
	return user, ok
}

type deviceView struct {
	User     string   `json:"user,omitempty"` // the admin listing only
	DeviceID string   `json:"deviceId"`
	Kinds    []string `json:"kinds"`
	Created  int64    `json:"created"`
	Updated  int64    `json:"updated"`
	LastSent int64    `json:"lastSent,omitempty"`
	// NeedsNewHandle: the relay will not deliver to this handle for this
	// workspace any more (it is bound to an earlier relay registration of
	// the workspace, or the relay no longer knows it) — the app creates a
	// fresh handle at the relay and registers again.
	NeedsNewHandle bool   `json:"needsNewHandle,omitempty"`
	RelayError     string `json:"relayError,omitempty"` // handle_bound | handle_unknown
}

func view(d Device, epoch string) deviceView {
	k := d.Kinds
	if k == nil {
		k = []string{}
	}
	v := deviceView{DeviceID: d.DeviceID, Kinds: k, Created: d.Created, Updated: d.Updated, LastSent: d.LastSent}
	if d.stale(epoch) {
		v.NeedsNewHandle, v.RelayError = true, d.RelayErr
		if v.RelayError == "" || d.ErrEpoch != epoch {
			v.RelayError = "handle_bound" // delivered under an earlier relay registration
		}
	}
	return v
}

// APIRegister is POST /devices/push {deviceId, handle, publicKey, kinds?}:
// the app registers (or refreshes) where this user's pushes go.
func (s *Service) APIRegister(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	var body struct {
		DeviceID  string   `json:"deviceId"`
		Handle    string   `json:"handle"`
		PublicKey string   `json:"publicKey"`
		Kinds     []string `json:"kinds"`
	}
	if decode(r, &body) != nil {
		fail(w, http.StatusBadRequest, "need {deviceId, handle, publicKey, kinds?}")
		return
	}
	switch {
	case !deviceIDRe.MatchString(body.DeviceID):
		fail(w, http.StatusBadRequest, "deviceId: 1–128 of A–Z a–z 0–9 . _ : -")
		return
	case !handleRe.MatchString(body.Handle):
		fail(w, http.StatusBadRequest, "handle: the relay's handle (base64url)")
		return
	case len(body.Kinds) > 32:
		fail(w, http.StatusBadRequest, "kinds: at most 32")
		return
	}
	pub, err := ParsePublicKey(body.PublicKey)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var kinds []string
	for _, k := range body.Kinds {
		if !kindRe.MatchString(k) {
			fail(w, http.StatusBadRequest, fmt.Sprintf("kinds: %q is not a kind (agent, agent.permission, agent.question, agent.turn, tile, tile.<kind>)", k))
			return
		}
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	d, err := s.st.upsert(Device{User: user, DeviceID: body.DeviceID, Handle: body.Handle,
		PublicKey: b64.EncodeToString(pub), Kinds: kinds}, s.o.Now().Unix())
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not store the registration: "+err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"device": view(d, s.currentEpoch()), "workspace": s.Workspace(), "enabled": s.Enabled()})
}

// APIList is GET /devices/push: the caller's registrations.
func (s *Service) APIList(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	out := []deviceView{}
	e := s.currentEpoch()
	for _, d := range s.st.devices(user) {
		out = append(out, view(d, e))
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"workspace": s.Workspace(), "enabled": s.Enabled(), "devices": out})
}

// APIUnregister is DELETE /devices/push/{deviceId}.
func (s *Service) APIUnregister(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	if !s.st.remove(user, r.PathValue("deviceId")) {
		fail(w, http.StatusNotFound, "no push registration for that device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// APIPrefs is GET /push/prefs.
func (s *Service) APIPrefs(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	server.WriteJSON(w, http.StatusOK, s.st.prefs(user))
}

// APISetPrefs is PUT /push/prefs {mutedTiles}: replaces the caller's
// preferences.
func (s *Service) APISetPrefs(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	var p Prefs
	if decode(r, &p) != nil || len(p.MutedTiles) > 1000 {
		fail(w, http.StatusBadRequest, "need {mutedTiles: [tile path, …]} (at most 1000)")
		return
	}
	var clean []string
	for _, t := range p.MutedTiles {
		t = strings.Trim(t, "/")
		if t == "" || len(t) > 256 || strings.ContainsAny(t, "\x00\n\r") {
			fail(w, http.StatusBadRequest, fmt.Sprintf("mutedTiles: %q is not a tile path", t))
			return
		}
		if !slices.Contains(clean, t) {
			clean = append(clean, t)
		}
	}
	slices.Sort(clean)
	if err := s.st.setPrefs(user, Prefs{MutedTiles: clean}); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, s.st.prefs(user))
}

// APITest is POST /push/test: a test notification to the caller's devices.
func (s *Service) APITest(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	if !s.Enabled() {
		fail(w, http.StatusConflict, "push is off on this workspace (an admin turns it on: PUT /api/xbin/push/config)")
		return
	}
	n, stale := 0, 0
	e := s.currentEpoch()
	for _, d := range s.st.devices(user) {
		if d.stale(e) {
			stale++
		} else {
			n++
		}
	}
	switch {
	case n == 0 && stale > 0:
		fail(w, http.StatusConflict, "every registered device needs a new relay handle (needsNewHandle)")
		return
	case n == 0:
		fail(w, http.StatusConflict, "no device is registered for push")
		return
	}
	if ok, wait := s.self.allow(user); !ok {
		tooMany(w, wait, "too many test notifications; try later")
		return
	}
	if !s.enqueue(note{user: user, kind: KindTest, title: "xbin", body: "Test notification — push works."}) {
		fail(w, http.StatusServiceUnavailable, "the push queue is full; try later")
		return
	}
	server.WriteJSON(w, http.StatusAccepted, map[string]any{"devices": n})
}

// notifier resolves who may call POST /notify: a tile's backend (instance
// token) notifies any reader of the tile; its frontend (frame token) or a
// shell in it (terminal token) acts for the person using it and may notify
// only them — else any reader could send pushes "from the tile" to anyone.
// self is that person ("" for a backend).
func notifier(p auth.Principal) (tile, self string, ok bool) {
	if p.Component == "" {
		return "", "", false
	}
	switch {
	case p.Via == "instance":
		return p.Component, "", true
	case p.UserID != "":
		return p.Component, p.UserID, true
	case p.Via == "frame" || p.Via == "terminal":
		return p.Component, OwnerUser, true // the bootstrap owner's (or the tile's own) token
	}
	return "", "", false
}

// APINotify is POST /notify {user, title, body?, link?, kind?, collapseId?}
// for tile backends: a notification to a person who can read the calling
// tile. A tile's frontend may notify only the person using it.
func (s *Service) APINotify(w http.ResponseWriter, r *http.Request) {
	tile, self, ok := notifier(auth.PrincipalOf(r))
	if !ok {
		fail(w, http.StatusForbidden, "notify is called by a tile's backend: the notification names the tile it comes from")
		return
	}
	var body struct {
		User       string `json:"user"`
		Title      string `json:"title"`
		Body       string `json:"body"`
		Link       string `json:"link"`
		Kind       string `json:"kind"`
		CollapseID string `json:"collapseId"`
	}
	if decode(r, &body) != nil {
		fail(w, http.StatusBadRequest, "need {user, title, body?, link?, kind?, collapseId?}")
		return
	}
	user := strings.TrimPrefix(strings.TrimSpace(body.User), "user:")
	title := strings.TrimSpace(body.Title)
	link, linkOK := tileLink(body.Link)
	switch {
	case user == "" || len(user) > 64:
		fail(w, http.StatusBadRequest, "user: the id of the person to notify")
		return
	case title == "":
		fail(w, http.StatusBadRequest, "title: required")
		return
	case !linkOK:
		fail(w, http.StatusBadRequest, "link: relative to the tile (#fragment, ?query or a path inside it)")
		return
	case body.Kind != "" && !tileKindRe.MatchString(body.Kind):
		fail(w, http.StatusBadRequest, "kind: 1–32 of a–z 0–9 -")
		return
	case body.CollapseID != "" && !collapseRe.MatchString(body.CollapseID):
		fail(w, http.StatusBadRequest, "collapseId: 1–64 of A–Z a–z 0–9 . _ : -")
		return
	case self != "" && user != self:
		fail(w, http.StatusForbidden, "a tile's frontend can notify only the person using it; notify others from the tile's backend")
		return
	}
	accepted := func() { server.WriteJSON(w, http.StatusAccepted, map[string]any{"ok": true}) }
	// muted: nothing is sent and nothing is charged
	if s.st.muted(user, tile) {
		accepted()
		return
	}
	// the tile's budget next: refused calls spend it too, so a tile cannot
	// probe who reads it without bound
	if ok, wait := s.tile.allow(tile); !ok {
		tooMany(w, wait, "this tile is sending too many notifications; try later")
		return
	}
	if !s.o.CanRead(user, tile) {
		fail(w, http.StatusForbidden, "that user cannot read this tile")
		return
	}
	n := note{user: user, tile: tile, kind: KindTile, title: title, body: body.Body, link: "c/" + tile + "/" + link}
	if body.Kind != "" {
		n.kind += "." + body.Kind
	}
	if body.CollapseID != "" {
		n.collapse = "tile:" + tile + ":" + body.CollapseID
	}
	// nothing would reach a device: spend nothing of the person's budget
	if !s.Enabled() || !s.wants(user, n.kind) {
		accepted()
		return
	}
	// what every tile together sends this person; over it the notification
	// is dropped quietly — a 429 would tell this tile how much the others
	// send (the tile's own limit above is its backpressure)
	if ok, _ := s.user.allow(user); !ok {
		s.snd.limited.Add(1)
		accepted()
		return
	}
	s.enqueue(n)
	accepted()
}

// tileLink validates a link relative to the tile: a fragment, a query, or
// a path inside it — never a scheme, an absolute path or a way out. Path
// segments are checked percent-decoded: a URL parser resolves %2e%2e as
// "..", so "%2e%2e/%2e%2e/login" would leave the tile.
func tileLink(l string) (string, bool) {
	if l == "" {
		return "", true
	}
	if len(l) > 400 || strings.HasPrefix(l, "/") {
		return "", false
	}
	for _, c := range l {
		if c < 0x20 || c == 0x7f || c == '\\' || c == ' ' {
			return "", false
		}
	}
	head := l
	if i := strings.IndexAny(l, "/?#"); i >= 0 {
		head = l[:i]
	}
	if strings.Contains(head, ":") {
		return "", false
	}
	p := l
	if i := strings.IndexAny(l, "?#"); i >= 0 {
		p = l[:i]
	}
	for _, seg := range strings.Split(p, "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil || dec == ".." || dec == "." || strings.ContainsAny(dec, "/\\") {
			return "", false
		}
	}
	return l, true
}
