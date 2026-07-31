// Traefik ingress terminator backend (plans/ingress.md, docs/ingress.md).
//
// The shape: xbind owns the ROUTE TABLE (which public hostname → which tile,
// bound by the owner); this tile owns TLS. It polls its slice of the route
// table (GET /api/xbin/ingress-routes — scoped to routes bound through it),
// renders traefik config (one router per host → the one "xbin" service:
// XBIN_INGRESS_FORWARD_URL, xbind's ingress-forward door), and supervises a
// traefik process that listens on :80/:443 inside this sandbox — published to
// the real host ports via the runtime L4 relay. Certificates come from
// traefik's native ACME (Let's Encrypt), persisted in the certs resource, so
// no certificate machinery ever lives in the xbind daemon.
//
// Traffic path: internet → host :443 → (xbind L4 relay) → traefik (TLS ends
// here) → 10.0.2.2:8642 (relay gateway forward) → xbind ingress-forward →
// target tile, as the anonymous `ingress` principal on its declared paths.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const pollEvery = 15 * time.Second

type route struct {
	Host      string `json:"host"`
	Component string `json:"component"`
	Slot      string `json:"slot"`
	Zone      string `json:"zone,omitempty"`
}

// settings is the owner-editable ACME config (index.html drives it).
type settings struct {
	Email   string `json:"email"`           // ACME account email
	Staging bool   `json:"staging"`         // Let's Encrypt staging endpoint (test without rate limits)
	NoTLS   bool   `json:"noTls,omitempty"` // plain :80 only (behind an external TLS front)
}

type state struct {
	mu       sync.Mutex
	routes   []route
	set      settings
	lastErr  string
	traefik  *exec.Cmd
	restarts int
}

var st state

func dataDir() string {
	d := xbin.Resource("certs")
	if d == "" {
		fmt.Fprintln(os.Stderr, "certs resource not granted yet — approve the pending grant (kept trying)")
		d = "/tmp/traefik-unpersisted"
		_ = os.MkdirAll(d, 0o700)
	}
	return d
}

func loadSettings() settings {
	var s settings
	if b, err := os.ReadFile(filepath.Join(dataDir(), "settings.json")); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveSettings(s settings) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(dataDir(), "settings.json"), b, 0o600)
}

// fetchRoutes pulls this terminator's route slice from xbind.
func fetchRoutes(ctx context.Context) ([]route, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://xbin/api/xbin/ingress-routes", nil)
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ingress-routes: %s", resp.Status)
	}
	var out struct {
		Routes []route `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	sort.Slice(out.Routes, func(i, j int) bool { return out.Routes[i].Host < out.Routes[j].Host })
	return out.Routes, nil
}

// staticConfig renders traefik's static config. The file provider watches the
// dynamic dir, so route changes hot-reload without a traefik restart; only a
// settings change (ACME email/staging/TLS mode) restarts the process.
func staticConfig(dir string, s settings) string {
	var b strings.Builder
	fmt.Fprintf(&b, `[entryPoints]
  [entryPoints.web]
    address = ":80"
  [entryPoints.websecure]
    address = ":443"

[providers]
  [providers.file]
    directory = %q
    watch = true

[log]
  level = "INFO"

[ping]
  entryPoint = "web"
`, filepath.Join(dir, "dynamic"))
	if !s.NoTLS {
		acme := "https://acme-v02.api.letsencrypt.org/directory"
		if s.Staging {
			acme = "https://acme-staging-v02.api.letsencrypt.org/directory"
		}
		fmt.Fprintf(&b, `
[certificatesResolvers.le.acme]
  email = %q
  storage = %q
  caServer = %q
  [certificatesResolvers.le.acme.tlsChallenge]
`, s.Email, filepath.Join(dir, "acme.json"), acme)
	}
	return b.String()
}

// dynamicConfig renders the per-host routers. Every router forwards to the
// one xbin service — xbind re-resolves the Host and enforces the target
// tile's public paths, so a rogue config here can't reroute anything.
func dynamicConfig(routes []route, s settings, forwardURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[http.services.xbin.loadBalancer]\n")
	fmt.Fprintf(&b, "  [[http.services.xbin.loadBalancer.servers]]\n    url = %q\n\n", forwardURL)
	for i, r := range routes {
		name := fmt.Sprintf("r%d", i)
		if s.NoTLS {
			fmt.Fprintf(&b, "[http.routers.%s]\n  rule = \"Host(`%s`)\"\n  entryPoints = [\"web\"]\n  service = \"xbin\"\n\n", name, r.Host)
			continue
		}
		// HTTPS router with ACME…
		fmt.Fprintf(&b, "[http.routers.%s]\n  rule = \"Host(`%s`)\"\n  entryPoints = [\"websecure\"]\n  service = \"xbin\"\n  [http.routers.%s.tls]\n    certResolver = \"le\"\n\n", name, r.Host, name)
		// …and an http→https redirect for the same host.
		fmt.Fprintf(&b, "[http.routers.%s_http]\n  rule = \"Host(`%s`)\"\n  entryPoints = [\"web\"]\n  service = \"xbin\"\n  middlewares = [\"to-https\"]\n\n", name, r.Host)
	}
	if !s.NoTLS && len(routes) > 0 {
		b.WriteString("[http.middlewares.to-https.redirectScheme]\n  scheme = \"https\"\n  permanent = true\n")
	}
	return b.String()
}

// render writes config; reports whether the STATIC half changed (→ restart).
func render(routes []route, s settings) (staticChanged bool, err error) {
	dir := dataDir()
	dyn := filepath.Join(dir, "dynamic")
	if err := os.MkdirAll(dyn, 0o700); err != nil {
		return false, err
	}
	fwd := os.Getenv("XBIN_INGRESS_FORWARD_URL")
	if fwd == "" {
		return false, fmt.Errorf("XBIN_INGRESS_FORWARD_URL not set — is this tile's provides {kind:\"ingress\"} intact?")
	}
	stat := staticConfig(dir, s)
	statPath := filepath.Join(dir, "traefik.toml")
	old, _ := os.ReadFile(statPath)
	if string(old) != stat {
		if err := os.WriteFile(statPath, []byte(stat), 0o600); err != nil {
			return false, err
		}
		staticChanged = true
	}
	dynCfg := dynamicConfig(routes, s, fwd)
	dynPath := filepath.Join(dyn, "routes.toml")
	oldDyn, _ := os.ReadFile(dynPath)
	if string(oldDyn) != dynCfg {
		if err := os.WriteFile(dynPath, []byte(dynCfg), 0o600); err != nil {
			return false, err
		}
	}
	return staticChanged, nil
}

// supervise (re)starts traefik on the current static config.
func supervise(restart bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.traefik != nil && !restart {
		return
	}
	if st.traefik != nil && st.traefik.Process != nil {
		_ = st.traefik.Process.Kill()
		_, _ = st.traefik.Process.Wait()
		st.traefik = nil
	}
	cmd := exec.Command("traefik", "--configfile", filepath.Join(dataDir(), "traefik.toml"))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr // tile log
	if err := cmd.Start(); err != nil {
		st.lastErr = "traefik start: " + err.Error()
		return
	}
	st.traefik = cmd
	st.restarts++
	go func() {
		_ = cmd.Wait()
		st.mu.Lock()
		if st.traefik == cmd {
			st.traefik = nil
			st.lastErr = "traefik exited"
		}
		st.mu.Unlock()
	}()
}

// reconcile is the whole control loop body: routes → config → process.
func reconcile(ctx context.Context) {
	routes, err := fetchRoutes(ctx)
	st.mu.Lock()
	if err != nil {
		st.lastErr = err.Error()
		st.mu.Unlock()
		return
	}
	changed := !reflect.DeepEqual(routes, st.routes)
	st.routes = routes
	set := st.set
	running := st.traefik != nil
	st.lastErr = ""
	// ACME can't run without an account email — serve plain :80 until the
	// owner sets one, and say so, rather than crash-looping traefik.
	if !set.NoTLS && set.Email == "" {
		set.NoTLS = true
		st.lastErr = "TLS is OFF until you set an ACME email in settings"
	}
	st.mu.Unlock()

	staticChanged, err := render(routes, set)
	if err != nil {
		st.mu.Lock()
		st.lastErr = err.Error()
		st.mu.Unlock()
		return
	}
	_ = changed // dynamic config is hot-reloaded by traefik's file watcher
	if staticChanged || !running {
		supervise(true)
	}
}

func main() {
	st.set = loadSettings()

	go func() {
		ctx := context.Background()
		reconcile(ctx)
		for range time.Tick(pollEvery) {
			reconcile(ctx)
		}
	}()

	mux := http.NewServeMux()
	// Status for the tile UI (its own frontend is admin of it).
	mux.Handle("GET /state", xbin.RoleFunc("reader", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"routes": st.routes, "settings": st.set,
			"running": st.traefik != nil, "restarts": st.restarts,
			"error":     st.lastErr,
			"acmeReady": st.set.NoTLS || st.set.Email != "",
		})
	}))
	mux.Handle("POST /settings", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		var s settings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := saveSettings(s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		st.mu.Lock()
		st.set = s
		st.mu.Unlock()
		go reconcile(context.Background()) // outlives this request
		w.WriteHeader(http.StatusNoContent)
	}))
	xbin.Serve(mux)
}
