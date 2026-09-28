package broker

// The vault of tile deployments (deployvault.go, 08-data §10): the broker
// fixture (deploy_fixture_test.go) with a real deployments plane booted over
// records for apps/calendar (main primary, dev beside it; protected in the
// tests that say so) and apps/shop (dev primary, main beside it), its answers
// installed as boot's stepBroker installs them. apps/calendar is owned by the
// fixture's writer, wes, which makes him a tile manager who isn't an admin.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/util"
)

// vaultFx is the fixture: the broker and the plane over its workspace.
type vaultFx struct {
	b    *Broker
	dp   *deployments.Plane
	root string
}

// vaultRecord writes tile's deployment record, made for owner ref owner:
// main and dev. Unprotected, the primary follows the work tree and the other
// is pinned; protected, the primary is pinned and live reload drives the
// other.
func vaultRecord(t *testing.T, root, tile, owner, primary string, protected bool) {
	t.Helper()
	other := map[string]string{util.MainDeployment: "dev", "dev": util.MainDeployment}[primary]
	live, pinned := primary, other
	if protected {
		live, pinned = other, primary
	}
	data, err := json.Marshal(map[string]any{
		"schema": 1, "tile": tile, "owner": owner, "created": "2026-09-27T10:12:03Z", "seq": 2,
		"liveReload": live, "lastLiveReload": live, "primary": primary, "protectedPrimary": protected,
		"nextDeploy": 2, "deployments": map[string]any{
			live:   map[string]any{"checkpoint": nil, "created": "2026-09-27T10:12:03Z", "by": "user:ana"},
			pinned: map[string]any{"checkpoint": lifeTree, "created": "2026-09-27T10:12:03Z", "by": "user:ana"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := lifeRecordPath(root, tile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// newVaultFx builds the fixture; protected protects apps/calendar's primary.
// The vault is plaintext (--insecure-vault) until a test initializes the
// barrier.
func newVaultFx(t *testing.T, protected bool) *vaultFx {
	t.Helper()
	b := deployBroker(t)
	b.AllowInsecureVault = true
	f := &vaultFx{b: b, root: b.Reg.Root}
	if err := b.Users.SetOwner(fxCalendar, "user:"+fxWriter); err != nil {
		t.Fatal(err)
	}
	vaultRecord(t, f.root, fxCalendar, b.Users.Owner(fxCalendar), util.MainDeployment, protected)
	vaultRecord(t, f.root, fxShop, "", "dev", false)
	f.dp = &deployments.Plane{Root: f.root, OwnerRef: b.Users.Owner}
	if err := f.dp.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	dp := f.dp
	b.DeploymentHooks = DeploymentHooks{DeploymentExists: dp.HasDeployment, AddressableDeployments: dp.Addressable,
		DeploymentSummary: dp.PrimarySummary}
	b.DeploymentAnswers = DeploymentAnswers{PrimaryOf: dp.Primary, DeploymentsOf: dp.DeploymentsOf,
		AddressedDeployment: dp.Addressed, RegistrationsActive: dp.RegistrationsActive,
		DeploymentEdges: dp.EdgePolicies, ReadDeploymentFile: dp.ReadDeploymentFile,
		WriteDeploymentFile: dp.WriteDeploymentFile, RemoveDeploymentFile: dp.RemoveDeploymentFile}
	if _, _, protectedNow, _ := dp.PrimarySummary(fxCalendar); protectedNow != protected {
		t.Fatalf("the record didn't load as written: protected %v", protectedNow)
	}
	return f
}

// vcall calls a vault route as p: rest is the {rest...} path value
// (component, then key), query the raw query ("" for none).
func (f *vaultFx) vcall(t *testing.T, method string, p auth.Principal, rest, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := map[string]http.HandlerFunc{"GET": f.b.apiVaultGet, "PUT": f.b.apiVaultPut, "DELETE": f.b.apiVaultDelete}[method]
	url := "/api/xbin/vault/" + rest
	if query != "" {
		url += "?" + query
	}
	return call(t, h, p, method, url, body, map[string]string{"rest": rest})
}

// want asserts rec's status and returns its decoded body.
func vwant(t *testing.T, rec *httptest.ResponseRecorder, code int, what string) map[string]any {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("%s: %d %s, want %d", what, rec.Code, strings.TrimSpace(rec.Body.String()), code)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m
}

func (f *vaultFx) put(t *testing.T, p auth.Principal, tile, key, value, query string, code int) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"value": value})
	return vwant(t, f.vcall(t, "PUT", p, tile+"/"+key, query, string(body)), code, "PUT "+tile+"/"+key+" as "+p.From()+"/"+p.Via+" "+query)
}

// value reads key's value as p (a backend) and fails unless it answers 200.
func (f *vaultFx) value(t *testing.T, p auth.Principal, tile, key string) string {
	t.Helper()
	m := vwant(t, f.vcall(t, "GET", p, tile+"/"+key, "", ""), 200, "GET "+tile+"/"+key+" as "+p.From()+"+"+p.Deployment)
	v, _ := m["value"].(string)
	return v
}

// list lists tile's vault as p: its keys and placeholders (nil when the
// answer has no placeholders field).
func (f *vaultFx) list(t *testing.T, p auth.Principal, tile, query string) (keys, placeholders []string, body map[string]any) {
	t.Helper()
	body = vwant(t, f.vcall(t, "GET", p, tile, query, ""), 200, "list "+tile+" as "+p.From()+" "+query)
	strs := func(v any) []string {
		if v == nil {
			return nil
		}
		out := []string{}
		for _, s := range v.([]any) {
			out = append(out, s.(string))
		}
		return out
	}
	return strs(body["keys"]), strs(body["placeholders"]), body
}

// devVaultFile is deployment dep of tile's vault file.
func (f *vaultFx) devVaultFile(tile, dep string) string {
	return filepath.Join(f.root, "data", "vault", ".deployments", util.TileKey(tile), dep+".json")
}

func eqStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

var (
	calTermDev    = auth.Principal{Component: fxCalendar, Via: "terminal", UserID: fxTerm, Deployment: "dev"}
	calTermFollow = auth.Principal{Component: fxCalendar, Via: "terminal", UserID: fxTerm}
	consoleAdmin  = auth.Principal{Component: fxConsole, Via: "instance"} // holds xbin at admin
)

// covers D127i T5 PO-2 PO-3 D127g D127d SC-DATA — one vault per deployment (08-data
// §10): main keeps today's file and format; dev's is its own file under
// data/vault/.deployments/<TileKey>/, which names its tile, and starts with
// the primary's key names only, as placeholders computed at each request
// (a key the primary gains shows at once, one it drops disappears), whose
// value reads answer the placeholder's 404. Neither side reads or writes the
// other's values. D30 holds per deployment: only the deployment's own
// backend reads a value; the tile's credentials never name another
// deployment. Admins and tile managers reach ?deployment=<name>, echoed;
// other tiles' admin credentials reach only the primary's vault. A primary
// that isn't main keeps its file beyond main, and main's today's. The
// barrier's first init seals the plaintext vaults beyond main too, and a
// file naming another tile is refused.
func TestVaultPerDeployment(t *testing.T) {
	f := newVaultFx(t, false)
	b := f.b
	mainFile := b.vaultPath(fxCalendar)
	f.put(t, calMain, fxCalendar, "STRIPE_KEY", "sk-live", "", 200)
	f.put(t, calMain, fxCalendar, "SMTP", "smtp-live", "", 200)

	t.Run("dev starts with placeholders", func(t *testing.T) {
		keys, ph, _ := f.list(t, calDev, fxCalendar, "")
		eqStrings(t, "dev's keys", keys, []string{})
		eqStrings(t, "dev's placeholders", ph, []string{"SMTP", "STRIPE_KEY"})
		rec := f.vcall(t, "GET", calDev, fxCalendar+"/STRIPE_KEY", "", "")
		body := vwant(t, rec, 404, "dev reads a placeholder")
		if got, want := body["error"], `vault key "STRIPE_KEY" has no value in deployment dev; a tile manager can copy it from the primary`; got != want {
			t.Errorf("placeholder read: %q, want %q", got, want)
		}
		vwant(t, f.vcall(t, "GET", calDev, fxCalendar+"/NOPE", "", ""), 404, "dev reads a key nobody has")
		if _, err := os.Lstat(f.devVaultFile(fxCalendar, "dev")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("reading dev's vault created its file (%v)", err)
		}
	})

	t.Run("each deployment writes its own", func(t *testing.T) {
		before, _ := os.ReadFile(mainFile)
		f.put(t, calDev, fxCalendar, "STRIPE_KEY", "sk-test", "", 200)
		if got := f.value(t, calDev, fxCalendar, "STRIPE_KEY"); got != "sk-test" {
			t.Errorf("dev reads %q", got)
		}
		if got := f.value(t, calMain, fxCalendar, "STRIPE_KEY"); got != "sk-live" {
			t.Errorf("main reads %q after dev's write", got)
		}
		if after, _ := os.ReadFile(mainFile); !bytes.Equal(before, after) {
			t.Errorf("dev's write changed main's file:\n%s\n%s", before, after)
		}
		keys, ph, _ := f.list(t, calDev, fxCalendar, "")
		eqStrings(t, "dev's keys", keys, []string{"STRIPE_KEY"})
		eqStrings(t, "dev's placeholders", ph, []string{"SMTP"})
		// main's file is today's: data/vault/<CompKey>.json, a plaintext map
		if mainFile != filepath.Join(f.root, "data", "vault", util.CompKey(fxCalendar)+".json") {
			t.Errorf("main's vault moved: %s", mainFile)
		}
		var mainDoc map[string]string
		if raw, _ := os.ReadFile(mainFile); json.Unmarshal(raw, &mainDoc) != nil || mainDoc["STRIPE_KEY"] != "sk-live" || len(mainDoc) != 2 {
			t.Errorf("main's file isn't today's plaintext map: %v", mainDoc)
		}
		var doc depVaultDoc
		if raw, err := os.ReadFile(f.devVaultFile(fxCalendar, "dev")); err != nil || json.Unmarshal(raw, &doc) != nil {
			t.Fatalf("dev's vault file: %v", err)
		}
		if doc.Tile != fxCalendar || doc.Plain["STRIPE_KEY"] != "sk-test" || len(doc.Plain) != 1 {
			t.Errorf("dev's vault file: %+v", doc)
		}
		// the primary's list is today's shape, byte for byte
		if got := f.vcall(t, "GET", calMain, fxCalendar, "", "").Body.String(); got != `{"keys":["SMTP","STRIPE_KEY"]}`+"\n" {
			t.Errorf("the primary's list: %q", got)
		}
	})

	t.Run("placeholders follow the primary", func(t *testing.T) {
		f.put(t, calMain, fxCalendar, "WEBHOOK", "wh", "", 200)
		vwant(t, f.vcall(t, "DELETE", calMain, fxCalendar+"/SMTP", "", ""), 200, "main deletes SMTP")
		_, ph, _ := f.list(t, calDev, fxCalendar, "")
		eqStrings(t, "dev's placeholders", ph, []string{"WEBHOOK"})
		got, err := b.VaultPlaceholders(fxCalendar, "dev")
		if err != nil {
			t.Fatal(err)
		}
		eqStrings(t, "VaultPlaceholders", got, []string{"WEBHOOK"})
		if got, _ := b.VaultPlaceholders(fxCalendar, util.MainDeployment); len(got) != 0 {
			t.Errorf("the primary has placeholders: %q", got)
		}
		sum, err := b.DeploymentVault(fxCalendar, "dev")
		if err != nil || sum != (deployments.VaultSummary{Keys: 2, Placeholders: 1}) {
			t.Errorf("dev's summary: %+v %v", sum, err)
		}
		if sum, _ := b.DeploymentVault(fxCalendar, util.MainDeployment); sum != (deployments.VaultSummary{Keys: 2}) {
			t.Errorf("main's summary: %+v", sum)
		}
	})

	t.Run("D30 per deployment", func(t *testing.T) {
		d30 := "a secret's value is readable only by the tile's backend; admins and tile terminals can list and set secrets, not read them (D30)"
		for _, c := range []struct {
			p     auth.Principal
			query string
			code  int
			why   string
		}{
			{calTermDev, "", 403, d30},
			{deployPerson(t, b, fxAdmin), "deployment=dev", 403, d30},
			{deployPerson(t, b, fxWriter), "deployment=dev", 403, d30},
			{calDev, "deployment=main", 403, "a tile's own credentials act only on their own deployment (dev)"},
			{calMain, "deployment=dev", 403, "a tile's own credentials act only on their own deployment (main)"},
			{calDev, "deployment=dev", 200, ""},
		} {
			body := vwant(t, f.vcall(t, "GET", c.p, fxCalendar+"/STRIPE_KEY", c.query, ""), c.code, c.p.From()+" "+c.query)
			if c.why != "" && body["error"] != c.why {
				t.Errorf("%s %s: %q, want %q", c.p.From(), c.query, body["error"], c.why)
			}
			if c.code == 200 && (body["value"] != "sk-test" || body["deployment"] != "dev") {
				t.Errorf("dev's named read: %v", body)
			}
		}
		// the binding is checked again on its own: an instance token of one
		// deployment never reads another's values, however the call came
		// to address it
		for _, c := range []struct {
			p   auth.Principal
			dep string
			ok  bool
		}{{calDev, "dev", true}, {calMain, "main", true}, {calMain, "", true},
			{calDev, "main", false}, {calMain, "dev", false}, {shopDev, "main", false}} {
			if got := !valueRefused(c.p, vaultCall{comp: c.p.Component, dep: c.dep, key: "K"}); got != c.ok {
				t.Errorf("%s+%q reads %s's values: %v, want %v", c.p.Component, c.p.Deployment, c.dep, got, c.ok)
			}
		}
		// the session that targets dev manages dev's vault, write-only
		f.put(t, calTermDev, fxCalendar, "DEV_ONLY", "d", "", 200)
		f.put(t, calDev, fxCalendar, "X", "x", "deployment=main", 403)
		if got := f.value(t, calDev, fxCalendar, "DEV_ONLY"); got != "d" {
			t.Errorf("dev reads %q", got)
		}
		vwant(t, f.vcall(t, "GET", calMain, fxCalendar+"/DEV_ONLY", "", ""), 404, "main reads dev's key")
	})

	t.Run("admins and managers name the deployment", func(t *testing.T) {
		ana, wes, tom := deployPerson(t, b, fxAdmin), deployPerson(t, b, fxWriter), deployPerson(t, b, fxTerm)
		keys, ph, body := f.list(t, ana, fxCalendar, "deployment=dev")
		eqStrings(t, "dev's keys for an admin", keys, []string{"DEV_ONLY", "STRIPE_KEY"})
		eqStrings(t, "dev's placeholders for an admin", ph, []string{"WEBHOOK"})
		if body["deployment"] != "dev" {
			t.Errorf("no echo: %v", body)
		}
		if _, _, body := f.list(t, ana, fxCalendar, ""); body["deployment"] != nil || body["placeholders"] != nil {
			t.Errorf("an admin's bare list is the primary's, in today's shape: %v", body)
		}
		if _, _, body := f.list(t, ana, fxCalendar, "deployment=main"); body["deployment"] != "main" || body["placeholders"] != nil {
			t.Errorf("an admin's list of main: %v", body)
		}
		// a tile manager who isn't an admin: dev's vault, in their own session
		f.put(t, wes, fxCalendar, "MGR", "m", "deployment=dev", 200)
		if got := f.value(t, calDev, fxCalendar, "MGR"); got != "m" {
			t.Errorf("dev reads the manager's value %q", got)
		}
		if b := f.put(t, wes, fxCalendar, "MGR", "m", "deployment=dev", 200); b["deployment"] != "dev" || b["ok"] != "true" {
			t.Errorf("a named write's answer: %v", b)
		}
		// …but the primary's vault stays as today: admins only
		for _, q := range []string{"", "deployment=main"} {
			if body := vwant(t, f.vcall(t, "GET", wes, fxCalendar, q, ""), 403, "a manager lists the primary "+q); body["error"] != errVaultPrivate {
				t.Errorf("a manager on the primary: %v", body)
			}
		}
		// neither admin nor manager: today's refusal, whatever it names
		for _, q := range []string{"", "deployment=dev", "deployment=nope"} {
			if body := vwant(t, f.vcall(t, "GET", tom, fxCalendar, q, ""), 403, "tom lists "+q); body["error"] != errVaultPrivate {
				t.Errorf("tom %s: %v", q, body)
			}
		}
		vwant(t, f.vcall(t, "GET", ana, fxCalendar, "deployment=nope", ""), 404, "an unknown deployment")
		vwant(t, f.vcall(t, "GET", ana, fxCalendar, "deployment=Dev", ""), 400, "a bad name")
		// another tile's admin credentials: the primary's vault as today, never dev's (D127d)
		f.list(t, consoleAdmin, fxCalendar, "")
		vwant(t, f.vcall(t, "GET", consoleAdmin, fxCalendar, "deployment=dev", ""), 403, "another tile's admin names dev")
		f.put(t, consoleAdmin, fxCalendar, "ADMIN_SET", "a", "", 200)
		vwant(t, f.vcall(t, "DELETE", consoleAdmin, fxCalendar+"/ADMIN_SET", "", ""), 200, "another tile's admin deletes")
	})

	t.Run("a primary beyond main", func(t *testing.T) {
		// apps/shop: dev is the primary; main is beside it, with today's file
		f.put(t, shopDev, fxShop, "PAYMENT", "live", "", 200)
		f.put(t, shopMain, fxShop, "OLD", "o", "", 200)
		if _, err := os.Stat(f.devVaultFile(fxShop, "dev")); err != nil {
			t.Errorf("the primary dev's vault isn't its own file: %v", err)
		}
		var m map[string]string
		if raw, _ := os.ReadFile(b.vaultPath(fxShop)); json.Unmarshal(raw, &m) != nil || !reflect.DeepEqual(m, map[string]string{"OLD": "o"}) {
			t.Errorf("main's file: %v", m)
		}
		keys, ph, _ := f.list(t, shopMain, fxShop, "")
		eqStrings(t, "main's keys", keys, []string{"OLD"})
		eqStrings(t, "main's placeholders", ph, []string{"PAYMENT"})
		if _, ph, _ := f.list(t, shopDev, fxShop, ""); ph != nil {
			t.Errorf("the primary lists placeholders: %q", ph)
		}
		if _, ph, _ := f.list(t, deployPerson(t, b, fxAdmin), fxShop, ""); ph != nil {
			t.Errorf("an admin's bare list isn't the primary's: %q", ph)
		}
		// a reset with vault:true empties a non-primary vault, main's included
		if err := b.EmptyDeploymentVault(fxShop, util.MainDeployment); err != nil {
			t.Fatal(err)
		}
		keys, ph, _ = f.list(t, shopMain, fxShop, "")
		eqStrings(t, "main's keys after the reset", keys, []string{})
		eqStrings(t, "main's placeholders after the reset", ph, []string{"PAYMENT"})
		if err := b.EmptyDeploymentVault(fxShop, "dev"); err == nil {
			t.Error("the primary's vault was emptied")
		}
		if got := f.value(t, shopDev, fxShop, "PAYMENT"); got != "live" {
			t.Errorf("the primary reads %q", got)
		}
	})

	t.Run("a file naming another tile is refused", func(t *testing.T) {
		p := f.devVaultFile(fxCalendar, "dev")
		saved, _ := os.ReadFile(p)
		forged, _ := json.Marshal(depVaultDoc{Tile: fxShop, Plain: map[string]string{"STRIPE_KEY": "stolen"}})
		if err := os.WriteFile(p, forged, 0o600); err != nil {
			t.Fatal(err)
		}
		vwant(t, f.vcall(t, "GET", calDev, fxCalendar+"/STRIPE_KEY", "", ""), 500, "a forged file")
		if err := os.WriteFile(p, saved, 0o600); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("the barrier seals vaults beyond main", func(t *testing.T) {
		initBarrier(t, b)
		for _, p := range []string{f.devVaultFile(fxCalendar, "dev"), f.devVaultFile(fxShop, "dev")} {
			raw, _ := os.ReadFile(p)
			var doc depVaultDoc
			if json.Unmarshal(raw, &doc) != nil || doc.Enc != 1 || doc.Plain != nil || len(doc.Data) == 0 || doc.Tile == "" {
				t.Errorf("%s isn't sealed: %s", p, raw)
			}
			if bytes.Contains(raw, []byte("sk-test")) || bytes.Contains(raw, []byte("PAYMENT")) {
				t.Errorf("%s holds plaintext: %s", p, raw)
			}
		}
		if got := f.value(t, calDev, fxCalendar, "STRIPE_KEY"); got != "sk-test" {
			t.Errorf("dev reads %q after sealing", got)
		}
		f.put(t, calDev, fxCalendar, "AFTER", "sealed", "", 200)
		if raw, _ := os.ReadFile(f.devVaultFile(fxCalendar, "dev")); bytes.Contains(raw, []byte("sealed")) {
			t.Errorf("a write after init left plaintext: %s", raw)
		}
		b.Barrier().Seal()
		vwant(t, f.vcall(t, "GET", calDev, fxCalendar, "", ""), 503, "dev lists while sealed")
		f.put(t, calDev, fxCalendar, "K", "v", "", 503)
	})
}

// initBarrier initializes b's vault barrier as UnsealOrInit's first use
// does for the vault: the barrier, then the migration of the plaintext
// vaults. It mounts no encrypted resource (the fixture's filesystem
// resources would outlive the test's temporary directory).
func initBarrier(t *testing.T, b *Broker) {
	t.Helper()
	if err := b.Barrier().Init("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	b.migrateVaults()
}

// lockedBuffer collects log lines from any goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// captureLog sends slog's default logger to a buffer for the test's life.
func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf, old := &lockedBuffer{}, slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// covers D127i T5 — the vault copy (08-data §10; 11-contract §1.8) is a tile
// manager's act in a person's own session: terminal and agent tokens (a
// manager's own included), the tile's backends, other tiles' admin
// credentials, view-as sessions and people who don't manage the tile are
// refused. It copies the named keys, or all, from the primary into a
// non-primary deployment, overwriting that deployment's own values and never
// touching the primary's; keys the primary lacks come back as missing. A
// dry run writes nothing; exactly one of keys and all; never onto the
// primary; an unknown deployment 404s; a sealed vault answers the vault
// routes' 503. The audit line names keys, never values.
func TestVaultCopyManagerOnly(t *testing.T) {
	f := newVaultFx(t, false)
	b := f.b
	log := captureLog(t)
	f.put(t, calMain, fxCalendar, "STRIPE_KEY", "sk-live-secret", "", 200)
	f.put(t, calMain, fxCalendar, "SMTP", "smtp-live-secret", "", 200)
	f.put(t, calDev, fxCalendar, "STRIPE_KEY", "sk-test", "", 200)
	mainBefore, _ := os.ReadFile(b.vaultPath(fxCalendar))
	req := func(keys []string, all bool) deployments.VaultCopyRequest {
		return deployments.VaultCopyRequest{Tile: fxCalendar, Deployment: "dev", Keys: keys, All: all}
	}
	refused := func(p auth.Principal, r deployments.VaultCopyRequest, status int, why string) {
		t.Helper()
		_, err := b.VaultCopy(p, r)
		var de *deployments.Error
		if !errors.As(err, &de) || de.Status != status || !strings.Contains(de.Msg, why) {
			t.Errorf("vault copy by %s/%s: %v, want %d %q", p.From(), p.Via, err, status, why)
		}
	}
	ana, wes := deployPerson(t, b, fxAdmin), deployPerson(t, b, fxWriter)
	anaTerm := auth.Principal{Component: fxCalendar, Via: "terminal", UserID: fxAdmin, User: ana.User, Access: ana.Access}
	session := "a tile manager's act, done in a person's own session"
	notManager := "a tile manager's act: the tile's owner, its org's admins, or a workspace admin"
	viewAs := wes
	viewAs.Impersonator = fxAdmin
	for _, c := range []struct {
		p    auth.Principal
		why  string
		code int
	}{
		{anaTerm, session, 403},
		{calTermDev, session, 403},
		{calDev, session, 403},
		{calMain, session, 403},
		{consoleAdmin, session, 403},
		{deployPerson(t, b, fxTerm), notManager, 403},
		{deployPerson(t, b, fxReader), notManager, 403},
		{viewAs, "view-as", 403},
	} {
		refused(c.p, req([]string{"SMTP"}, false), c.code, c.why)
	}
	refused(wes, req(nil, false), 400, "exactly one of keys and all")
	refused(wes, req([]string{"SMTP"}, true), 400, "exactly one of keys and all")
	refused(wes, deployments.VaultCopyRequest{Tile: fxCalendar, Deployment: "main", Keys: []string{"SMTP"}}, 409, "main is the primary of apps/calendar")
	refused(wes, deployments.VaultCopyRequest{Tile: fxCalendar, Deployment: "nope", Keys: []string{"SMTP"}}, 404, `apps/calendar has no deployment "nope"`)
	if _, err := os.Lstat(f.devVaultFile(fxCalendar, "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused copy wrote a vault (%v)", err)
	}

	// a dry run answers and writes nothing
	devBefore, _ := os.ReadFile(f.devVaultFile(fxCalendar, "dev"))
	dry := req([]string{"SMTP", "MISSING"}, false)
	dry.DryRun = true
	ans, err := b.VaultCopy(wes, dry)
	if err != nil || !reflect.DeepEqual(ans, deployments.VaultCopyAnswer{Copied: []string{"SMTP"}, Missing: []string{"MISSING"}}) {
		t.Errorf("dry run: %+v %v", ans, err)
	}
	if after, _ := os.ReadFile(f.devVaultFile(fxCalendar, "dev")); !bytes.Equal(devBefore, after) {
		t.Error("a dry run wrote dev's vault")
	}

	// a tile manager who isn't an admin copies key by key
	ans, err = b.VaultCopy(wes, req([]string{"SMTP", "MISSING", "SMTP"}, false))
	if err != nil || !reflect.DeepEqual(ans, deployments.VaultCopyAnswer{Copied: []string{"SMTP"}, Missing: []string{"MISSING"}}) {
		t.Fatalf("copy: %+v %v", ans, err)
	}
	if got := f.value(t, calDev, fxCalendar, "SMTP"); got != "smtp-live-secret" {
		t.Errorf("dev's SMTP = %q", got)
	}
	if got := f.value(t, calDev, fxCalendar, "STRIPE_KEY"); got != "sk-test" {
		t.Errorf("dev's own STRIPE_KEY changed without being named: %q", got)
	}
	_, ph, _ := f.list(t, calDev, fxCalendar, "")
	eqStrings(t, "dev's placeholders after the copy", ph, []string{})

	// an admin copies all, overwriting dev's own value; the primary is untouched
	ans, err = b.VaultCopy(ana, req(nil, true))
	if err != nil || !reflect.DeepEqual(ans, deployments.VaultCopyAnswer{Copied: []string{"SMTP", "STRIPE_KEY"}, Missing: []string{}}) {
		t.Fatalf("copy all: %+v %v", ans, err)
	}
	if got := f.value(t, calDev, fxCalendar, "STRIPE_KEY"); got != "sk-live-secret" {
		t.Errorf("dev's STRIPE_KEY = %q after copying all", got)
	}
	if after, _ := os.ReadFile(b.vaultPath(fxCalendar)); !bytes.Equal(mainBefore, after) {
		t.Error("a vault copy wrote the primary's vault")
	}
	if again, err := b.VaultCopy(ana, req(nil, true)); err != nil || !reflect.DeepEqual(again, ans) {
		t.Errorf("copy all again (idempotent): %+v %v", again, err)
	}

	// the audit names keys and who, never values
	out := log.String()
	for _, want := range []string{"vault copy", "tile=apps/calendar", "to=dev", "SMTP", "STRIPE_KEY", "by=user:" + fxWriter, "by=user:" + fxAdmin} {
		if !strings.Contains(out, want) {
			t.Errorf("the audit lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"sk-live-secret", "smtp-live-secret", "sk-test"} {
		if strings.Contains(out, secret) {
			t.Errorf("the audit logs a value (%s):\n%s", secret, out)
		}
	}

	// sealed: the vault routes' 503
	initBarrier(t, b)
	b.Barrier().Seal()
	refused(ana, req(nil, true), 503, "vault is sealed")
}

// covers D127p T5 NP-06-9 — a protected primary's vault writes are tile
// managers' acts (06-security T5.4; 16-open-questions O5): its own backend
// and admins in their own session write it; the tile's terminal and agent
// sessions never reach it (a session that follows the primary is refused,
// one that targets dev writes dev's vault); another tile's admin
// credentials, which aren't a manager, may list it but not write it; a tile
// manager who isn't an admin gets no session path to it (the v1 default),
// while dev's vault stays theirs to manage. Unprotected, the same writes
// pass as today.
func TestProtectedPrimaryVaultWritesManagerOnly(t *testing.T) {
	f := newVaultFx(t, true)
	b := f.b
	ana, wes := deployPerson(t, b, fxAdmin), deployPerson(t, b, fxWriter)
	f.put(t, calMain, fxCalendar, "STRIPE_KEY", "live", "", 200)
	vwant(t, f.vcall(t, "DELETE", calMain, fxCalendar+"/STRIPE_KEY", "", ""), 200, "the backend deletes")
	f.put(t, calMain, fxCalendar, "STRIPE_KEY", "live", "", 200)
	f.put(t, ana, fxCalendar, "SMTP", "s", "", 200)
	f.put(t, ana, fxCalendar, "SMTP2", "s", "deployment=main", 200)
	vwant(t, f.vcall(t, "DELETE", ana, fxCalendar+"/SMTP2", "", ""), 200, "an admin deletes")

	before, _ := os.ReadFile(b.vaultPath(fxCalendar))
	session := "the primary of apps/calendar is protected: terminal and agent sessions can't target it"
	protected := "the primary of apps/calendar (main) is protected"
	for _, c := range []struct {
		p     auth.Principal
		query string
		code  int
		why   string
	}{
		{calTermFollow, "", 403, session},
		{calTermFollow, "deployment=main", 403, session},
		{auth.Principal{Component: fxCalendar, Via: "terminal", UserID: fxTerm, Deployment: "main"}, "", 403, session},
		{consoleAdmin, "", 403, protected},
		{wes, "", 403, errVaultPrivate},
		{wes, "deployment=main", 403, errVaultPrivate},
		{deployPerson(t, b, fxTerm), "", 403, errVaultPrivate},
	} {
		for _, method := range []string{"PUT", "DELETE"} {
			rec := f.vcall(t, method, c.p, fxCalendar+"/STRIPE_KEY", c.query, `{"value":"hijacked"}`)
			if body := vwant(t, rec, c.code, method+" as "+c.p.From()+"/"+c.p.Via+" "+c.query); !strings.HasPrefix(body["error"].(string), c.why) {
				t.Errorf("%s as %s/%s %s: %q, want %q…", method, c.p.From(), c.p.Via, c.query, body["error"], c.why)
			}
		}
	}
	if after, _ := os.ReadFile(b.vaultPath(fxCalendar)); !bytes.Equal(before, after) {
		t.Errorf("a refused write changed the protected primary's vault:\n%s\n%s", before, after)
	}
	if got := f.value(t, calMain, fxCalendar, "STRIPE_KEY"); got != "live" {
		t.Errorf("the protected primary reads %q", got)
	}
	// listing isn't writing: another tile's admin still lists the keys
	if keys, _, _ := f.list(t, consoleAdmin, fxCalendar, ""); !reflect.DeepEqual(keys, []string{"SMTP", "STRIPE_KEY"}) {
		t.Errorf("another tile's admin lists %q", keys)
	}
	// the session that targets dev, and the manager, write dev's vault
	f.put(t, calTermDev, fxCalendar, "STRIPE_KEY", "test", "", 200)
	f.put(t, wes, fxCalendar, "SMTP", "dev-smtp", "deployment=dev", 200)
	if got := f.value(t, calDev, fxCalendar, "STRIPE_KEY"); got != "test" {
		t.Errorf("dev reads %q", got)
	}
	if got := f.value(t, calMain, fxCalendar, "STRIPE_KEY"); got != "live" {
		t.Errorf("the protected primary reads %q after dev's writes", got)
	}

	// unprotected, another tile's admin writes the primary's vault as today
	u := newVaultFx(t, false)
	u.put(t, consoleAdmin, fxCalendar, "STRIPE_KEY", "set", "", 200)
	u.put(t, calTermFollow, fxCalendar, "SMTP", "set", "", 200)
	vwant(t, u.vcall(t, "DELETE", consoleAdmin, fxCalendar+"/SMTP", "", ""), 200, "unprotected: another tile's admin deletes")
}

// covers D127c D127g SC-DATA NP-02-13 — synthetic data, the vault half: a terminal
// or agent token whose session targets dev writes vault values (bx vault set
// sends PUT /vault/<self>/<key> with it) only into dev's vault, and lists
// dev's keys; the same user's session that follows the primary writes the
// primary's, as today, and on a tile whose primary is dev it writes dev's.
// The (S, dev) data half is WP-40's kvAccess by deployment
// (TestKVNamespacePerDeployment); this test gains its kv rows when both
// have merged.
func TestTargetedSessionWritesOnlyItsNamespace(t *testing.T) {
	f := newVaultFx(t, false)
	b := f.b
	f.put(t, calMain, fxCalendar, "API_KEY", "live", "", 200)
	mainBefore, _ := os.ReadFile(b.vaultPath(fxCalendar))

	f.put(t, calTermDev, fxCalendar, "API_KEY", "synthetic", "", 200)
	f.put(t, calTermDev, fxCalendar, "SEED_USER", "alice", "", 200)
	if after, _ := os.ReadFile(b.vaultPath(fxCalendar)); !bytes.Equal(mainBefore, after) {
		t.Errorf("the session targeting dev wrote the primary's vault:\n%s\n%s", mainBefore, after)
	}
	if got := f.value(t, calDev, fxCalendar, "API_KEY"); got != "synthetic" {
		t.Errorf("dev's API_KEY = %q", got)
	}
	if got := f.value(t, calMain, fxCalendar, "API_KEY"); got != "live" {
		t.Errorf("main's API_KEY = %q", got)
	}
	keys, ph, _ := f.list(t, calTermDev, fxCalendar, "")
	eqStrings(t, "the dev session's keys", keys, []string{"API_KEY", "SEED_USER"})
	eqStrings(t, "the dev session's placeholders", ph, []string{})

	// the same user's session that follows the primary: the primary's vault
	f.put(t, calTermFollow, fxCalendar, "PRIMARY_ONLY", "p", "", 200)
	if got := f.value(t, calMain, fxCalendar, "PRIMARY_ONLY"); got != "p" {
		t.Errorf("main's PRIMARY_ONLY = %q", got)
	}
	vwant(t, f.vcall(t, "GET", calDev, fxCalendar+"/PRIMARY_ONLY", "", ""), 404, "dev reads the primary's new key")
	keys, ph, body := f.list(t, calTermFollow, fxCalendar, "")
	eqStrings(t, "the following session's keys", keys, []string{"API_KEY", "PRIMARY_ONLY"})
	if ph != nil || body["deployment"] != nil {
		t.Errorf("the following session's list isn't today's: %v", body)
	}

	// apps/shop's primary is dev: a following session writes dev's file
	shopFollow := auth.Principal{Component: fxShop, Via: "terminal", UserID: fxTerm}
	f.put(t, shopFollow, fxShop, "K", "v", "", 200)
	if got := f.value(t, shopDev, fxShop, "K"); got != "v" {
		t.Errorf("shop's dev reads %q", got)
	}
	if _, err := os.Lstat(b.vaultPath(fxShop)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a session following shop's primary (dev) wrote main's vault (%v)", err)
	}
}
