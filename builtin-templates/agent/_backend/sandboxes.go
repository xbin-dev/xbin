// sandboxes.go — the coding sandboxes this agent can use (D115): the sandbox
// managers bound to its `sandboxes` interface slot (multi, service
// "sandbox-manager"; docs/sandbox-manager.md is the contract), their merged
// catalog, and a typed call for every contract route the agent uses
// (sandbox_client.go).
//
// A sandbox reference is "<provider>[#inst]|<id>": the manager tile as its
// binding names it, and the manager's id for the sandbox. Unlike a model
// reference it is always qualified — a stored binding must keep naming the
// same sandbox however many managers are bound later.
//
// Who is asking: xbind sets X-XBin-From (this tile) on every call; the person
// the agent acts for travels in Sbx-User (asserted — the manager records it
// and trusts this backend to enforce who may use what: sandbox_access.go).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// sbxManager is one bound sandbox manager.
type sbxManager struct {
	Provider string // the manager tile ("apps/coding-sandbox"; "apps/coding-sandbox#eu" for an instance)
	URL      string // its base (the contract's routes are under <URL>/sbx/), no trailing slash
}

// sbxClient reaches the managers (through the xbin gateway); tests point it
// at plain HTTP servers with setSbxClient — under a lock, since a harness
// pipe's background eof or kill can outlive the test that started it.
var (
	sbxClientMu sync.RWMutex
	sbxClientFn = xbin.Client
)

func sbxClient() *http.Client {
	sbxClientMu.RLock()
	f := sbxClientFn
	sbxClientMu.RUnlock()
	return f()
}

// setSbxClient points sbxClient at f and returns what it was (tests).
func setSbxClient(f func() *http.Client) (old func() *http.Client) {
	sbxClientMu.Lock()
	defer sbxClientMu.Unlock()
	old, sbxClientFn = sbxClientFn, f
	return old
}

// sandboxManagers reads the `sandboxes` slot's bindings from the
// runner-injected env (rebinding restarts the backend). Nothing bound ⇒ none:
// unlike the llm slot there is no legacy fallback.
func sandboxManagers() []sbxManager {
	var eps []struct {
		Provider string `json:"provider"`
		Instance string `json:"instance"`
		URL      string `json:"url"`
		Service  string `json:"service"`
	}
	if raw := os.Getenv("XBIN_IFACE_SANDBOXES"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &eps)
	}
	out := make([]sbxManager, 0, len(eps))
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
		out = append(out, sbxManager{Provider: name, URL: strings.TrimRight(e.URL, "/")})
	}
	return out
}

// boundManager finds a bound manager by provider.
func boundManager(provider string) (sbxManager, bool) {
	for _, m := range sandboxManagers() {
		if m.Provider == provider {
			return m, true
		}
	}
	return sbxManager{}, false
}

// --- references --------------------------------------------------------------

// sbxIDRe is the contract's sandbox id shape.
var sbxIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// sandboxRef names a sandbox on a manager.
func sandboxRef(provider, id string) string { return provider + "|" + id }

// splitSandboxRef takes "<provider>|<id>" apart; ok is false for anything
// that isn't one (an id never holds "|", so the last one splits).
func splitSandboxRef(ref string) (provider, id string, ok bool) {
	i := strings.LastIndexByte(ref, '|')
	if i <= 0 {
		return "", "", false
	}
	provider, id = ref[:i], ref[i+1:]
	return provider, id, sbxIDRe.MatchString(id) && !strings.ContainsAny(provider, " \t\n")
}

// --- hello -------------------------------------------------------------------

type sbxImage struct {
	ID      string   `json:"id"`
	Title   string   `json:"title,omitempty"`
	Default bool     `json:"default,omitempty"`
	Tools   []string `json:"tools,omitempty"`
	// Harnesses are the coding agents the manager says the image has
	// (harness_catalog.go); nil on every image of a manager that predates
	// the field — unknown, not none.
	Harnesses []sbxHarness `json:"harnesses,omitempty"`
}

// sbxHarness is one of hello.images[].harnesses: a coding agent speaking
// ACP on stdio. A catalog id needs nothing else (the sdk's catalog has its
// title, argv and login); any other needs argv.
type sbxHarness struct {
	ID    string   `json:"id"`
	Title string   `json:"title,omitempty"`
	Argv  []string `json:"argv,omitempty"`
	Login string   `json:"login,omitempty"`
}

type sbxSize struct {
	ID      string `json:"id"`
	MemMiB  int    `json:"memMiB,omitempty"`
	VCPUs   int    `json:"vcpus,omitempty"`
	DiskGiB int    `json:"diskGiB,omitempty"`
	Default bool   `json:"default,omitempty"`
}

type sbxLimits struct {
	Sandboxes       int   `json:"sandboxes"`
	RunTimeoutMaxMs int   `json:"runTimeoutMaxMs"`
	RunOutputMax    int   `json:"runOutputMax"`
	ExecsRunning    int   `json:"execsRunning"`
	OutputRing      int   `json:"outputRing"`
	StdinMax        int   `json:"stdinMax"`
	FileMax         int64 `json:"fileMax"`
	TarMax          int64 `json:"tarMax"`
	WaitMaxSec      int   `json:"waitMaxSec"`
}

// sbxHello is a manager's GET /sbx/hello?protocol=1.
type sbxHello struct {
	Protocol  int   `json:"protocol"`
	Protocols []int `json:"protocols,omitempty"`
	Manager   struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"manager"`
	Caps   []string   `json:"caps"`
	Egress []string   `json:"egress"`
	Images []sbxImage `json:"images"`
	Sizes  []sbxSize  `json:"sizes"`
	Limits sbxLimits  `json:"limits"`
}

func (h *sbxHello) has(capName string) bool {
	for _, c := range h.Caps {
		if c == capName {
			return true
		}
	}
	return false
}

// imageTools is what the manager says image id has (nil: it says nothing,
// or has no such image) — at most 40 names, each a short word.
func (h *sbxHello) imageTools(id string) []string {
	if h == nil {
		return nil
	}
	for _, im := range h.Images {
		if im.ID != id {
			continue
		}
		var out []string
		for _, t := range im.Tools {
			if t = strings.TrimSpace(t); t != "" && len(t) <= 40 && !strings.ContainsAny(t, "\n\r,") && len(out) < 40 {
				out = append(out, t)
			}
		}
		return out
	}
	return nil
}

// title is how people know the manager: its own title, else its tile.
func (h *sbxHello) title(provider string) string {
	if h != nil && h.Manager.Title != "" {
		return h.Manager.Title
	}
	if h != nil && h.Manager.Name != "" {
		return h.Manager.Name
	}
	return provider
}

type helloEntry struct {
	h   *sbxHello
	err error
	at  time.Time
}

var (
	helloMu      sync.Mutex
	helloCache   = map[string]helloEntry{}
	helloTTL     = 5 * time.Minute
	helloFailTTL = 15 * time.Second // a manager that is down is asked again soon
)

// managerHello negotiates protocol 1 with a manager, cached. A manager that
// doesn't speak it, or lacks the capabilities protocol 1 requires (exec,
// files), is refused with the reason — the agent ignores it.
func managerHello(ctx context.Context, m sbxManager) (*sbxHello, error) {
	key := m.Provider + "\x00" + m.URL
	helloMu.Lock()
	if e, ok := helloCache[key]; ok {
		ttl := helloTTL
		if e.err != nil {
			ttl = helloFailTTL
		}
		if time.Since(e.at) < ttl {
			helloMu.Unlock()
			return e.h, e.err
		}
	}
	helloMu.Unlock()
	return managerHelloFresh(ctx, m)
}

// managerHelloFresh asks the manager again, bypassing the cache and
// refreshing it — before a refusal a missing capability would cause: a
// manager updated since (or an xbind that gained a capability) must not be
// refused for up to helloTTL on what it said before (the sandbox's own caps,
// sandbox_info prints them, are always fresh).
func managerHelloFresh(ctx context.Context, m sbxManager) (*sbxHello, error) {
	h, err := fetchHello(ctx, m)
	if ctx.Err() != nil && err != nil {
		return nil, err // the caller gave up: nothing learned about the manager
	}
	helloMu.Lock()
	helloCache[m.Provider+"\x00"+m.URL] = helloEntry{h: h, err: err, at: time.Now()}
	helloMu.Unlock()
	return h, err
}

// capWords says what a manager offers, for a refusal: its title and
// version, and its capabilities.
func (h *sbxHello) capWords(provider string) string {
	v := ""
	if h != nil && h.Manager.Version != "" {
		v = " " + h.Manager.Version
	}
	caps := "none"
	if h != nil && len(h.Caps) > 0 {
		caps = strings.Join(h.Caps, ", ")
	}
	return fmt.Sprintf("%s%s (it offers: %s)", h.title(provider), v, caps)
}

func fetchHello(ctx context.Context, m sbxManager) (*sbxHello, error) {
	c := &sbxConn{M: m}
	var h sbxHello
	if err := c.call(ctx, "GET", "/hello", map[string]string{"protocol": "1"}, nil, &h, sbxHelloTimeout); err != nil {
		return nil, err
	}
	switch {
	case h.Protocol != 1:
		return nil, &sbxError{Provider: m.Provider, Refusal: "protocol",
			Msg: fmt.Sprintf("it speaks sandbox-manager protocol %d; this agent speaks 1", h.Protocol)}
	case !h.has("exec") || !h.has("files"):
		return nil, &sbxError{Provider: m.Provider, Refusal: "unsupported",
			Msg: "it lacks the exec and files capabilities protocol 1 requires"}
	}
	return &h, nil
}

// forgetHellos drops every cached hello (tests; a rebinding restarts us).
func forgetHellos() {
	helloMu.Lock()
	helloCache = map[string]helloEntry{}
	helloMu.Unlock()
}

// --- the catalog -------------------------------------------------------------

// sbxCatalogManager is one manager as the catalog shows it.
type sbxCatalogManager struct {
	Provider string     `json:"provider"`
	Title    string     `json:"title"`
	OK       bool       `json:"ok"`
	Error    string     `json:"error,omitempty"`
	Refusal  string     `json:"refusal,omitempty"`
	Caps     []string   `json:"caps"`
	Egress   []string   `json:"egress"`
	Images   []sbxImage `json:"images"`
	Sizes    []sbxSize  `json:"sizes"`
	Limits   *sbxLimits `json:"limits,omitempty"`
}

// sbxCatalogEntry is one sandbox a manager listed for this agent.
type sbxCatalogEntry struct {
	Ref      string
	Provider string
	Manager  string // the manager's title
	Box      *sbxSandbox
}

type sbxCatalog struct {
	Sandboxes []sbxCatalogEntry
	Managers  []sbxCatalogManager
	at        time.Time
}

var (
	sbxCatMu    sync.Mutex
	sbxCatCache *sbxCatalog
	sbxCatGen   int // bumped by every invalidation: a fetch that raced one isn't cached
	sbxCatTTL   = 15 * time.Second
)

// invalidateSandboxCatalog forgets the cached catalog — after this agent
// changed something at a manager, so the next read sees it.
func invalidateSandboxCatalog() {
	sbxCatMu.Lock()
	sbxCatCache = nil
	sbxCatGen++
	sbxCatMu.Unlock()
}

// sandboxCatalog merges every bound manager's sandboxes (this agent's
// partition: the ones it created and those shared with it), cached briefly.
// Managers are asked in parallel; one that fails, or that hello refused, is
// reported with why, never fatal.
func sandboxCatalog(ctx context.Context) *sbxCatalog {
	sbxCatMu.Lock()
	if c := sbxCatCache; c != nil && time.Since(c.at) < sbxCatTTL {
		sbxCatMu.Unlock()
		return c
	}
	gen := sbxCatGen
	sbxCatMu.Unlock()
	mgrs := sandboxManagers()
	type answer struct {
		boxes []sbxCatalogEntry
		mgr   sbxCatalogManager
	}
	answers := make([]answer, len(mgrs))
	var wg sync.WaitGroup
	for i, m := range mgrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := answer{mgr: sbxCatalogManager{Provider: m.Provider, Title: m.Provider,
				Caps: []string{}, Egress: []string{}, Images: []sbxImage{}, Sizes: []sbxSize{}}}
			defer func() { answers[i] = a }()
			h, err := managerHello(ctx, m)
			if err != nil {
				a.mgr.Error, a.mgr.Refusal = err.Error(), sbxRefusal(err)
				return
			}
			a.mgr.Title, a.mgr.Caps, a.mgr.Egress = h.title(m.Provider), h.Caps, h.Egress
			a.mgr.Images, a.mgr.Sizes, a.mgr.Limits = h.Images, h.Sizes, &h.Limits
			boxes, err := (&sbxConn{M: m}).List(ctx)
			if err != nil {
				a.mgr.Error, a.mgr.Refusal = err.Error(), sbxRefusal(err)
				return
			}
			a.mgr.OK = true
			for _, b := range boxes {
				a.boxes = append(a.boxes, sbxCatalogEntry{Ref: sandboxRef(m.Provider, b.ID), Provider: m.Provider, Manager: a.mgr.Title, Box: b})
			}
		}()
	}
	wg.Wait()
	c := &sbxCatalog{at: time.Now(), Sandboxes: []sbxCatalogEntry{}, Managers: []sbxCatalogManager{}}
	for _, a := range answers {
		c.Sandboxes = append(c.Sandboxes, a.boxes...)
		c.Managers = append(c.Managers, a.mgr)
	}
	sbxCatMu.Lock()
	if gen == sbxCatGen && ctx.Err() == nil {
		sbxCatCache = c
	}
	sbxCatMu.Unlock()
	return c
}

// catalogManager is one manager's catalog entry (nil when it isn't bound).
func (c *sbxCatalog) manager(provider string) *sbxCatalogManager {
	for i := range c.Managers {
		if c.Managers[i].Provider == provider {
			return &c.Managers[i]
		}
	}
	return nil
}
