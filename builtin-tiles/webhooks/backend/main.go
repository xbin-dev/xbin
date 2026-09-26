// webhooks — turns webhooks from outside (GitHub, a CI, a monitor) into
// events for the agents this tile is bound to (D87): each hook is a public
// URL (/hook/<id>, published with bx expose) checked by a token or an HMAC
// signature, and each delivery is pushed to the agents over the agent-inbox
// contract (POST /adapter/event, docs/agent-inbox.md) as PUBLIC data — it
// comes from outside, so only triggers that may take outside data run on it.
//
// It needs no egress and no alwaysOn: a delivery starts it like any request.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// hook is one webhook endpoint.
type hook struct {
	ID          string `json:"id"`                    // the URL segment: /hook/<id>
	Name        string `json:"name"`                  // the topic it pushes (with topicFrom appended, when set)
	Agent       string `json:"agent,omitempty"`       // one bound agent's path ("" = every bound agent)
	Auth        string `json:"auth"`                  // token | hmac
	SigHeader   string `json:"sigHeader,omitempty"`   // hmac: the signature header (X-Hub-Signature-256)
	SigPrefix   string `json:"sigPrefix,omitempty"`   // hmac: before the hex digest ("sha256=")
	EventIDFrom string `json:"eventIdFrom,omitempty"` // header:<name> | json:<dotted.key> | "" (a hash of the body)
	TopicFrom   string `json:"topicFrom,omitempty"`   // header:<name> | json:<dotted.key> | "" (just the name)
	Enabled     bool   `json:"enabled"`
	Created     int64  `json:"created"`
}

// delivery is one line of the page's recent activity.
type delivery struct {
	At      int64  `json:"at"`
	Hook    string `json:"hook"`
	Topic   string `json:"topic,omitempty"`
	EventID string `json:"eventId,omitempty"`
	Status  int    `json:"status"`
	Result  string `json:"result"`
}

// agentEndpoint is one bound agent (XBIN_IFACE_AGENTS, a multi slot).
type agentEndpoint struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
}

type store interface {
	Get(key string) ([]byte, error)
	Put(key string, val []byte) error
}

// Tile is the adapter.
type Tile struct {
	kv       store
	secret   func(name string) (string, error)
	setSec   func(name, value string) error // "" deletes
	agents   func() []agentEndpoint
	hc       *http.Client
	mu       sync.Mutex
	hooks    []hook
	cache    map[string]string // hook id → its secret (read once from the vault)
	log      []delivery
	lastHost string // the public host deliveries came in on
}

func newTile(kv store, secret func(string) (string, error), setSec func(string, string) error, agents func() []agentEndpoint, hc *http.Client) *Tile {
	t := &Tile{kv: kv, secret: secret, setSec: setSec, agents: agents, hc: hc, cache: map[string]string{}}
	if b, err := kv.Get("hooks"); err == nil {
		_ = json.Unmarshal(b, &t.hooks)
	}
	return t
}

func (t *Tile) saveHooks() error {
	b, _ := json.Marshal(t.hooks)
	return t.kv.Put("hooks", b)
}

func (t *Tile) find(id string) (hook, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, h := range t.hooks {
		if h.ID == id {
			return h, true
		}
	}
	return hook{}, false
}

func secretName(h hook) string { return "hook-" + h.Auth + "-" + h.ID }

func (t *Tile) hookSecret(h hook) string {
	t.mu.Lock()
	s, ok := t.cache[h.ID]
	t.mu.Unlock()
	if ok {
		return s
	}
	s, err := t.secret(secretName(h))
	if err != nil || s == "" {
		return ""
	}
	t.mu.Lock()
	t.cache[h.ID] = s
	t.mu.Unlock()
	return s
}

func (t *Tile) record(d delivery) {
	d.At = time.Now().Unix()
	t.mu.Lock()
	t.log = append(t.log, d)
	if len(t.log) > 50 {
		t.log = t.log[len(t.log)-50:]
	}
	t.mu.Unlock()
}

const maxBody = 256 << 10

// handleHook is a delivery: POST /hook/{id}, from the outside (ingress) —
// or the tile's own owner, testing.
func (t *Tile) handleHook(w http.ResponseWriter, r *http.Request) {
	c := xbin.Caller(r)
	if !c.Ingress() && !xbin.RoleSatisfies(c.Role, "admin") {
		xbin.WriteError(w, http.StatusForbidden, "webhooks come from outside")
		return
	}
	h, ok := t.find(r.PathValue("id"))
	if !ok || !h.Enabled {
		xbin.WriteError(w, http.StatusNotFound, "no such hook")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		xbin.WriteError(w, http.StatusRequestEntityTooLarge, "the body is over 256 KB")
		return
	}
	if !verify(h, t.hookSecret(h), r, body) {
		t.record(delivery{Hook: h.ID, Status: 401, Result: "bad token or signature"})
		xbin.WriteError(w, http.StatusUnauthorized, "bad token or signature")
		return
	}
	if host := r.Header.Get("X-XBin-Ingress-Host"); host != "" {
		t.mu.Lock()
		t.lastHost = host
		t.mu.Unlock()
	}
	ev := map[string]any{"eventId": eventID(h, r, body), "topic": topic(h, r, body), "dataClass": "public"}
	if json.Valid(body) {
		ev["data"] = json.RawMessage(body)
	} else if len(body) > 0 {
		ev["text"] = string(body)
	}
	status, result := t.forward(h, ev)
	t.record(delivery{Hook: h.ID, Topic: ev["topic"].(string), EventID: ev["eventId"].(string), Status: status, Result: result})
	xbin.WriteJSON(w, status, map[string]string{"result": result})
}

// forward pushes the event to the hook's agents: 202 when one took it, 503
// when one was halted or unreachable (the sender retries), 404 when none
// has a trigger for it.
func (t *Tile) forward(h hook, ev map[string]any) (int, string) {
	body, _ := json.Marshal(ev)
	var took, retry, none []string
	for _, a := range t.agents() {
		if h.Agent != "" && a.Provider != h.Agent {
			continue
		}
		resp, err := t.hc.Post(strings.TrimRight(a.URL, "/")+"/adapter/event", "application/json", bytes.NewReader(body))
		if err != nil {
			retry = append(retry, a.Provider)
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		switch {
		case resp.StatusCode == 200:
			took = append(took, a.Provider)
		case resp.StatusCode == 404:
			none = append(none, a.Provider)
		default:
			retry = append(retry, fmt.Sprintf("%s (%d)", a.Provider, resp.StatusCode))
		}
	}
	switch {
	case len(retry) > 0:
		return http.StatusServiceUnavailable, "try again: " + strings.Join(retry, ", ")
	case len(took) > 0:
		return http.StatusAccepted, "delivered to " + strings.Join(took, ", ")
	case len(none) > 0:
		return http.StatusNotFound, "no trigger takes it on " + strings.Join(none, ", ")
	}
	return http.StatusServiceUnavailable, "not bound to an agent"
}

// --- event id and topic ------------------------------------------------------------

func eventID(h hook, r *http.Request, body []byte) string {
	if v := extract(h.EventIDFrom, r, body); v != "" {
		return h.ID + ":" + v
	}
	sum := sha256.Sum256(body)
	return h.ID + ":" + hex.EncodeToString(sum[:12])
}

func topic(h hook, r *http.Request, body []byte) string {
	if v := extract(h.TopicFrom, r, body); v != "" {
		return h.Name + "/" + v
	}
	return h.Name
}

// extract reads header:<name> or json:<dotted.key> ("" when absent).
func extract(from string, r *http.Request, body []byte) string {
	kind, arg, _ := strings.Cut(from, ":")
	switch kind {
	case "header":
		return clip(r.Header.Get(arg), 200)
	case "json":
		var v any
		if json.Unmarshal(body, &v) != nil {
			return ""
		}
		for _, k := range strings.Split(arg, ".") {
			m, ok := v.(map[string]any)
			if !ok {
				return ""
			}
			v = m[k]
		}
		switch x := v.(type) {
		case string:
			return clip(x, 200)
		case float64, bool:
			return fmt.Sprint(x)
		}
	}
	return ""
}

// --- the owner's routes ----------------------------------------------------------------

var hookIDRe = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)

func newSecret() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func mayWrite(w http.ResponseWriter, r *http.Request) bool {
	if c := xbin.Caller(r); c.User == "" || c.UserCanWrite() {
		return true
	}
	xbin.WriteError(w, http.StatusForbidden, "changing hooks needs write access to this tile")
	return false
}

func (t *Tile) handleList(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	hooks := append([]hook{}, t.hooks...)
	log := append([]delivery{}, t.log...)
	host := t.lastHost
	t.mu.Unlock()
	agents := []agentEndpoint{}
	agents = append(agents, t.agents()...)
	xbin.WriteJSON(w, 200, map[string]any{"hooks": hooks, "deliveries": log, "agents": agents, "host": host})
}

// handleCreate makes a hook with a fresh secret, returned this once.
//
//	POST /hooks {name, id?, agent?, auth: token|hmac, sigHeader?, sigPrefix?, eventIdFrom?, topicFrom?}
func (t *Tile) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !mayWrite(w, r) {
		return
	}
	var h hook
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	h.Name = strings.TrimSpace(h.Name)
	if h.ID == "" {
		h.ID = newSecret()[:12]
	}
	switch {
	case h.Name == "" || len(h.Name) > 60 || strings.ContainsAny(h.Name, " /"):
		xbin.WriteError(w, 400, "a hook needs a name: the topic its events carry (no spaces or slashes)")
		return
	case !hookIDRe.MatchString(h.ID):
		xbin.WriteError(w, 400, "the id is 3–40 of a-z, 0-9 and -")
		return
	case h.Auth != "token" && h.Auth != "hmac":
		xbin.WriteError(w, 400, "auth is token or hmac")
		return
	}
	if h.Auth == "hmac" && h.SigHeader == "" {
		h.SigHeader, h.SigPrefix = "X-Hub-Signature-256", "sha256="
	}
	if _, dup := t.find(h.ID); dup {
		xbin.WriteError(w, 409, "a hook with that id exists")
		return
	}
	sec := newSecret()
	if err := t.setSec(secretName(h), sec); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	h.Enabled, h.Created = true, time.Now().Unix()
	t.mu.Lock()
	t.hooks = append(t.hooks, h)
	t.cache[h.ID] = sec
	err := t.saveHooks()
	t.mu.Unlock()
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"hook": h, "secret": sec})
}

func (t *Tile) mutate(w http.ResponseWriter, id string, fn func(h *hook) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.hooks {
		if t.hooks[i].ID == id {
			if !fn(&t.hooks[i]) {
				t.hooks = append(t.hooks[:i], t.hooks[i+1:]...)
			}
			if err := t.saveHooks(); err != nil {
				xbin.WriteError(w, 500, err.Error())
				return
			}
			xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
			return
		}
	}
	xbin.WriteError(w, 404, "no such hook")
}

// handleUpdate: PUT /hooks/{id} {enabled?, name?, agent?, eventIdFrom?, topicFrom?}
func (t *Tile) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if !mayWrite(w, r) {
		return
	}
	var p struct {
		Enabled                             *bool
		Name, Agent, EventIDFrom, TopicFrom *string
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	t.mutate(w, r.PathValue("id"), func(h *hook) bool {
		if p.Enabled != nil {
			h.Enabled = *p.Enabled
		}
		for dst, src := range map[*string]*string{&h.Name: p.Name, &h.Agent: p.Agent, &h.EventIDFrom: p.EventIDFrom, &h.TopicFrom: p.TopicFrom} {
			if src != nil {
				*dst = strings.TrimSpace(*src)
			}
		}
		return true
	})
}

func (t *Tile) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !mayWrite(w, r) {
		return
	}
	id := r.PathValue("id")
	t.mutate(w, id, func(h *hook) bool {
		delete(t.cache, h.ID)
		_ = t.setSec(secretName(*h), "")
		return false
	})
}

// handleRotate gives a hook a new secret, returned this once.
func (t *Tile) handleRotate(w http.ResponseWriter, r *http.Request) {
	if !mayWrite(w, r) {
		return
	}
	h, ok := t.find(r.PathValue("id"))
	if !ok {
		xbin.WriteError(w, 404, "no such hook")
		return
	}
	sec := newSecret()
	if err := t.setSec(secretName(h), sec); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	t.mu.Lock()
	t.cache[h.ID] = sec
	t.mu.Unlock()
	xbin.WriteJSON(w, 200, map[string]string{"secret": sec})
}

// handleAgentTriggers: the push triggers each bound agent has for this tile,
// so the page can name hooks after them.
func (t *Tile) handleAgentTriggers(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	for _, a := range t.agents() {
		resp, err := t.hc.Get(strings.TrimRight(a.URL, "/") + "/adapter/triggers")
		if err != nil {
			continue
		}
		var v struct {
			Triggers []map[string]any `json:"triggers"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v)
		resp.Body.Close()
		out[a.Provider] = v.Triggers
	}
	xbin.WriteJSON(w, 200, out)
}

func (t *Tile) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /hook/{id}", t.handleHook)
	mux.Handle("GET /hooks", xbin.RoleFunc("admin", t.handleList))
	mux.Handle("POST /hooks", xbin.RoleFunc("admin", t.handleCreate))
	mux.Handle("PUT /hooks/{id}", xbin.RoleFunc("admin", t.handleUpdate))
	mux.Handle("DELETE /hooks/{id}", xbin.RoleFunc("admin", t.handleDelete))
	mux.Handle("POST /hooks/{id}/rotate", xbin.RoleFunc("admin", t.handleRotate))
	mux.Handle("GET /agent-triggers", xbin.RoleFunc("admin", t.handleAgentTriggers))
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// boundAgents reads the multi slot's endpoints (sorted, for a stable page).
func boundAgents() []agentEndpoint {
	var out []agentEndpoint
	_ = json.Unmarshal([]byte(os.Getenv("XBIN_IFACE_AGENTS")), &out)
	sort.Slice(out, func(i, k int) bool { return out[i].Provider < out[k].Provider })
	return out
}

func main() {
	setSec := func(name, v string) error {
		if v == "" {
			return xbin.DeleteSecret(name)
		}
		return xbin.SetSecret(name, v)
	}
	t := newTile(xbin.KV(xbin.Resource("state")), xbin.Secret, setSec, boundAgents, xbin.Client())
	mux := http.NewServeMux()
	t.routes(mux)
	xbin.Serve(mux)
}
