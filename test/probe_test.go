//go:build integration

package test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The probe backend (15-test-plan §5.2): one inline Go backend that says
// which code runs, which tree is bound, what it was given, and who calls it.
// examples/ is never edited for this.

// probeFile is the file GET /file reads at the tile's canonical path (the
// backend's working directory): which tree is bound there.
const probeFile = "probe.txt"

// writeProbe writes the probe at ws/tile, or turns an existing one into
// marker: backend/main.go with marker compiled in (GET /v), probeFile holding
// marker (GET /file), a go.mod requiring the sdk, and a scope of the tile's
// own with a kv, a bus and a cron resource (XBIN_RES_KV, _BUS, _CRON; no
// file-backed kind, so no gocryptfs is needed). Only files whose bytes change
// are written, so a new marker is a save of main.go and probeFile, as an
// editor makes it. The module path comes from tile: two probes in one
// workspace never make go.work refuse a module twice.
//
// The probe serves:
//   - GET /v: the marker compiled in;
//   - GET /file: probeFile, read at the canonical path;
//   - GET /env: XBIN_RES_*, XBIN_IFACE_*_URL and XBIN_DEPLOYMENT (when set);
//   - PUT and GET /kv/{k}: the kv resource through the SDK (404 when unset);
//   - POST to any other path (/tick for cron, /on for the bus, by
//     convention): recorded as a delivery with its caller, and a bus event's
//     subscription, topic and data;
//   - GET /seen: the deliveries recorded, oldest first;
//   - GET /caller: xbin.Caller(r), with the X-XBin-Deployment header;
//   - GET /self: the statuses and bodies of its own /api/<self>/v and
//     /api/<self>+main/v, called through the gateway.
func writeProbe(t *testing.T, ws, tile, marker string) {
	t.Helper()
	dir := filepath.Join(ws, filepath.FromSlash(tile))
	mod := "probe/" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/-._~", r) {
			return r
		}
		return '_'
	}, tile)
	res := func(name string) string {
		return `{"target":"res:` + tile + `/` + name + `","role":"writer"}`
	}
	// The manifest last: the tile becomes a Go component once its code is there.
	for _, f := range []struct{ rel, content string }{
		{"go.mod", "module " + mod + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n"},
		{"scope.json", `{"resources":{"kv":{"type":"kv"},"bus":{"type":"bus"},"cron":{"type":"cron"}}}` + "\n"},
		{"backend/main.go", strings.NewReplacer("__MARKER__", strconv.Quote(marker), "__FILE__", strconv.Quote(probeFile)).Replace(probeSource)},
		{probeFile, marker},
		{"xbin.json", `{"runtime":"go","uses":[` + res("kv") + `,` + res("bus") + `,` + res("cron") + `]}` + "\n"},
	} {
		writeIfChanged(t, filepath.Join(dir, filepath.FromSlash(f.rel)), f.content)
	}
}

// writeIfChanged writes content to p (creating its directory) unless p holds
// exactly that already, and says whether it wrote.
func writeIfChanged(t *testing.T, p, content string) bool {
	t.Helper()
	if b, err := os.ReadFile(p); err == nil && string(b) == content {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return true
}

// waitProbe waits until tile's probe answers marker on GET /v through d, and
// fails the test with the last answer otherwise. The bound covers a fresh
// workspace's cold build; a failed build doesn't mend itself, so an answer
// that says so ends the wait.
func waitProbe(t *testing.T, d *isoDaemon, tile, marker string) {
	t.Helper()
	var code int
	var body string
	waitFor(func() bool {
		code, body = d.do(t, "GET", "/api/"+tile+"/v", "")
		return code == 200 && body == marker || strings.Contains(body, "build failed")
	}, 3*time.Minute)
	if code != 200 || body != marker {
		t.Fatalf("probe %s never served %q: %d %s", tile, marker, code, body)
	}
}

// probeSource is backend/main.go; writeProbe puts the quoted marker in place
// of __MARKER__, and probeFile in place of __FILE__.
const probeSource = `package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const marker = __MARKER__

// delivery is one POST xbind made: a cron tick, a bus event, or another call.
type delivery struct {
	Path         string ` + "`json:\"path\"`" + `
	From         string ` + "`json:\"from\"`" + `
	Role         string ` + "`json:\"role\"`" + `
	Deployment   string ` + "`json:\"deployment\"`" + `
	Subscription string ` + "`json:\"subscription,omitempty\"`" + `
	Topic        string ` + "`json:\"topic,omitempty\"`" + `
	Data         string ` + "`json:\"data,omitempty\"`" + `
}

func main() {
	var mu sync.Mutex
	seen := []delivery{}
	kv := xbin.KV(xbin.Resource("kv"))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, marker)
	})
	mux.HandleFunc("GET /file", func(w http.ResponseWriter, r *http.Request) {
		wd, err := os.Getwd()
		if err != nil {
			xbin.WriteError(w, 500, err.Error())
			return
		}
		b, err := os.ReadFile(filepath.Join(wd, __FILE__))
		if err != nil {
			xbin.WriteError(w, 404, err.Error())
			return
		}
		w.Write(b)
	})
	mux.HandleFunc("GET /env", func(w http.ResponseWriter, r *http.Request) {
		env := map[string]string{}
		for _, e := range os.Environ() {
			k, v, _ := strings.Cut(e, "=")
			if strings.HasPrefix(k, "XBIN_RES_") || k == "XBIN_DEPLOYMENT" ||
				strings.HasPrefix(k, "XBIN_IFACE_") && strings.HasSuffix(k, "_URL") {
				env[k] = v
			}
		}
		xbin.WriteJSON(w, 200, env)
	})
	mux.HandleFunc("PUT /kv/{k}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := kv.Put(r.PathValue("k"), b); err != nil {
			xbin.WriteError(w, 502, err.Error())
			return
		}
		xbin.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /kv/{k}", func(w http.ResponseWriter, r *http.Request) {
		b, err := kv.Get(r.PathValue("k"))
		if errors.Is(err, xbin.ErrNotFound) {
			xbin.WriteError(w, 404, "no such key")
			return
		} else if err != nil {
			xbin.WriteError(w, 502, err.Error())
			return
		}
		w.Write(b)
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		d := delivery{Path: r.URL.Path, From: c.From, Role: c.Role, Deployment: r.Header.Get("X-XBin-Deployment")}
		var ev xbin.BusEvent
		if b, _ := io.ReadAll(r.Body); json.Unmarshal(b, &ev) == nil && ev.Subscription != "" {
			d.Subscription, d.Topic, d.Data = ev.Subscription, ev.Topic, string(ev.Data)
		}
		mu.Lock()
		seen = append(seen, d)
		mu.Unlock()
		xbin.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /seen", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		xbin.WriteJSON(w, 200, seen)
	})
	mux.HandleFunc("GET /caller", func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		xbin.WriteJSON(w, 200, map[string]any{
			"from": c.From, "role": c.Role, "owner": c.Owner, "user": c.User,
			"userLevel": c.UserLevel, "viewedBy": c.ViewedBy,
			"deployment": r.Header.Get("X-XBin-Deployment"),
		})
	})
	mux.HandleFunc("GET /self", func(w http.ResponseWriter, r *http.Request) {
		call := func(target string) (int, string) {
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://xbin/api/"+target+"/v", nil)
			resp, err := xbin.Client().Do(req)
			if err != nil {
				return 0, err.Error()
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		selfCode, selfBody := call(xbin.Self())
		mainCode, mainBody := call(xbin.Self() + "+main")
		xbin.WriteJSON(w, 200, map[string]any{
			"self": selfCode, "selfBody": selfBody, "main": mainCode, "mainBody": mainBody,
		})
	})
	xbin.Serve(mux)
}
`
