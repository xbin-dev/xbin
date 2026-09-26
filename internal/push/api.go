package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	DeviceID string   `json:"deviceId"`
	Kinds    []string `json:"kinds"`
	Created  int64    `json:"created"`
	Updated  int64    `json:"updated"`
	LastSent int64    `json:"lastSent,omitempty"`
}

func view(d Device) deviceView {
	k := d.Kinds
	if k == nil {
		k = []string{}
	}
	return deviceView{DeviceID: d.DeviceID, Kinds: k, Created: d.Created, Updated: d.Updated, LastSent: d.LastSent}
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
	server.WriteJSON(w, http.StatusOK, map[string]any{"device": view(d), "workspace": s.Workspace(), "enabled": s.Enabled()})
}

// APIList is GET /devices/push: the caller's registrations.
func (s *Service) APIList(w http.ResponseWriter, r *http.Request) {
	user, ok := s.human(w, r)
	if !ok {
		return
	}
	out := []deviceView{}
	for _, d := range s.st.devices(user) {
		out = append(out, view(d))
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
	n := len(s.st.devices(user))
	if n == 0 {
		fail(w, http.StatusConflict, "no device is registered for push")
		return
	}
	if ok, wait := s.user.allow(user); !ok {
		tooMany(w, wait, "too many notifications for this user; try later")
		return
	}
	if !s.enqueue(note{user: user, kind: KindTest, title: "xbin", body: "Test notification — push works."}) {
		fail(w, http.StatusServiceUnavailable, "the push queue is full; try later")
		return
	}
	server.WriteJSON(w, http.StatusAccepted, map[string]any{"devices": n})
}

// APINotify is POST /notify {user, title, body?, link?, kind?, collapseId?}
// for tile backends (and frontends): a notification to a person who can
// read the calling tile.
func (s *Service) APINotify(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	tile := p.Component
	if tile == "" {
		fail(w, http.StatusForbidden, "notify is called by a tile (its backend or frontend): the notification names the tile it comes from")
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
	}
	// the tile's budget first: refused calls spend it too, so a tile cannot
	// probe who reads it without bound
	if ok, wait := s.tile.allow(tile); !ok {
		tooMany(w, wait, "this tile is sending too many notifications; try later")
		return
	}
	if !s.o.CanRead(user, tile) {
		fail(w, http.StatusForbidden, "that user cannot read this tile")
		return
	}
	if ok, wait := s.user.allow(user); !ok {
		tooMany(w, wait, "too many notifications for this user; try later")
		return
	}
	n := note{user: user, tile: tile, kind: KindTile, title: title, body: body.Body, link: "c/" + tile + "/" + link}
	if body.Kind != "" {
		n.kind += "." + body.Kind
	}
	if body.CollapseID != "" {
		n.collapse = "tile:" + tile + ":" + body.CollapseID
	}
	if s.Enabled() {
		s.enqueue(n)
	}
	server.WriteJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// tileLink validates a link relative to the tile: a fragment, a query, or
// a path inside it — never a scheme, an absolute path or a way out.
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
		if seg == ".." || seg == "." {
			return "", false
		}
	}
	return l, true
}

// --- the workspace opt-in (admins) ---

type configView struct {
	Enabled        bool   `json:"enabled"`
	Source         string `json:"source,omitempty"` // env | admin
	Relay          string `json:"relay,omitempty"`
	RelayWorkspace string `json:"relayWorkspace,omitempty"`
	DefaultRelay   string `json:"defaultRelay,omitempty"`
	Set            int64  `json:"set,omitempty"`
	By             string `json:"by,omitempty"`
	Workspace      string `json:"workspace"`
	Devices        int    `json:"devices"`
	Stats          Stats  `json:"stats"`
}

func (s *Service) configView() configView {
	v := configView{Workspace: s.Workspace(), Devices: s.st.count(), Stats: s.snd.stats(), DefaultRelay: s.o.RelayURL}
	if c, src := s.relayConfig(); c != nil {
		v.Enabled, v.Source, v.Relay, v.RelayWorkspace, v.Set, v.By = true, src, c.URL, c.WorkspaceID, c.Set, c.By
	}
	return v
}

func (s *Service) admin(w http.ResponseWriter, r *http.Request) bool {
	if !s.o.IsAdmin(auth.PrincipalOf(r)) {
		fail(w, http.StatusForbidden, "admin only")
		return false
	}
	return true
}

// APIConfig is GET /push/config (admins).
func (s *Service) APIConfig(w http.ResponseWriter, r *http.Request) {
	if s.admin(w, r) {
		server.WriteJSON(w, http.StatusOK, s.configView())
	}
}

// APISetConfig is PUT /push/config {relay?, key?} (admins): turn push on.
// Without a key xbind registers this workspace with the relay (POST
// /v1/workspaces) and keeps the key it answers.
func (s *Service) APISetConfig(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	if _, src := s.relayConfig(); src == "env" {
		fail(w, http.StatusConflict, "the relay is configured by the environment (XBIN_PUSH_RELAY, XBIN_PUSH_RELAY_KEY)")
		return
	}
	var body struct {
		Relay string `json:"relay"`
		Key   string `json:"key"`
	}
	if err := decode(r, &body); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "need {relay?, key?}")
		return
	}
	if body.Relay == "" {
		body.Relay = s.o.RelayURL
	}
	relay, err := checkRelayURL(body.Relay)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	c := &RelayConfig{URL: relay, Key: strings.TrimSpace(body.Key), Set: s.o.Now().Unix(), By: auth.PrincipalOf(r).From()}
	if c.Key == "" {
		if c.WorkspaceID, c.Key, err = s.registerWorkspace(r.Context(), relay); err != nil {
			fail(w, http.StatusBadGateway, "registering with the relay: "+err.Error())
			return
		}
	}
	if err := s.st.setRelay(c); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.o.Log.Info("push: relay configured", "relay", relay, "by", c.By)
	server.WriteJSON(w, http.StatusOK, s.configView())
}

// APIDeleteConfig is DELETE /push/config (admins): push off. Registrations
// stay, so turning it back on needs nothing from the apps.
func (s *Service) APIDeleteConfig(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	if _, src := s.relayConfig(); src == "env" {
		fail(w, http.StatusConflict, "the relay is configured by the environment (XBIN_PUSH_RELAY, XBIN_PUSH_RELAY_KEY)")
		return
	}
	if err := s.st.setRelay(nil); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkRelayURL wants https (http only for a relay on this machine).
func checkRelayURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("relay: a URL such as https://relay.example")
	}
	h := u.Hostname()
	loop := h == "localhost"
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		loop = true
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
		return "", errors.New("relay: https:// (http only for a relay on localhost)")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *Service) registerWorkspace(ctx context.Context, relay string) (id, key string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relay+"/v1/workspaces", bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		WorkspaceID string `json:"workspaceId"`
		Key         string `json:"key"`
	}
	if json.Unmarshal(b, &out) != nil || out.Key == "" {
		return "", "", errors.New("the relay answered no key")
	}
	return out.WorkspaceID, out.Key, nil
}
