// managers.go — the sandbox managers bound to this tile's `sandboxes` slot
// (multi, service "sandbox-manager"; docs/sandbox-manager.md is the
// contract), and what this tile asks them: hello (for `tty`) and the
// sandboxes it may see, AS a person.
//
// This tile is a consumer that creates nothing: a sandbox reaches it by
// being shared with it (a share naming this tile, users "*" or a list) or by
// being its own. Its backend's calls carry no verified person, so it names
// the person it acts for in Sbx-User (asserted) and enforces the contract's
// person rules itself (mayUse) — the manager only records the assertion.
// What a manager says is never stored beyond a short cache (hello).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// manager is one bound sandbox manager.
type manager struct {
	Provider string `json:"provider"` // the manager tile ("apps/coding-sandbox", "apps/cs#eu" for an instance)
	URL      string `json:"-"`        // its base: the contract's routes are under <URL>/sbx/ (no trailing slash)
}

// managersFromEnv reads the `sandboxes` slot's bindings (XBIN_IFACE_SANDBOXES;
// rebinding restarts the backend). Nothing bound ⇒ none.
func managersFromEnv() []manager {
	var eps []struct {
		Provider string `json:"provider"`
		Instance string `json:"instance"`
		URL      string `json:"url"`
		Service  string `json:"service"`
	}
	if raw := os.Getenv("XBIN_IFACE_SANDBOXES"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &eps)
	}
	out := []manager{}
	seen := map[string]bool{}
	for _, e := range eps {
		if e.URL == "" || e.Provider == "" || (e.Service != "" && e.Service != "sandbox-manager") {
			continue
		}
		name := e.Provider
		if e.Instance != "" {
			name += "#" + e.Instance
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, manager{Provider: name, URL: strings.TrimRight(e.URL, "/")})
	}
	return out
}

// --- the contract's shapes (only what this tile reads) ------------------------------

type hello struct {
	Protocol int      `json:"protocol"`
	Caps     []string `json:"caps"`
	Manager  struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"manager"`
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type share struct {
	Consumer string          `json:"consumer"`
	Users    json.RawMessage `json:"users"` // "*" or a list of user ids
}

type sandbox struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	StateDetail string `json:"stateDetail,omitempty"`
	Image       struct {
		ID    string `json:"id"`
		Title string `json:"title,omitempty"`
	} `json:"image"`
	Isolation string `json:"isolation,omitempty"`
	Egress    string `json:"egress,omitempty"`
	Owner     struct {
		User     string `json:"user"`
		Via      string `json:"via"`
		Asserted bool   `json:"asserted,omitempty"`
	} `json:"owner"`
	Visibility string   `json:"visibility"`
	Members    []string `json:"members"`
	Shares     []share  `json:"shares"`
	Shared     bool     `json:"shared"`
	Workdir    string   `json:"workdir,omitempty"`
	User       string   `json:"user,omitempty"`
	Shell      string   `json:"shell,omitempty"`
	Caps       []string `json:"caps"`
	Created    int64    `json:"created"`
}

// refusal is a manager's error body (the contract's `{error, refusal, state?}`).
type refusal struct {
	Status   int    `json:"-"`
	Provider string `json:"-"`
	Msg      string `json:"error"`
	Refusal  string `json:"refusal"`
	State    string `json:"state,omitempty"`
}

func (r *refusal) Error() string {
	msg := r.Msg
	if msg == "" {
		msg = fmt.Sprintf("status %d", r.Status)
	}
	return r.Provider + ": " + msg
}

// decodeRefusal reads a refused answer (at most 4 KiB of it).
func decodeRefusal(provider string, resp *http.Response) *refusal {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	r := &refusal{Status: resp.StatusCode, Provider: provider}
	if json.Unmarshal(b, r) != nil || r.Msg == "" {
		r.Msg = strings.TrimSpace(string(b))
		if r.Msg == "" {
			r.Msg = resp.Status
		}
	}
	return r
}

// --- calls ----------------------------------------------------------------------------

// call is one JSON round trip to a manager as person (Sbx-User; "" = none):
// in as the body (nil: none; []byte: raw), the answer into out (nil: dropped).
func (t *Tile) call(ctx context.Context, m manager, person, method, route string, in, out any) error {
	var body io.Reader
	ctype := ""
	switch v := in.(type) {
	case nil:
	case []byte:
		body, ctype = bytes.NewReader(v), "application/octet-stream"
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		body, ctype = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, m.URL+"/sbx"+route, body)
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if person != "" {
		req.Header.Set("Sbx-User", person)
	}
	resp, err := t.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return &refusal{Provider: m.Provider, Refusal: "unavailable", Msg: "unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeRefusal(m.Provider, resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return &refusal{Provider: m.Provider, Refusal: "unavailable", Msg: "a garbled answer: " + err.Error()}
	}
	return nil
}

// helloCache keeps each manager's hello for a minute (a failure: 10 s).
type helloCache struct {
	mu sync.Mutex
	m  map[string]helloEntry
}

type helloEntry struct {
	h   *hello
	err error
	at  time.Time
}

// hello negotiates protocol 1 with a manager, cached.
func (t *Tile) hello(ctx context.Context, m manager) (*hello, error) {
	key := m.Provider + "\x00" + m.URL
	t.hellos.mu.Lock()
	if e, ok := t.hellos.m[key]; ok {
		ttl := time.Minute
		if e.err != nil {
			ttl = 10 * time.Second
		}
		if time.Since(e.at) < ttl {
			t.hellos.mu.Unlock()
			return e.h, e.err
		}
	}
	t.hellos.mu.Unlock()
	var h hello
	err := t.call(ctx, m, "", "GET", "/hello?protocol=1", nil, &h)
	if err == nil && h.Protocol != 1 {
		err = &refusal{Provider: m.Provider, Refusal: "protocol", Msg: fmt.Sprintf("speaks protocol %d, not 1", h.Protocol)}
	}
	if ctx.Err() != nil && err != nil {
		return nil, err // the caller gave up: nothing learned
	}
	t.hellos.mu.Lock()
	if t.hellos.m == nil {
		t.hellos.m = map[string]helloEntry{}
	}
	if err != nil {
		t.hellos.m[key] = helloEntry{err: err, at: time.Now()}
		t.hellos.mu.Unlock()
		return nil, err
	}
	t.hellos.m[key] = helloEntry{h: &h, at: time.Now()}
	t.hellos.mu.Unlock()
	return &h, nil
}

// --- who may use what -------------------------------------------------------------------

// mayUse is the contract's person rule, which this tile enforces for the
// people it acts for (the manager doesn't, on a backend's call): a sandbox
// shared with this tile only as far as its share names the person, then its
// owner, a member, or anyone when it is team. Only a sandbox whose home
// (owner.via) is this tile is its own: one that names no home — a manager
// off the contract — needs a share like any other.
func (t *Tile) mayUse(person string, sb *sandbox) bool {
	if person == "" || sb == nil {
		return false
	}
	if sb.Shared || sb.Owner.Via != t.self {
		if !shareAllows(sb.Shares, t.self, person) {
			return false
		}
	}
	return sb.Owner.User == person || has(sb.Members, person) || sb.Visibility == "team"
}

// shareAllows: the share naming consumer lists user ("*": everyone). No such
// share (or one that can't be read) is no.
func shareAllows(shares []share, consumer, user string) bool {
	if consumer == "" {
		return false
	}
	for _, sh := range shares {
		if sh.Consumer != consumer {
			continue
		}
		var all string
		if json.Unmarshal(sh.Users, &all) == nil {
			return all == "*"
		}
		var list []string
		return json.Unmarshal(sh.Users, &list) == nil && has(list, user)
	}
	return false
}

// --- a person's sandboxes, across the managers -----------------------------------------

// entry is one sandbox a person may use, on its manager, with its login
// name (the SSH user name that picks it).
type entry struct {
	M     manager
	Hello *hello
	SB    sandbox
	Login string
}

// tty: its manager (and the sandbox) offer terminals.
func (e *entry) tty() bool {
	return e.Hello != nil && has(e.Hello.Caps, "tty") && (e.SB.Caps == nil || has(e.SB.Caps, "tty"))
}

// managerView is one manager as a person's listing shows it.
type managerView struct {
	Provider string `json:"provider"`
	Title    string `json:"title,omitempty"`
	TTY      bool   `json:"tty"`
	Error    string `json:"error,omitempty"`
}

// usable lists the sandboxes person may use on every bound manager, in the
// managers' order (then by creation), each with its login name; a manager
// that fails is listed with its error and contributes nothing.
func (t *Tile) usable(ctx context.Context, person string) ([]entry, []managerView) {
	ms := t.managers()
	type result struct {
		h   *hello
		sbs []sandbox
		err error
	}
	res := make([]result, len(ms))
	var wg sync.WaitGroup
	for i, m := range ms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			h, err := t.hello(cctx, m)
			if err != nil {
				res[i].err = err
				return
			}
			var l struct {
				Sandboxes []sandbox `json:"sandboxes"`
			}
			if err := t.call(cctx, m, person, "GET", "/sandboxes", nil, &l); err != nil {
				res[i].err = err
				return
			}
			res[i] = result{h: h, sbs: l.Sandboxes}
		}()
	}
	wg.Wait()
	var out []entry
	views := make([]managerView, 0, len(ms))
	for i, m := range ms {
		v := managerView{Provider: m.Provider}
		if r := res[i]; r.err != nil {
			v.Error = r.err.Error()
		} else {
			v.Title, v.TTY = r.h.Manager.Title, has(r.h.Caps, "tty")
			sbs := r.sbs
			sort.SliceStable(sbs, func(a, b int) bool {
				return sbs[a].Created < sbs[b].Created || sbs[a].Created == sbs[b].Created && sbs[a].ID < sbs[b].ID
			})
			for _, sb := range sbs {
				if t.mayUse(person, &sb) {
					out = append(out, entry{M: m, Hello: r.h, SB: sb})
				}
			}
		}
		views = append(views, v)
	}
	assignLogins(out)
	return out, views
}

// loginBase is the SSH user name a sandbox's name gives: lower case, and
// every run of anything but [a-z0-9._-] one '-'.
func loginBase(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		switch {
		case ok:
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-.")
}

// assignLogins gives every entry its login: the name's base when it is the
// only one with it, else base.<n> (n from 1 in the listing's order); a
// sandbox whose name gives nothing logs in by its id.
func assignLogins(es []entry) {
	groups := map[string][]int{}
	for i := range es {
		base := loginBase(es[i].SB.Name)
		if base == "" {
			base = es[i].SB.ID
		}
		groups[base] = append(groups[base], i)
	}
	for base, idx := range groups {
		if len(idx) == 1 {
			es[idx[0]].Login = base
			continue
		}
		for n, i := range idx {
			es[i].Login = fmt.Sprintf("%s.%d", base, n+1)
		}
	}
}

// errNoSandbox is a login that names none or several of a person's
// sandboxes; its message says what to use instead.
type errNoSandbox struct{ msg string }

func (e *errNoSandbox) Error() string { return e.msg }

// pick resolves an SSH user name to one of the person's sandboxes: its
// login, its id, or its name's base — exactly one of them, else an error
// that lists the choices.
func pick(es []entry, views []managerView, login string) (*entry, error) {
	var hits []int
	for i := range es {
		base := loginBase(es[i].SB.Name)
		if es[i].Login == login || es[i].SB.ID == login || (base != "" && base == loginBase(login)) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 1 {
		return &es[hits[0]], nil
	}
	var b strings.Builder
	if len(hits) > 1 {
		fmt.Fprintf(&b, "%q names %d sandboxes you may use — log in as one of:\n", login, len(hits))
		for _, i := range hits {
			fmt.Fprintf(&b, "  %-20s %s (id %s) on %s\n", es[i].Login, es[i].SB.Name, es[i].SB.ID, es[i].M.Provider)
		}
		return nil, &errNoSandbox{b.String()}
	}
	fmt.Fprintf(&b, "no sandbox %q here for you.", login)
	switch {
	case len(views) == 0:
		b.WriteString(" This tile isn't bound to a sandbox manager yet: its owner binds one (bx bind apps/sandbox-terminal sandboxes=<manager>).\n")
	case len(es) == 0:
		b.WriteString(" You may use none through this tile: a sandbox reaches it when its owner or a manager shares it with this terminal tile.\n")
	default:
		b.WriteString(" Log in as one of:\n")
		for _, e := range es {
			fmt.Fprintf(&b, "  %-20s %s (id %s) on %s\n", e.Login, e.SB.Name, e.SB.ID, e.M.Provider)
		}
	}
	for _, v := range views {
		if v.Error != "" {
			fmt.Fprintf(&b, "(%s didn't answer: %s)\n", v.Provider, v.Error)
		}
	}
	return nil, &errNoSandbox{strings.TrimRight(b.String(), "\n")}
}

// sbxPath is a sandbox route: /sandboxes/<id><rest>.
func sbxPath(id, rest string) string { return "/sandboxes/" + url.PathEscape(id) + rest }

// isRefusal reports a manager's refusal with one of the given refusals.
func isRefusal(err error, refusals ...string) (*refusal, bool) {
	var r *refusal
	if !errors.As(err, &r) {
		return nil, false
	}
	return r, len(refusals) == 0 || has(refusals, r.Refusal)
}
