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
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
)

// The workspace's relay opt-in and the admin's view of registrations.
//
// The relay key names this workspace at the relay, and the relay binds every
// handle to the workspace that first pushed to it. So the key is kept for
// good: turning push off keeps it, turning push on again reuses it, and a
// new relay workspace is minted only on an explicit rotate (or a relay URL
// the key does not belong to). A new one orphans every handle that
// delivered under the old key; those registrations read needsNewHandle
// and the apps renew them.

type configView struct {
	Enabled        bool   `json:"enabled"`
	Source         string `json:"source,omitempty"` // env | admin
	Relay          string `json:"relay,omitempty"`
	RelayWorkspace string `json:"relayWorkspace,omitempty"`
	KeySet         bool   `json:"keySet,omitempty"` // an admin config holds a key (PUT {} reuses it)
	DefaultRelay   string `json:"defaultRelay,omitempty"`
	Set            int64  `json:"set,omitempty"`
	By             string `json:"by,omitempty"`
	Workspace      string `json:"workspace"`
	Devices        int    `json:"devices"`
	Stale          int    `json:"staleDevices,omitempty"` // registrations waiting for their app to renew the handle
	Stats          Stats  `json:"stats"`
	KeyChecked     int64  `json:"keyChecked,omitempty"` // the last daily key check (unix)
	KeyError       string `json:"keyError,omitempty"`   // what it found wrong ("" = the relay knows the key)
}

func (s *Service) configView() configView {
	v := configView{Workspace: s.Workspace(), Devices: s.st.count(), Stats: s.snd.stats(), DefaultRelay: s.o.RelayURL, Enabled: s.Enabled()}
	c, src := s.storedRelay()
	if c != nil {
		v.Source, v.Relay, v.RelayWorkspace, v.Set, v.By = src, c.URL, c.WorkspaceID, c.Set, c.By
		v.KeySet = src == "admin" && c.Key != ""
	}
	e := s.currentEpoch()
	for _, d := range s.st.all() {
		if d.stale(e) {
			v.Stale++
		}
	}
	s.mu.Lock()
	v.KeyChecked, v.KeyError = s.keyAt, s.keyErr
	s.mu.Unlock()
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

const envConfigured = "the relay is configured by the environment (XBIN_PUSH_RELAY, XBIN_PUSH_RELAY_KEY)"

// APISetConfig is PUT /push/config {relay?, key?, rotate?} (admins): turn
// push on. A key is checked with the relay (GET /v1/workspace). Without
// one, the stored key is reused when the relay is the same; a new relay
// workspace (POST /v1/workspaces) is registered only for a first opt-in, a
// different relay, or rotate:true.
func (s *Service) APISetConfig(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	if _, src := s.storedRelay(); src == "env" {
		fail(w, http.StatusConflict, envConfigured)
		return
	}
	var body struct {
		Relay  string `json:"relay"`
		Key    string `json:"key"`
		Rotate bool   `json:"rotate"`
	}
	if err := decode(r, &body); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "need {relay?, key?, rotate?}")
		return
	}
	cur := s.st.relay()
	raw := body.Relay
	switch {
	case raw != "":
	case cur != nil:
		raw = cur.URL
	default:
		raw = s.o.RelayURL
	}
	relay, err := checkRelayURL(raw)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	key := strings.TrimSpace(body.Key)
	c := &RelayConfig{URL: relay, Key: key, Set: s.o.Now().Unix(), By: auth.PrincipalOf(r).From()}
	switch {
	case key != "" && body.Rotate:
		fail(w, http.StatusBadRequest, "key and rotate: one or the other")
		return
	case key != "":
		id, code, err := s.probeKey(r.Context(), relay, key)
		switch {
		case code == http.StatusUnauthorized:
			fail(w, http.StatusBadRequest, "the relay does not know that key")
			return
		case err != nil:
			fail(w, http.StatusBadGateway, "checking the key with the relay: "+err.Error())
			return
		}
		c.WorkspaceID = id
	case !body.Rotate && cur != nil && cur.URL == relay && cur.Key != "":
		id, code, err := s.probeKey(r.Context(), relay, cur.Key)
		switch {
		case code == http.StatusUnauthorized:
			fail(w, http.StatusConflict, "the relay no longer knows this workspace's key; PUT {rotate:true} registers the workspace again (every app then renews its handle on its own)")
			return
		case err != nil:
			fail(w, http.StatusBadGateway, "reaching the relay: "+err.Error())
			return
		}
		c.Key, c.WorkspaceID = cur.Key, id
	default:
		if c.WorkspaceID, c.Key, err = s.registerWorkspace(r.Context(), relay); err != nil {
			fail(w, http.StatusBadGateway, "registering with the relay: "+err.Error())
			return
		}
		// one authenticated call: the relay reaps keys nobody ever used
		// (relay/README.md §Capacity), and an opt-in may wait long for its
		// first device
		_, _, _ = s.probeKey(r.Context(), relay, c.Key)
	}
	if err := s.st.setRelay(c); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.o.Log.Info("push: relay configured", "relay", relay, "by", c.By, "newKey", cur == nil || cur.Key != c.Key)
	server.WriteJSON(w, http.StatusOK, s.configView())
}

// APIDeleteConfig is DELETE /push/config (admins): push off. The relay
// key and every registration stay, so PUT /push/config {} turns it back on
// with nothing to do in the apps.
func (s *Service) APIDeleteConfig(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	if _, src := s.storedRelay(); src == "env" {
		fail(w, http.StatusConflict, envConfigured)
		return
	}
	if c := s.st.relay(); c != nil && !c.Off {
		c.Off, c.Set, c.By = true, s.o.Now().Unix(), auth.PrincipalOf(r).From()
		if err := s.st.setRelay(c); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
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

// relayCall makes one call to the relay and decodes a JSON answer.
func (s *Service) relayCall(ctx context.Context, method, target, key string, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var body io.Reader
	if method == http.MethodPost {
		body = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if json.Unmarshal(b, out) != nil {
		return resp.StatusCode, errors.New("the answer is not the relay's (is the URL the relay's base URL?)")
	}
	return resp.StatusCode, nil
}

// probeKey checks a key with the relay (GET /v1/workspace). code 401 = the
// relay's own "unknown key"; any other failure is an error.
func (s *Service) probeKey(ctx context.Context, relay, key string) (id string, code int, err error) {
	var out struct {
		WorkspaceID string `json:"workspaceId"`
		Code        string `json:"code"`
	}
	code, err = s.relayCall(ctx, http.MethodGet, relay+"/v1/workspace", key, &out)
	if code == http.StatusUnauthorized {
		return "", code, err
	}
	if err == nil && out.WorkspaceID == "" {
		err = errors.New("the answer is not the relay's (is the URL the relay's base URL?)")
	}
	return out.WorkspaceID, code, err
}

func (s *Service) registerWorkspace(ctx context.Context, relay string) (id, key string, err error) {
	var out struct {
		WorkspaceID string `json:"workspaceId"`
		Key         string `json:"key"`
	}
	if _, err := s.relayCall(ctx, http.MethodPost, relay+"/v1/workspaces", "", &out); err != nil {
		return "", "", err
	}
	if out.Key == "" {
		return "", "", errors.New("the relay answered no key")
	}
	return out.WorkspaceID, out.Key, nil
}

// --- registrations, the admin's view ---

// APIAdminDevices is GET /push/devices[?user=] (admins): every
// registration, or one user's.
func (s *Service) APIAdminDevices(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	user := r.URL.Query().Get("user")
	e := s.currentEpoch()
	out := []deviceView{}
	s.pruneDead()
	for _, d := range s.st.all() {
		if user == "" || d.User == user {
			v := view(d, e)
			v.User = d.User
			out = append(out, v)
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// APIAdminForget is DELETE /push/devices/{user} and
// /push/devices/{user}/{deviceId} (admins): revoke a user's registrations
// (a lost phone), all or one.
func (s *Service) APIAdminForget(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	user, dev := r.PathValue("user"), r.PathValue("deviceId")
	n := 0
	switch {
	case dev != "":
		if s.st.remove(user, dev) {
			n = 1
		}
	default:
		n = s.st.removeUser(user, false)
	}
	if n == 0 {
		fail(w, http.StatusNotFound, "no such push registration")
		return
	}
	s.o.Log.Info("push: registrations revoked by an admin", "user", user, "device", dev, "n", n, "by", auth.PrincipalOf(r).From())
	server.WriteJSON(w, http.StatusOK, map[string]any{"removed": n})
}
