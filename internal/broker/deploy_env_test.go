package broker

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// The zero-state data goldens (15-test-plan §2.7): what a tile that never
// opted into tile deployments stores, and where, and the env its backend
// gets — hand-maintained literals, written against today's code before any
// feature code. Changing one is a compat change (12-compat), never a
// regeneration. The workspace below is shared by the goldens of this file
// and deploy_backup_test.go; nothing here edits testWorkspace.

// zeroDataWorkspace writes a workspace that exercises every store and every
// env branch a zero-state tile can reach:
//   - apps/cal roots a scope holding each resource type, and uses them all,
//     plus a granted workspace-level kv and an ungranted foreign resource;
//   - apps/cal/widget sits in apps/cal's scope without rooting it;
//   - apps/mail reads apps/cal's kv and sqlite across scopes (granted) and
//     binds one http slot; apps/notes binds a slot to a provider instance;
//     apps/agent binds a multi slot to both;
//   - a terminator (apps/traefik), a zone-delegated http expose (apps/cms), a
//     stream-interface client (apps/client), a lan-ingress provider and its
//     client (apps/vpn, apps/db);
//   - the workspace default archiver binding.
func zeroDataWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"xbin.json": `{"schema":1,
			"resources":{"shared":{"type":"kv"},"wsfiles":{"type":"filesystem"}},
			"grants":[
				{"from":"apps/cal","target":"res:workspace/shared","role":"writer"},
				{"from":"apps/mail","target":"res:apps/cal/events","role":"reader"},
				{"from":"apps/mail","target":"res:apps/cal/db","role":"reader"}
			],
			"bindings":{
				"*":           {"@archive":{"ref":"apps/archiver"}},
				"apps/mail":   {"llm":{"ref":"apps/llm"}},
				"apps/notes":  {"llm":{"ref":"apps/pool#team"}},
				"apps/agent":  {"channels":[{"ref":"apps/llm"},{"ref":"apps/pool#team"}]},
				"apps/cms":    {"web":{"ref":"runtime","zone":"*.sites.example.com"}},
				"apps/client": {"db":{"ref":"apps/traefik#web"}},
				"apps/db":     {"vpnlan":{"ref":"apps/vpn"}}
			},
			"ifaceInstances":{"apps/pool":{"team":"/teams/team"}}}`,
		"apps/cal/scope.json": `{"resources":{
			"events":{"type":"kv"},"my-cache":{"type":"kv"},"bus":{"type":"bus"},"db":{"type":"sqlite"},
			"files":{"type":"filesystem"},"pics":{"type":"blob"},"ticks":{"type":"cron"}}}`,
		"apps/cal/xbin.json": `{"runtime":"go",
			"expose":{"roles":{"reader":"r","writer":"w"}},
			"uses":[
				{"target":"res:apps/cal/events","role":"writer"},
				{"target":"res:apps/cal/my-cache","role":"writer"},
				{"target":"res:apps/cal/bus","role":"writer"},
				{"target":"res:apps/cal/db","role":"writer"},
				{"target":"res:apps/cal/files","role":"writer"},
				{"target":"res:apps/cal/pics","role":"writer"},
				{"target":"res:apps/cal/ticks","role":"writer"},
				{"target":"res:workspace/shared","role":"writer"},
				{"target":"res:workspace/wsfiles","role":"writer"},
				{"target":"res:apps/mail/nope","role":"reader"}]}`,
		"apps/cal/index.html":       `<html></html>`,
		"apps/cal/widget/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/cal/events","role":"reader"}]}`,
		"apps/mail/xbin.json": `{"runtime":"go",
			"interfaces":{"llm":{"kind":"http","service":"openai"}},
			"uses":[{"target":"res:apps/cal/events","role":"reader"},{"target":"res:apps/cal/db","role":"reader"}]}`,
		"apps/notes/xbin.json": `{"runtime":"go","interfaces":{"llm":{"kind":"http","service":"openai"}}}`,
		"apps/agent/xbin.json": `{"runtime":"go","interfaces":{"channels":{"kind":"http","service":"openai","multi":true}}}`,
		"apps/llm/xbin.json": `{"runtime":"go","expose":{"roles":{"writer":"w"}},
			"provides":{"chat":{"kind":"http","service":"openai","role":"writer"}}}`,
		"apps/pool/xbin.json": `{"runtime":"go","expose":{"roles":{"writer":"w"}},
			"provides":{"chat":{"kind":"http","service":"openai","role":"writer","instances":true}}}`,
		"apps/traefik/xbin.json": `{"runtime":"go",
			"provides":{"public":{"kind":"ingress"}},
			"interfaces":{"net":{"kind":"net"}},
			"exposes":{"web":{"kind":"stream","port":80}}}`,
		"apps/cms/xbin.json":    `{"runtime":"go","exposes":{"web":{"kind":"http","paths":["/*"]}}}`,
		"apps/client/xbin.json": `{"runtime":"go","interfaces":{"db":{"kind":"stream"}}}`,
		"apps/vpn/xbin.json": `{"runtime":"go",
			"provides":{"net":{"kind":"net"},"lan":{"kind":"lan-ingress"}},
			"uses":[{"target":"cap:net-admin","role":"writer"}]}`,
		"apps/db/xbin.json":    `{"runtime":"go","interfaces":{"vpnlan":{"kind":"lan-ingress"}}}`,
		"apps/plain/xbin.json": `{"runtime":"go"}`,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// zeroDataBroker opens a broker on zeroDataWorkspace, wired as production
// wires it (a users store attached).
func zeroDataBroker(t *testing.T) *Broker {
	t.Helper()
	reg, err := registry.Open(zeroDataWorkspace(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	testUsers(t, b)
	b.IngressSocket = func(source string) string { return "/run/igw-" + source + ".sock" }
	return b
}

// zeroDataCall runs one broker handler as p, with the {rest...} path value
// set when rest != "".
func zeroDataCall(t *testing.T, h func(http.ResponseWriter, *http.Request), method, rest, body string, p auth.Principal) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/xbin/x", strings.NewReader(body))
	if rest != "" {
		r.SetPathValue("rest", rest)
	}
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// covers P5 SC-ZERO PO-3 — a zero-state backend's resource and interface env
// is today's, as a joined string, for every branch EnvFor has: same-scope kv,
// bus, blob, cron (a res: id), sqlite and filesystem (a path under the
// decrypted mount), a granted workspace-level resource, cross-scope kv (an
// id) and sqlite (nothing), ungranted uses (nothing), single, instance and
// multi http slots, a terminator's forward door, a stream interface, a
// lan-ingress leg and its provider's map. No XBIN_DEPLOYMENT, ever.
func TestZeroStateBackendEnv(t *testing.T) {
	b := zeroDataBroker(t)
	root := b.Reg.Root
	env := func(comp string) string {
		c, ok := b.Reg.Component(comp)
		if !ok {
			t.Fatalf("fixture: no component %s", comp)
		}
		lines := b.EnvFor(c)
		for _, l := range lines {
			if strings.HasPrefix(l, "XBIN_DEPLOYMENT") {
				t.Errorf("%s: a zero-state backend got %s", comp, l)
			}
		}
		return strings.ReplaceAll(strings.Join(lines, "\n"), root, "<ws>")
	}
	for comp, want := range map[string]string{
		"apps/cal": strings.Join([]string{
			"XBIN_RES_EVENTS=res:apps/cal/events",
			"XBIN_RES_MY_CACHE=res:apps/cal/my-cache",
			"XBIN_RES_BUS=res:apps/cal/bus",
			"XBIN_RES_DB=<ws>/.xbin/resenc/apps~cal/db/db.sqlite",
			"XBIN_RES_FILES=<ws>/.xbin/resenc/apps~cal/files",
			"XBIN_RES_PICS=res:apps/cal/pics",
			"XBIN_RES_TICKS=res:apps/cal/ticks",
			"XBIN_RES_SHARED=res:workspace/shared",
		}, "\n"),
		"apps/cal/widget": "XBIN_RES_EVENTS=res:apps/cal/events",
		"apps/mail": strings.Join([]string{
			"XBIN_RES_EVENTS=res:apps/cal/events",
			"XBIN_IFACE_LLM_URL=http://xbin/api/apps/llm",
		}, "\n"),
		"apps/notes": strings.Join([]string{
			"XBIN_IFACE_LLM_URL=http://xbin/api/apps/pool/teams/team",
			"XBIN_IFACE_LLM_INSTANCE=team",
		}, "\n"),
		"apps/agent": `XBIN_IFACE_CHANNELS=[{"provider":"apps/llm","service":"openai","url":"http://xbin/api/apps/llm"},` +
			`{"instance":"team","provider":"apps/pool","service":"openai","url":"http://xbin/api/apps/pool/teams/team"}]`,
		"apps/traefik": "XBIN_INGRESS_FORWARD_URL=http://10.0.2.2:8642",
		"apps/client":  "XBIN_IFACE_DB_ADDR=10.0.2.2:20000",
		"apps/db":      "XBIN_IFACE_VPNLAN_IP=10.43.0.2",
		"apps/vpn":     `XBIN_LAN_INGRESS=[{"component":"apps/db","slot":"vpnlan","providerAddr":"10.43.0.1/30","clientAddr":"10.43.0.2/30"}]`,
		"apps/cms":     "",
		"apps/plain":   "",
	} {
		if got := env(comp); got != want {
			t.Errorf("%s: backend env changed\n got:\n%s\nwant:\n%s", comp, got, want)
		}
	}
}

// covers P5 P6 SC-ZERO Z6 PO-2 — main's storage names, as today, for every
// store the broker keys (08-data §2's "today" column): the path keys, kv
// buckets and their encryption labels, the resenc cipher and mount dirs and
// their password labels, the plaintext cron dir, the vault file and its
// unlabelled envelope, the cron, bus-subscription, backup-schedule, interface
// instance and ingress host stores, the archive and terminal-layer keys, the
// disk-quota keys, and the bus topic and its counter. Each is driven through
// the code path that uses it, so a store that moves is caught where it moves.
func TestZeroStateKeys(t *testing.T) {
	b := zeroDataBroker(t)
	root := b.Reg.Root
	ws := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	eq := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s changed\n got: %q\nwant: %q", what, got, want)
		}
	}

	// --- path keys: CompKey (tile) and ScopeKey (scope) -------------------
	for path, want := range map[string]string{
		"apps/cal":                         "apps~cal-c2360189",
		"apps/cal/widget":                  "apps~cal~widget-4289f1dd",
		"apps/mail":                        "apps~mail-9a57f032",
		"apps/a-very-long-tile-name/inner": "apps~a-very-long-tile-na-1acd73ae", // truncated at 24
		"apps/a-very-long-tile-name/other": "apps~a-very-long-tile-na-c035509f",
	} {
		eq("CompKey("+path+")", util.CompKey(path), want)
	}
	eq(`ScopeKey("")`, util.ScopeKey(""), "workspace")
	eq(`ScopeKey("apps/cal")`, util.ScopeKey("apps/cal"), "apps~cal")

	// --- kv: one shared bbolt file, a bucket per resource id --------------
	eq("kv file", b.kv.db.Path(), ws("data/kv.db"))
	for target, want := range map[string]string{
		"res:apps/cal/events":   "res:apps/cal/events",
		"res:apps/cal/my-cache": "res:apps/cal/my-cache",
		"res:workspace/shared":  "res:workspace/shared",
	} {
		rt, _, ok := b.parseRes(target)
		if !ok {
			t.Fatalf("fixture: %s does not resolve", target)
		}
		eq("kv bucket of "+target, rt.String(), want)
	}
	cal := auth.Principal{Component: "apps/cal", Via: "instance"}
	put := func(rest, val string) {
		t.Helper()
		if w := zeroDataCall(t, b.apiKVPut, "PUT", rest, val, cal); w.Code != 200 {
			t.Fatalf("kv put %s: %d %s", rest, w.Code, w.Body.String())
		}
	}
	put("res:apps/cal/events/k0", "v0")  // no barrier yet: stored plaintext, tagged 0x00
	put("res:workspace/shared/k0", "w0") // the workspace scope's bucket
	raw := func(bucket, key string) []byte {
		var out []byte
		_ = b.kv.db.View(func(tx *bolt.Tx) error {
			if bk := tx.Bucket([]byte(bucket)); bk != nil {
				out = append([]byte(nil), bk.Get([]byte(key))...)
			}
			return nil
		})
		return out
	}
	if got := raw("res:apps/cal/events", "k0"); !bytes.Equal(got, []byte("\x00v0")) {
		t.Errorf("a plaintext kv value is stored as %q, want tag 0x00 + value", got)
	}

	// The vault, before the barrier: a plaintext map at data/vault/<CompKey>.json.
	b.AllowInsecureVault = true
	if w := zeroDataCall(t, b.apiVaultPut, "PUT", "apps/cal/token", `{"value":"s3"}`, cal); w.Code != 200 {
		t.Fatalf("vault put: %d %s", w.Code, w.Body.String())
	}
	eq("vault path", b.vaultPath("apps/cal"), ws("data/vault/apps~cal-c2360189.json"))
	if got, _ := os.ReadFile(ws("data/vault/apps~cal-c2360189.json")); string(got) != "{\n  \"token\": \"s3\"\n}" {
		t.Errorf("plaintext vault file changed: %q", got)
	}

	// Bring the barrier up (the file resources get their password labels here).
	var fsLabels []string
	errRecorded := errors.New("label recorded")
	b.resenc = resenc.New(root, "/nonexistent/gocryptfs", func(label string) ([]byte, error) {
		fsLabels = append(fsLabels, label)
		return nil, errRecorded // stop before anything runs
	})
	if err := b.UnsealOrInit("zero-state-pass"); err != nil {
		t.Fatal(err)
	}
	sort.Strings(fsLabels)
	eq("resenc password labels", strings.Join(fsLabels, " "),
		"fs:apps~cal/db fs:apps~cal/files fs:apps~cal/pics fs:workspace/wsfiles")
	// …the vault was migrated in place: the old file's bytes, sealed with the
	// DEK and no label; a write re-seals the compact map the same way.
	openVault := func() string {
		t.Helper()
		var envl struct {
			Enc  int    `json:"enc"`
			Data []byte `json:"data"`
		}
		vb, _ := os.ReadFile(ws("data/vault/apps~cal-c2360189.json"))
		if err := json.Unmarshal(vb, &envl); err != nil || envl.Enc != 1 {
			t.Fatalf("vault envelope: %v %s", err, vb)
		}
		pt, err := b.barrier.Decrypt(envl.Data)
		if err != nil {
			t.Fatalf("the vault envelope must open with the unlabelled DEK: %v", err)
		}
		return string(pt)
	}
	eq("migrated vault", openVault(), "{\n  \"token\": \"s3\"\n}")
	if w := zeroDataCall(t, b.apiVaultPut, "PUT", "apps/cal/other", `{"value":"x"}`, cal); w.Code != 200 {
		t.Fatalf("vault put: %d %s", w.Code, w.Body.String())
	}
	eq("sealed vault", openVault(), `{"other":"x","token":"s3"}`)
	// …and a kv value written now is sealed under "kv:<bucket>".
	put("res:apps/cal/events/k1", "v1")
	enc := raw("res:apps/cal/events", "k1")
	if len(enc) == 0 || enc[0] != 0x01 {
		t.Fatalf("an encrypted kv value must carry tag 0x01: %q", enc)
	}
	if pt, err := b.barrier.DecryptFor("kv:res:apps/cal/events", enc[1:]); err != nil || string(pt) != "v1" {
		t.Errorf("kv label changed: DecryptFor(kv:res:apps/cal/events) = %q, %v", pt, err)
	}
	if _, err := b.barrier.DecryptFor("kv:res:apps/cal/my-cache", enc[1:]); err == nil {
		t.Error("a kv value opened under another bucket's label")
	}
	var buckets []string
	_ = b.kv.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			buckets = append(buckets, string(name))
			return nil
		})
	})
	eq("kv.db buckets", strings.Join(buckets, " "), "res:apps/cal/events res:workspace/shared")

	// --- file-backed resources: resenc dirs, the sqlite file, cron's dir ---
	eq("cipher dir", b.resenc.CipherDir(util.ScopeKey("apps/cal"), "db"), ws("data/resources-enc/apps~cal/db"))
	eq("mount dir", b.resenc.MountDir(util.ScopeKey("apps/cal"), "files"), ws(".xbin/resenc/apps~cal/files"))
	eq("workspace mount dir", b.resenc.MountDir(util.ScopeKey(""), "wsfiles"), ws(".xbin/resenc/workspace/wsfiles"))
	eq("sqlite path", b.fsResPath("apps/cal", "db", true), ws(".xbin/resenc/apps~cal/db/db.sqlite"))
	eq("fs label", resLabel(util.ScopeKey("apps/cal"), "db"), "apps~cal/db")
	eq("resources root", b.resourcesRoot("apps/cal"), ws("data/resources/apps~cal"))
	if fi, err := os.Stat(ws("data/resources/apps~cal")); err != nil || !fi.IsDir() {
		t.Errorf("a cron resource's scope dir data/resources/apps~cal is not provisioned: %v", err)
	}

	// --- registrations: cron, bus subscriptions, backup schedule ----------
	if w := zeroDataCall(t, b.apiCronPut, "PUT", "",
		`{"name":"tick","resource":"res:apps/cal/ticks","schedule":"@every 1h","path":"/tick"}`,
		auth.Principal{Component: "apps/cal"}); w.Code != 200 {
		t.Fatalf("cron put: %d %s", w.Code, w.Body.String())
	}
	eq("cron store", b.cron.storePath(), ws("data/cron-jobs.json"))
	if _, ok := b.cron.jobs["apps/cal\x00tick"]; !ok {
		t.Errorf("cron job key changed: %v", zeroDataKeys(b.cron.jobs))
	}
	cronFile, _ := os.ReadFile(ws("data/cron-jobs.json"))
	eq("data/cron-jobs.json", string(cronFile), `[
  {
    "name": "tick",
    "resource": "res:apps/cal/ticks",
    "schedule": "@every 1h",
    "component": "apps/cal",
    "path": "/tick",
    "role": "writer"
  }
]`)
	if w := zeroDataCall(t, b.apiBusSubsPut, "PUT", "",
		`{"name":"s1","resource":"res:apps/cal/bus","prefix":"cal.","path":"/on"}`,
		auth.Principal{Component: "apps/cal"}); w.Code != 200 {
		t.Fatalf("bus subscription put: %d %s", w.Code, w.Body.String())
	}
	eq("bus subscription store", b.bus.storePath(), ws("data/bus-subscriptions.json"))
	if _, ok := b.bus.subs["apps/cal\x00s1"]; !ok {
		t.Errorf("bus subscription key changed: %v", zeroDataKeys(b.bus.subs))
	}
	busFile, _ := os.ReadFile(ws("data/bus-subscriptions.json"))
	eq("data/bus-subscriptions.json", string(busFile), `[
  {
    "name": "s1",
    "resource": "res:apps/cal/bus",
    "prefix": "cal.",
    "component": "apps/cal",
    "path": "/on",
    "role": "writer"
  }
]`)
	if w := zeroDataCall(t, b.apiBackupScheduleSet, "POST", "",
		`{"component":"apps/cal","schedule":"@every 24h","retention":3}`, auth.Principal{Owner: true}); w.Code != 200 {
		t.Fatalf("backup schedule: %d %s", w.Code, w.Body.String())
	}
	eq("backup schedule store", b.cron.backupPath(), ws("data/backup-schedule.json"))
	schedFile, _ := os.ReadFile(ws("data/backup-schedule.json"))
	eq("data/backup-schedule.json", string(schedFile), `[
  {
    "component": "apps/cal",
    "schedule": "@every 24h",
    "retention": 3
  }
]`)

	// --- interface instances and ingress hosts: root xbin.json, by tile ---
	if w := zeroDataCall(t, b.apiIfaceInstancesSet, "PUT", "", `{"instances":{"ops":"/teams/ops"}}`,
		auth.Principal{Component: "apps/pool"}); w.Code != 200 {
		t.Fatalf("iface instances: %d %s", w.Code, w.Body.String())
	}
	if w := zeroDataCall(t, b.apiIngressHosts, "PUT", "", `{"hosts":["a.sites.example.com"]}`,
		auth.Principal{Component: "apps/cms"}); w.Code != 200 {
		t.Fatalf("ingress hosts: %d %s", w.Code, w.Body.String())
	}
	var rootManifest struct {
		IfaceInstances map[string]map[string]string `json:"ifaceInstances"`
		IngressHosts   map[string][]string          `json:"ingressHosts"`
	}
	rb, _ := os.ReadFile(ws("xbin.json"))
	if err := json.Unmarshal(rb, &rootManifest); err != nil {
		t.Fatal(err)
	}
	gotInst, _ := json.Marshal(rootManifest.IfaceInstances)
	eq("root xbin.json ifaceInstances", string(gotInst), `{"apps/pool":{"ops":"/teams/ops"}}`)
	gotHosts, _ := json.Marshal(rootManifest.IngressHosts)
	eq("root xbin.json ingressHosts", string(gotHosts), `{"apps/cms":["a.sites.example.com"]}`)

	// --- archive and terminal-layer keys, disk-quota keys ----------------
	eq("archive key", backupKey("apps/cal"), "apps~cal-c2360189")
	eq("terminal layer", b.termDir("apps/cal"), ws(".xbin/term/apps~cal-c2360189"))
	quota := zeroDataKeys(b.scopeDiskUsage())
	eq("disk-quota keys", strings.Join(quota, " "), "apps~cal workspace")

	// --- the bus: topic res:<scope>/<name>/<topic>, counted by resource id -
	ch, cancel := b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "bus" })
	defer cancel()
	if w := zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"res:apps/cal/bus","topic":"cal.new","data":{"n":1}}`,
		auth.Principal{Component: "apps/cal"}); w.Code != 200 {
		t.Fatalf("bus publish: %d %s", w.Code, w.Body.String())
	}
	select {
	case e := <-ch:
		got, _ := json.Marshal(e)
		eq("bus event", string(got), `{"type":"bus","topic":"res:apps/cal/bus/cal.new","data":{"n":1}}`)
	case <-time.After(5 * time.Second):
		t.Fatal("no bus event")
	}
	if n := b.busEventCount("res:apps/cal/bus"); n != 1 {
		t.Errorf("bus counter keyed by resource id: %d", n)
	}
}

// zeroDataKeys lists a map's keys, sorted.
func zeroDataKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- tile deployments: each deployment's data namespace ----

// nsPlane stands in for the deployments plane in the data-namespace tests:
// per tile with a record, its primary and what each deployment runs (a
// checkpoint's materialized tree, or "" for the work tree). A tile it has no
// record for answers as the zero state: main alone, the primary, on the
// work tree.
type nsPlane struct {
	mu   sync.Mutex
	recs map[string]nsRecord
}

type nsRecord struct {
	primary string
	code    map[string]string
}

func (f *nsPlane) record(tile string) (nsRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.recs[tile]
	return r, ok
}

// set gives tile a record (code nil removes it) and rescans, as the plane
// does when a record changes what the registry composes.
func (f *nsPlane) set(t *testing.T, b *Broker, tile, primary string, code map[string]string) {
	t.Helper()
	f.mu.Lock()
	if code == nil {
		delete(f.recs, tile)
	} else {
		f.recs[tile] = nsRecord{primary: primary, code: code}
	}
	f.mu.Unlock()
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
}

// install puts the stand-in's answers where boot puts the plane's.
func (f *nsPlane) install(b *Broker) {
	primary := func(tile string) string {
		if r, ok := f.record(tile); ok {
			return r.primary
		}
		return util.MainDeployment
	}
	has := func(tile, dep string) bool {
		if r, ok := f.record(tile); ok {
			_, ok := r.code[dep]
			return ok
		}
		return dep == util.MainDeployment
	}
	b.DeploymentAnswers = DeploymentAnswers{
		PrimaryOf: primary,
		DeploymentsOf: func(tile string) (string, []string) {
			r, ok := f.record(tile)
			if !ok {
				return util.MainDeployment, []string{util.MainDeployment}
			}
			names := []string{util.MainDeployment}
			for _, n := range slices.Sorted(maps.Keys(r.code)) {
				if n != util.MainDeployment {
					names = append(names, n)
				}
			}
			return r.primary, names
		},
		AddressedDeployment: func(p auth.Principal, tile string) (string, error) { // as Plane.Addressed
			pr := primary(tile)
			if p.Component != tile {
				return pr, nil
			}
			switch dep := p.Deployment; {
			case dep == "" && p.Via != "terminal":
				return util.MainDeployment, nil
			case dep == "":
				return pr, nil
			case !has(tile, dep):
				return "", util.NoDeployment(tile, dep)
			default:
				return dep, nil
			}
		},
	}
	b.DeploymentHooks = DeploymentHooks{
		DeploymentExists: has,
		DeploymentCodeRoot: func(c *registry.Component, dep string) (string, bool, error) { // as Plane.CodeRoot
			r, ok := f.record(c.Path)
			if !ok {
				if dep != "" && dep != util.MainDeployment {
					return "", false, util.NoDeployment(c.Path, dep)
				}
				return c.Dir, false, nil
			}
			root, ok := r.code[cmp.Or(dep, r.primary)]
			switch {
			case !ok:
				return "", false, util.NoDeployment(c.Path, dep)
			case root == "":
				return c.Dir, false, nil
			}
			return root, true, nil
		},
	}
	b.Reg.PinnedPrimary = func(rel string) (*registry.PinnedCode, bool) {
		r, ok := f.record(rel)
		if !ok || r.code[r.primary] == "" {
			return nil, false
		}
		pc, err := registry.ReadCheckpoint(r.code[r.primary])
		if err != nil {
			return &registry.PinnedCode{ManifestErr: err.Error()}, true
		}
		return pc, true
	}
}

// nsWrite writes files under root.
func nsWrite(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// nsCheckpoint is a materialized checkpoint tree: an xbin.json, and the
// scope.json given ("" for none).
func nsCheckpoint(t *testing.T, scope string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{"xbin.json": `{"runtime":"go"}`}
	if scope != "" {
		files["scope.json"] = scope
	}
	nsWrite(t, root, files)
	return root
}

// calScope declares every resource type in apps/calendar's scope; calDevScope
// is dev's code: the same, plus a filesystem, a kv and a blob of its own.
const (
	calScope = `{"resources":{"events":{"type":"kv"},"bus":{"type":"bus"},"files":{"type":"filesystem"},
		"db":{"type":"sqlite"},"pics":{"type":"blob"}}}`
	calDevScope = `{"resources":{"events":{"type":"kv"},"bus":{"type":"bus"},"files":{"type":"filesystem"},
		"db":{"type":"sqlite"},"pics":{"type":"blob"},
		"devonly":{"type":"filesystem"},"devkv":{"type":"kv"},"devpics":{"type":"blob"}}}`
)

// newNSBroker is the broker fixture (deploy_fixture_test.go) with every
// resource type in apps/calendar's scope, used by apps/calendar together with
// the resources only dev's code declares; apps/email holding writer on
// apps/calendar's kv and bus; a workspace-level filesystem apps/chat (a
// workspace-scope tile) uses; a third tile in apps/shop's scope that never
// gets a record (apps/shop/stats); a plain-directory scope (apps/suite,
// holding apps/suite/app); and the stand-in plane installed, with no record
// yet.
func newNSBroker(t *testing.T) (*Broker, *nsPlane) {
	t.Helper()
	b := deployBroker(t)
	uses := func(targets ...string) string {
		var u []string
		for _, target := range targets {
			u = append(u, fmt.Sprintf(`{"target":%q,"role":"writer"}`, target))
		}
		return "[" + strings.Join(u, ",") + "]"
	}
	nsWrite(t, b.Reg.Root, map[string]string{
		"apps/calendar/scope.json": calScope,
		"apps/calendar/xbin.json": `{"runtime":"go","expose":{"roles":{"reader":"r","writer":"w","admin":"a"}},"uses":` +
			uses("res:apps/calendar/events", "res:apps/calendar/bus", "res:apps/calendar/files", "res:apps/calendar/db",
				"res:apps/calendar/pics", "res:apps/calendar/devonly", "res:apps/calendar/devkv", "res:apps/calendar/devpics") + `}`,
		"apps/chat/xbin.json": `{"runtime":"go","interfaces":{"llm":{"kind":"http","service":"openai"}},"uses":` +
			uses("res:workspace/wsfiles") + `}`,
		"apps/shop/stats/xbin.json": `{"runtime":"go","uses":` + uses("res:apps/shop/orders") + `}`,
		"apps/suite/scope.json":     `{"resources":{"jobs":{"type":"kv"}}}`,
		"apps/suite/app/xbin.json":  `{"runtime":"go","uses":` + uses("res:apps/suite/jobs") + `}`,
	})
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Resources = map[string]registry.Resource{"wsfiles": {Type: "filesystem"}}
		ws.Grants = append(ws.Grants,
			registry.Grant{From: fxEmail, Target: "res:apps/calendar/bus", Role: "writer"},
			registry.Grant{From: fxEmail, Target: "res:apps/calendar/events", Role: "writer"},
			registry.Grant{From: fxChat, Target: "res:workspace/wsfiles", Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	f := &nsPlane{recs: map[string]nsRecord{}}
	f.install(b)
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	return b, f
}

// nsFakeVolumes stands gocryptfs in for b: a script that logs each run's
// arguments, makes gocryptfs.conf on -init, claims the single-tenant patch,
// and marks a "mount" by a file in the mount point, which volumeMounted
// reads for the test's length. It unseals b's vault (initializing it) and
// returns the log.
func nsFakeVolumes(t *testing.T, b *Broker) (log func() string) {
	t.Helper()
	dir := t.TempDir()
	logf := filepath.Join(dir, "runs.log")
	script := `#!/bin/sh
echo "$@" >> ` + logf + `
for a; do last=$a; done
case "$1" in
-hh) echo "  -xbin-single-tenant"; exit 0 ;;
-init) touch "$last/gocryptfs.conf" ;;
*) touch "$last/.fake-mounted" ;;
esac
exit 0
`
	bin := filepath.Join(dir, "gocryptfs")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b.resenc = resenc.New(b.Reg.Root, bin, func(label string) ([]byte, error) { return b.barrier.DeriveKey(label) })
	prev := volumeMounted
	volumeMounted = func(m *resenc.Manager, k resKeys) bool {
		_, err := os.Lstat(filepath.Join(m.MountDir(k.DirKey, k.Name), ".fake-mounted"))
		return err == nil
	}
	t.Cleanup(func() { volumeMounted = prev })
	if err := b.UnsealOrInit("namespace-pass"); err != nil {
		t.Fatal(err)
	}
	return func() string { out, _ := os.ReadFile(logf); return string(out) }
}

// nsKV calls a kv handler as p on rest ("res:<scope>/<name>/<key>").
func nsKV(t *testing.T, b *Broker, method string, p auth.Principal, rest, body string) (int, string) {
	t.Helper()
	h := map[string]func(http.ResponseWriter, *http.Request){"GET": b.apiKVGet, "PUT": b.apiKVPut, "DELETE": b.apiKVDelete}[method]
	w := zeroDataCall(t, h, method, rest, body, p)
	return w.Code, w.Body.String()
}

// nsEnv splits env lines into a map.
func nsEnv(env []string) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

// covers P6 P7 P17 P22 P23 SC-DATA PO-11 — EnvFor per deployment: main's env,
// with a record, is today's, and its remap nil (every path at itself);
// dev's view gets the same XBIN_RES_* value for every resource both declare
// (the canonical path, main's mount, or the canonical id) and the same
// XBIN_IFACE_*_URL, plus the variables of the resources only its own code
// declares, and no XBIN_DEPLOYMENT (spawnSetup adds it). dev's remap binds its
// own volumes at the canonical directories, each mounted on its first use and
// none in main's data; a workspace-level filesystem of a workspace-scope tile
// keeps its variable and binds nothing (block, P23); a volume that can't be
// mounted has no entry, so the runner fails the start closed.
func TestEnvForPerDeployment(t *testing.T) {
	b, f := newNSBroker(t)
	root := b.Reg.Root
	log := nsFakeVolumes(t, b)
	cal, _ := b.Reg.Component(fxCalendar)
	chat, _ := b.Reg.Component(fxChat)
	today := map[string][]string{fxCalendar: b.EnvFor(cal), fxChat: b.EnvFor(chat)}

	cpDev, cpChat := nsCheckpoint(t, calDevScope), nsCheckpoint(t, "")
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": cpDev})
	f.set(t, b, fxChat, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": cpChat})
	for tile, want := range today {
		c, _ := b.Reg.Component(tile)
		env, remap := b.DeploymentEnv(c, util.MainDeployment)
		if !slices.Equal(env, want) || remap != nil {
			t.Errorf("%s main: env %q remap %v, want today's %q and no remap", tile, env, remap, want)
		}
	}

	view := func(c *registry.Component, cp string) *registry.Component {
		t.Helper()
		v, err := b.Reg.View(c, registry.ViewCode{Deployment: "dev", Tree: "tree-" + filepath.Base(cp), Root: cp})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	env, remap := b.DeploymentEnv(view(cal, cpDev), "dev")
	mainEnv, devEnv := nsEnv(today[fxCalendar]), nsEnv(env)
	for k, v := range mainEnv {
		if devEnv[k] != v {
			t.Errorf("dev's %s = %q, main's %q: a resource both declare must carry the same value", k, devEnv[k], v)
		}
	}
	mount := func(name string) string { return filepath.Join(root, ".xbin", "resenc", "apps~calendar", name) }
	for k, v := range map[string]string{
		"XBIN_RES_DEVONLY": mount("devonly"), "XBIN_RES_DEVKV": "res:apps/calendar/devkv", "XBIN_RES_DEVPICS": "res:apps/calendar/devpics",
	} {
		if devEnv[k] != v || mainEnv[k] != "" {
			t.Errorf("%s: dev %q, main %q; want %q in dev only, which alone declares it", k, devEnv[k], mainEnv[k], v)
		}
	}
	if len(devEnv) != len(mainEnv)+3 {
		t.Errorf("dev's env %v: want main's and the three resources dev alone declares", devEnv)
	}
	if _, ok := devEnv["XBIN_DEPLOYMENT"]; ok {
		t.Error("the broker's env names the deployment: spawnSetup adds XBIN_DEPLOYMENT")
	}
	ns := func(name string) string {
		return filepath.Join(root, ".xbin", "resenc", ".deployments", "apps~calendar", "dev", "fs", name)
	}
	wantRemap := map[string]runner.ResBind{mount("files"): {Src: ns("files")}, mount("db"): {Src: ns("db")}, mount("devonly"): {Src: ns("devonly")}}
	if !maps.Equal(remap, wantRemap) {
		t.Errorf("dev's remap %v, want %v", remap, wantRemap)
	}
	for canon, rb := range remap {
		if strings.Contains(rb.Src, filepath.Join(".xbin", "resenc", "apps~calendar")) ||
			strings.Contains(rb.Src, filepath.Join("data", "resources-enc", "apps~calendar")) || rb.Src == canon {
			t.Errorf("dev binds %s from main's data (%s)", canon, rb.Src)
		}
	}
	for _, name := range []string{"files", "db", "devonly"} {
		cipher := filepath.Join(root, "data", "resources-enc", ".deployments", "apps~calendar", "dev", "fs", name)
		if !strings.Contains(log(), "-init -q -passfile /dev/stdin "+cipher+"\n") || !strings.Contains(log(), cipher+" "+ns(name)+"\n") {
			t.Errorf("dev's %s volume wasn't initialized and mounted on its first use:\n%s", name, log())
		}
	}

	chatEnv, chatRemap := b.DeploymentEnv(view(chat, cpChat), "dev")
	if !slices.Equal(chatEnv, today[fxChat]) {
		t.Errorf("apps/chat dev env %q, want main's %q: interface URLs and resource paths are the same", chatEnv, today[fxChat])
	}
	ws := filepath.Join(root, ".xbin", "resenc", "workspace", "wsfiles")
	if want := (map[string]runner.ResBind{ws: {Src: ws, Omit: true}}); !maps.Equal(chatRemap, want) {
		t.Errorf("apps/chat dev remap %v, want %v: a workspace-level filesystem is blocked, its variable kept", chatRemap, want)
	}

	b.resenc = resenc.New(root, "", nil) // encryption can't run: nothing mounts
	if _, remap := b.DeploymentEnv(view(cal, cpDev), "dev"); len(remap) != 0 {
		t.Errorf("remap %v while no volume can mount: want none, so the start fails closed", remap)
	}
}

// covers P6 P14 SC-DATA — kv per deployment: a put with dev's credential is
// invisible to main and the reverse; dev starts empty, and reading it creates
// nothing; a kv only dev's code declares exists for dev alone; a terminal
// session reaches the deployment it targets, or the primary it follows; a
// credential of a deployment that's gone answers 404.
func TestKVNamespacePerDeployment(t *testing.T) {
	b, f := newNSBroker(t)
	root := b.Reg.Root
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	nsFile := filepath.Join(root, "data", "resources-enc", ".deployments", "apps~calendar", "dev", "kv.db")
	want := func(what string, code int, body string, wantCode int, wantBody string) {
		t.Helper()
		if code != wantCode || wantBody != "" && body != wantBody {
			t.Errorf("%s: %d %q, want %d %q", what, code, body, wantCode, wantBody)
		}
	}

	c, body := nsKV(t, b, "PUT", calMain, "res:apps/calendar/events/k", "main")
	want("main put", c, body, 200, "")
	c, body = nsKV(t, b, "GET", calDev, "res:apps/calendar/events/k", "")
	want("dev get of main's key", c, body, 404, "")
	c, body = nsKV(t, b, "GET", calDev, "res:apps/calendar/events", "")
	want("dev list", c, body, 200, `{"keys":null}`+"\n")
	if _, err := os.Lstat(nsFile); err == nil {
		t.Fatal("reading dev's namespace created its kv file")
	}
	c, body = nsKV(t, b, "PUT", calDev, "res:apps/calendar/events/k", "dev")
	want("dev put", c, body, 200, "")
	if _, err := os.Lstat(nsFile); err != nil {
		t.Fatalf("dev's first write made no namespace kv file: %v", err)
	}
	c, body = nsKV(t, b, "GET", calMain, "res:apps/calendar/events/k", "")
	want("main get", c, body, 200, "main")
	c, body = nsKV(t, b, "GET", calDev, "res:apps/calendar/events/k", "")
	want("dev get", c, body, 200, "dev")
	var mainRaw []byte
	_ = b.kv.db.View(func(tx *bolt.Tx) error {
		mainRaw = append(mainRaw, tx.Bucket([]byte("res:apps/calendar/events")).Get([]byte("k"))...)
		return nil
	})
	if string(mainRaw) != "\x00main" {
		t.Errorf("data/kv.db holds %q for main's key: dev's write reached it", mainRaw)
	}

	c, body = nsKV(t, b, "PUT", calDev, "res:apps/calendar/devkv/x", "1")
	want("dev put to its own kv", c, body, 200, "")
	c, body = nsKV(t, b, "GET", calMain, "res:apps/calendar/devkv/x", "")
	want("main get of dev's kv", c, body, 404, `{"docs":"/docs/resources.md","error":"no such kv resource"}`+"\n")

	target := auth.Principal{Component: fxCalendar, Via: "terminal", Deployment: "dev"}
	follow := auth.Principal{Component: fxCalendar, Via: "terminal"}
	c, body = nsKV(t, b, "GET", target, "res:apps/calendar/events/k", "")
	want("a session targeting dev", c, body, 200, "dev")
	c, body = nsKV(t, b, "GET", follow, "res:apps/calendar/events/k", "")
	want("a session following the primary", c, body, 200, "main")

	c, body = nsKV(t, b, "DELETE", calDev, "res:apps/calendar/events/k", "")
	want("dev delete", c, body, 200, "")
	c, body = nsKV(t, b, "GET", calMain, "res:apps/calendar/events/k", "")
	want("main get after dev's delete", c, body, 200, "main")

	gone := auth.Principal{Component: fxCalendar, Via: "instance", Deployment: "gone"}
	c, body = nsKV(t, b, "GET", gone, "res:apps/calendar/events/k", "")
	want("a deployment that's gone", c, body, 404, "")
}

// covers P3 P6 P28 — flow G: apps/shop+dev and apps/shop/admin+dev share the
// scope's dev namespace; a sibling without dev (apps/shop/admin's main, and
// apps/shop/stats, which has no record) uses the primary namespace; the
// scope's declarations for dev are the scope root's dev code; apps/shop/admin+dev
// calling apps/shop reaches apps/shop's primary through an edge, never its
// dev data.
func TestMultiTileScopeNamespaces(t *testing.T) {
	b, f := newNSBroker(t)
	shopDevCode := nsCheckpoint(t, `{"resources":{"orders":{"type":"kv"},"events":{"type":"bus"},"drafts":{"type":"kv"}}}`)
	f.set(t, b, fxShop, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": shopDevCode})
	f.set(t, b, fxShopAdmin, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, "")})
	adminMain := auth.Principal{Component: fxShopAdmin, Via: "instance"}
	adminDev := auth.Principal{Component: fxShopAdmin, Via: "instance", Deployment: "dev"}
	stats := auth.Principal{Component: "apps/shop/stats", Via: "instance"}
	get := func(p auth.Principal, rest string) string {
		t.Helper()
		code, body := nsKV(t, b, "GET", p, rest, "")
		if code != 200 {
			return fmt.Sprint(code)
		}
		return body
	}

	if c, body := nsKV(t, b, "PUT", shopDev, "res:apps/shop/orders/o1", "dev"); c != 200 {
		t.Fatalf("shop dev put: %d %s", c, body)
	}
	if got := get(adminDev, "res:apps/shop/orders/o1"); got != "dev" {
		t.Errorf("apps/shop/admin+dev reads %q: siblings named dev share (apps/shop, dev)", got)
	}
	for name, p := range map[string]auth.Principal{"apps/shop/admin's main": adminMain, "apps/shop/stats": stats, "apps/shop's main": shopMain} {
		if got := get(p, "res:apps/shop/orders/o1"); got != "404" {
			t.Errorf("%s reads dev's key (%q): it serves the primary namespace", name, got)
		}
	}
	if c, body := nsKV(t, b, "PUT", stats, "res:apps/shop/orders/o2", "main"); c != 200 {
		t.Fatalf("stats put: %d %s", c, body)
	}
	if got, dev := get(shopMain, "res:apps/shop/orders/o2"), get(adminDev, "res:apps/shop/orders/o2"); got != "main" || dev != "404" {
		t.Errorf("a sibling without dev writes the primary namespace: apps/shop's main reads %q, apps/shop/admin+dev %q", got, dev)
	}
	for _, tile := range []string{fxShop, fxShopAdmin} {
		c, _ := b.Reg.Component(tile)
		if res, _ := b.declaredIn(c.Scope, "dev"); res["drafts"].Type != "kv" || len(res) != 3 {
			t.Errorf("%s: the scope's dev declarations %v, want the scope root's dev code", tile, res)
		}
	}
	shop, _ := b.Reg.Component(fxShop)
	if d := b.Route(adminDev, shop, ""); d.Deployment != util.MainDeployment || d.Deny == nil && !(d.Clamped && d.Role == "reader") {
		t.Errorf("apps/shop/admin+dev → apps/shop: %+v, want apps/shop's primary through an edge (clamped, or refused)", d)
	}
	f.set(t, b, fxShop, util.MainDeployment, map[string]string{util.MainDeployment: ""}) // the root drops dev
	if got := get(adminDev, "res:apps/shop/orders/o1"); got != "dev" {
		t.Errorf("apps/shop/admin+dev reads %q once the scope root has no dev: its namespace stays, declared by the root's primary", got)
	}
}

// covers P13 T4 = TestNonPrimaryBusPublishIsolated — an own-scope publish
// lands in the publisher's namespace: the event names dev, counted apart; a
// main publish carries no deployment; another scope's bus, and its kv, are
// read-only to a non-primary deployment whatever the tile holds, while the
// primary writes them as today.
func TestBusPublishStaysInNamespace(t *testing.T) {
	b, f := newNSBroker(t)
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	f.set(t, b, fxEmail, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, "")})
	emailMain := auth.Principal{Component: fxEmail, Via: "instance"}
	ch, cancel := b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "bus" })
	defer cancel()
	publish := func(p auth.Principal, topic string) *httptest.ResponseRecorder {
		t.Helper()
		return zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"res:apps/calendar/bus","topic":"`+topic+`","data":1}`, p)
	}
	next := func() events.Event {
		t.Helper()
		select {
		case e := <-ch:
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("no bus event")
		}
		return events.Event{}
	}

	if w := publish(calDev, "t1"); w.Code != 200 {
		t.Fatalf("dev publish: %d %s", w.Code, w.Body.String())
	}
	if e, _ := json.Marshal(next()); string(e) != `{"type":"bus","topic":"res:apps/calendar/bus/t1","deployment":"dev","data":1}` {
		t.Errorf("dev's bus event %s", e)
	}
	if w := publish(calMain, "t2"); w.Code != 200 {
		t.Fatalf("main publish: %d %s", w.Code, w.Body.String())
	}
	if e, _ := json.Marshal(next()); string(e) != `{"type":"bus","topic":"res:apps/calendar/bus/t2","data":1}` {
		t.Errorf("main's bus event %s", e)
	}
	if m, d := b.busEventCount("res:apps/calendar/bus"), b.busEventCount("res:apps/calendar/bus\x00dev"); m != 1 || d != 1 {
		t.Errorf("bus counters: main %d, dev %d; want one each, apart", m, d)
	}

	w := publish(emailDev, "t3")
	if w.Code != 403 || !strings.Contains(w.Body.String(), `apps/email+dev may not write res:apps/calendar/bus: non-primary deployments reach other scopes read-only`) {
		t.Errorf("apps/email+dev publishing on apps/calendar's bus: %d %s", w.Code, w.Body.String())
	}
	if w := publish(emailMain, "t4"); w.Code != 200 {
		t.Errorf("apps/email's primary publishing with its writer grant: %d %s", w.Code, w.Body.String())
	}
	if c, body := nsKV(t, b, "PUT", calMain, "res:apps/calendar/events/k", "main"); c != 200 {
		t.Fatalf("calendar put: %d %s", c, body)
	}
	if c, body := nsKV(t, b, "PUT", emailDev, "res:apps/calendar/events/k", "x"); c != 403 || !strings.Contains(body, "read-only") {
		t.Errorf("apps/email+dev writing apps/calendar's kv: %d %s", c, body)
	}
	if c, body := nsKV(t, b, "GET", emailDev, "res:apps/calendar/events/k", ""); c != 200 || body != "main" {
		t.Errorf("apps/email+dev reading apps/calendar's kv: %d %q, want the scope primary's value", c, body)
	}
	if c, body := nsKV(t, b, "PUT", emailMain, "res:apps/calendar/events/k", "email"); c != 200 {
		t.Errorf("apps/email's primary writing apps/calendar's kv with its writer grant: %d %s", c, body)
	}
}

// covers P13 T4 — delivery compares namespaces, not only topics (09-fabric
// §5.10): a WebSocket subscriber gets an event only from the namespace it
// reaches for the resource (its own deployment's in its own scope, the scope
// primary's elsewhere), a frame in an admin's browser included; a push
// subscription of main never receives dev's publish.
func TestBusNamespaceMatch(t *testing.T) {
	b, f := newNSBroker(t)
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	f.set(t, b, fxEmail, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, "")})
	mainEv := events.Event{Type: "bus", Topic: "res:apps/calendar/bus/t"}
	devEv := events.Event{Type: "bus", Topic: "res:apps/calendar/bus/t", Deployment: "dev"}
	for name, c := range map[string]struct {
		p         auth.Principal
		main, dev bool
	}{
		"calendar's main instance":        {calMain, true, false},
		"calendar's main frame":           {auth.Principal{Component: fxCalendar, Via: "frame"}, true, false},
		"calendar's frame, admin's tab":   {auth.Principal{Component: fxCalendar, Via: "frame", UserID: fxAdmin}, true, false},
		"calendar's dev instance":         {calDev, false, true},
		"calendar's dev frame":            {auth.Principal{Component: fxCalendar, Via: "frame", Deployment: "dev"}, false, true},
		"a session targeting dev":         {auth.Principal{Component: fxCalendar, Via: "terminal", Deployment: "dev"}, false, true},
		"a session following the primary": {auth.Principal{Component: fxCalendar, Via: "terminal"}, true, false},
		"apps/email's primary (reader)":   {auth.Principal{Component: fxEmail, Via: "instance"}, true, false},
		"apps/email+dev (reader)":         {emailDev, true, false},
		"apps/plain (no grant)":           {auth.Principal{Component: fxPlain, Via: "instance"}, false, false},
	} {
		if got := b.busFilter(c.p, mainEv); got != c.main {
			t.Errorf("%s gets main's event: %v, want %v", name, got, c.main)
		}
		if got := b.busFilter(c.p, devEv); got != c.dev {
			t.Errorf("%s gets dev's event: %v, want %v", name, got, c.dev)
		}
	}

	calls := fakeBusDispatch(b, nil)
	if w := zeroDataCall(t, b.apiBusSubsPut, "PUT", "", `{"name":"s1","resource":"res:apps/calendar/bus","prefix":"","path":"/on"}`, calMain); w.Code != 200 {
		t.Fatalf("subscribe: %d %s", w.Code, w.Body.String())
	}
	for _, p := range []auth.Principal{calDev, calMain} {
		if w := zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"res:apps/calendar/bus","topic":"from-`+cmp.Or(p.Deployment, "main")+`"}`, p); w.Code != 200 {
			t.Fatalf("publish: %d %s", w.Code, w.Body.String())
		}
	}
	select {
	case c := <-calls:
		if c.d.Topic != "from-main" {
			t.Errorf("main's push subscription received %q", c.d.Topic)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("main's push subscription received nothing")
	}
	select {
	case c := <-calls:
		t.Errorf("a second delivery: %q", c.d.Topic)
	case <-time.After(200 * time.Millisecond):
	}
}

// covers P22 T2 — TestProvisionFollowsCode's non-primary rows (the primary's
// are deploy_provision_test.go's): a dev checkpoint declaring a new kv gets
// it in dev's namespace only; a dev checkpoint whose scope.json names a
// resource outside the rule, or is a symlink leaving the checkpoint, declares
// nothing, so nothing of it is provisioned; the live reload target declares
// from the work tree, read beneath it; a plain-directory scope's scope.json
// serves every deployment; a resource dev's code no longer declares keeps its
// data.
func TestProvisionFollowsCodeNonPrimary(t *testing.T) {
	b, f := newNSBroker(t)
	root := b.Reg.Root
	cpDev := nsCheckpoint(t, calDevScope)
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": cpDev})
	if c, body := nsKV(t, b, "PUT", calDev, "res:apps/calendar/devkv/k", "kept"); c != 200 {
		t.Fatalf("dev put: %d %s", c, body)
	}
	if main, _ := b.declaredIn(fxCalendar, util.MainDeployment); len(main) != 5 {
		t.Errorf("main's declarations %v: dev's code gave the primary a resource", main)
	}

	bad := nsCheckpoint(t, `{"resources":{"events":{"type":"kv"},"../../x":{"type":"filesystem"}}}`)
	link := nsCheckpoint(t, "")
	if err := os.Symlink(filepath.Join(root, "apps", "calendar", "scope.json"), filepath.Join(link, "scope.json")); err != nil {
		t.Fatal(err)
	}
	for name, cp := range map[string]string{"a name outside the rule": bad, "a symlink": link} {
		f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": cp})
		if res, err := b.declaredIn(fxCalendar, "dev"); len(res) != 0 || err == nil {
			t.Errorf("%s in dev's checkpoint: declares %v (%v), want nothing and the reason", name, res, err)
		}
		if c, _ := nsKV(t, b, "GET", calDev, "res:apps/calendar/events/k", ""); c != 404 {
			t.Errorf("%s in dev's checkpoint: dev's kv answers %d, want 404", name, c)
		}
	}

	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calScope)})
	if c, _ := nsKV(t, b, "GET", calDev, "res:apps/calendar/devkv/k", ""); c != 404 {
		t.Errorf("devkv, no longer declared by dev's code, answers %d", c)
	}
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": cpDev})
	if c, body := nsKV(t, b, "GET", calDev, "res:apps/calendar/devkv/k", ""); c != 200 || body != "kept" {
		t.Errorf("devkv declared again: %d %q, want its data kept", c, body)
	}

	// dev follows the work tree while main is pinned: the work tree declares dev's
	cpMain := nsCheckpoint(t, calScope)
	nsWrite(t, root, map[string]string{"apps/calendar/scope.json": calDevScope})
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: cpMain, "dev": ""})
	if res, err := b.declaredIn(fxCalendar, "dev"); len(res) != 8 || err != nil {
		t.Errorf("the live reload target declares %v (%v), want the work tree's eight", res, err)
	}
	if res, _ := b.declaredIn(fxCalendar, util.MainDeployment); len(res) != 5 {
		t.Errorf("pinned main declares %v, want its checkpoint's five", res)
	}
	elsewhere := t.TempDir()
	nsWrite(t, elsewhere, map[string]string{"scope.json": calDevScope})
	if err := os.Remove(filepath.Join(root, "apps", "calendar", "scope.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "scope.json"), filepath.Join(root, "apps", "calendar", "scope.json")); err != nil {
		t.Fatal(err)
	}
	if res, err := b.declaredIn(fxCalendar, "dev"); len(res) != 0 || err == nil {
		t.Errorf("a work-tree scope.json linking out of the tile declares %v (%v) for the live reload target", res, err)
	}

	f.set(t, b, "apps/suite/app", util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, `{"resources":{"x":{"type":"kv"}}}`)})
	suiteDev := auth.Principal{Component: "apps/suite/app", Via: "instance", Deployment: "dev"}
	if res, _ := b.declaredIn("apps/suite", "dev"); len(res) != 1 || res["jobs"].Type != "kv" {
		t.Errorf("a plain-directory scope declares %v for dev, want its own scope.json's", res)
	}
	if c, body := nsKV(t, b, "PUT", suiteDev, "res:apps/suite/jobs/j", "1"); c != 200 {
		t.Errorf("dev of a tile in a plain-directory scope: %d %s", c, body)
	}
}

// covers T10 — a namespace beyond main declares at most 64 resources, the
// first in name order; the rest are skipped with an error naming them, and
// answer as undeclared. main keeps no cap (P5).
func TestDeclaredResourceCap(t *testing.T) {
	b, f := newNSBroker(t)
	var decl []string
	for i := range 70 {
		decl = append(decl, fmt.Sprintf(`"r%02d":{"type":"kv"}`, i))
	}
	scope := `{"resources":{` + strings.Join(decl, ",") + `}}`
	nsWrite(t, b.Reg.Root, map[string]string{"apps/calendar/scope.json": scope})
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, scope)})
	res, err := b.declaredIn(fxCalendar, "dev")
	if len(res) != 64 || res["r63"].Type != "kv" || res["r64"].Type != "" {
		t.Errorf("dev declares %d resources (r63 %v, r64 %v), want r00…r63", len(res), res["r63"], res["r64"])
	}
	if err == nil || !strings.Contains(err.Error(), "r64, r65, r66, r67, r68, r69 not provisioned") {
		t.Errorf("the cap's error: %v", err)
	}
	if main, err := b.declaredIn(fxCalendar, util.MainDeployment); len(main) != 70 || err != nil {
		t.Errorf("main declares %d (%v), want all 70", len(main), err)
	}
}

// covers P22 P9 — a save provisions only the namespace whose declarations
// the work tree owns, the live reload target's: with main pinned and dev
// following the work tree, a resource added in the work tree reaches dev's
// declarations and none of main's (no mount, no kv); while live reload is
// paused, a save reaches neither; with dev the pinned primary and main the
// live reload target, main's volumes come from the work tree and none from
// dev's code. A tile without a record provisions main from the work tree,
// as today.
func TestLiveTargetSaveProvisionsOnlyItsNamespace(t *testing.T) {
	b, f := newNSBroker(t)
	root := b.Reg.Root
	log := nsFakeVolumes(t, b)
	nsWrite(t, root, map[string]string{"apps/calendar/xbin.json": `{"runtime":"go","uses":[
		{"target":"res:apps/calendar/files","role":"writer"},{"target":"res:apps/calendar/newfs","role":"writer"},
		{"target":"res:apps/calendar/newkv","role":"writer"}]}`})
	save := func(scope string) {
		t.Helper()
		nsWrite(t, root, map[string]string{"apps/calendar/scope.json": scope})
		if err := b.Reg.Rescan(); err != nil {
			t.Fatal(err)
		}
		b.Provision()
	}
	newfs := filepath.Join(root, "data", "resources-enc", "apps~calendar", "newfs")
	withNew := strings.Replace(calScope, `"events"`, `"newfs":{"type":"filesystem"},"newkv":{"type":"kv"},"ticks":{"type":"cron"},"events"`, 1)
	cronDir := func() bool {
		_, err := os.Lstat(filepath.Join(root, "data", "resources", "apps~calendar"))
		return err == nil
	}
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: nsCheckpoint(t, calScope), "dev": ""})
	save(withNew)
	if strings.Contains(log(), newfs) || cronDir() {
		t.Errorf("a save while dev follows the work tree provisioned main's newfs or ticks (cron dir %v):\n%s", cronDir(), log())
	}
	mainRes, _ := b.declaredIn(fxCalendar, util.MainDeployment)
	devRes, _ := b.declaredIn(fxCalendar, "dev")
	if _, ok := mainRes["newfs"]; ok || devRes["newfs"].Type != "filesystem" || devRes["newkv"].Type != "kv" {
		t.Errorf("after the save: main declares %v, dev %v; want the new resources in dev's alone", mainRes, devRes)
	}
	if c, _ := nsKV(t, b, "PUT", calMain, "res:apps/calendar/newkv/k", "x"); c != 404 {
		t.Errorf("main's newkv: %d, want 404", c)
	}
	if c, body := nsKV(t, b, "PUT", calDev, "res:apps/calendar/newkv/k", "x"); c != 200 {
		t.Errorf("dev's newkv: %d %s", c, body)
	}

	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: nsCheckpoint(t, calScope), "dev": nsCheckpoint(t, withNew)})
	save(strings.Replace(withNew, `"events"`, `"newer":{"type":"kv"},"events"`, 1))
	mainRes, _ = b.declaredIn(fxCalendar, util.MainDeployment)
	devRes, _ = b.declaredIn(fxCalendar, "dev")
	if _, ok := mainRes["newer"]; ok {
		t.Error("a save while paused reached main")
	}
	if _, ok := devRes["newer"]; ok {
		t.Error("a save while paused reached dev")
	}

	// dev is the primary, pinned, and main follows the work tree: a save
	// provisions main's namespace from the work tree, never from dev's code
	f.set(t, b, fxCalendar, "dev", map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, `{"resources":{"devfs":{"type":"filesystem"}}}`)})
	save(withNew)
	devfs := filepath.Join(root, "data", "resources-enc", "apps~calendar", "devfs")
	if !strings.Contains(log(), newfs) || strings.Contains(log(), devfs) || !cronDir() {
		t.Errorf("main as the live reload target beside a pinned primary dev: want main provisioned from the work tree (cron dir %v):\n%s", cronDir(), log())
	}

	f.set(t, b, fxCalendar, "", nil) // no record: today's
	if err := os.RemoveAll(newfs); err != nil {
		t.Fatal(err)
	}
	before := len(log())
	save(withNew)
	if !strings.Contains(log()[before:], "-init -q -passfile /dev/stdin "+newfs+"\n") {
		t.Errorf("without a record a save provisions main from the work tree:\n%s", log())
	}
}

// covers P22 P28 — 08-data §6.7's table, row by row: the workspace scope
// declares the root xbin.json's resources for every deployment; a scope root
// without a record, its work tree; with deployment d, d's code (the work
// tree while it is the live reload target, its checkpoint otherwise); a
// deployment only a sibling has, the root's primary's code; a
// plain-directory scope, its scope.json for every deployment.
func TestMultiTileScopeDeclarationSource(t *testing.T) {
	b, f := newNSBroker(t)
	names := func(scope, dep string) string {
		t.Helper()
		res, err := b.declaredIn(scope, dep)
		if err != nil {
			t.Errorf("%s/%s: %v", scope, dep, err)
		}
		return strings.Join(slices.Sorted(maps.Keys(res)), " ")
	}
	eq := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s: %q, want %q", what, got, want)
		}
	}
	eq("the workspace scope, dev", names("", "dev"), "wsfiles")
	eq("a root without a record, main", names(fxShop, util.MainDeployment), "events orders")
	f.set(t, b, fxShop, util.MainDeployment, map[string]string{util.MainDeployment: "",
		"dev": nsCheckpoint(t, `{"resources":{"orders":{"type":"kv"},"drafts":{"type":"kv"}}}`)})
	eq("the root's pinned dev", names(fxShop, "dev"), "drafts orders")
	eq("the root's primary on the work tree", names(fxShop, util.MainDeployment), "events orders")
	f.set(t, b, fxShop, util.MainDeployment, map[string]string{
		util.MainDeployment: nsCheckpoint(t, `{"resources":{"orders":{"type":"kv"}}}`), "dev": ""})
	eq("the root's dev on the work tree", names(fxShop, "dev"), "events orders")
	eq("the root's pinned primary", names(fxShop, util.MainDeployment), "orders")
	f.set(t, b, fxShopAdmin, util.MainDeployment, map[string]string{util.MainDeployment: "", "qa": nsCheckpoint(t, "")})
	eq("a deployment only a sibling has", names(fxShop, "qa"), "orders")
	f.set(t, b, "apps/suite/app", util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, "")})
	eq("a plain-directory scope, main", names("apps/suite", util.MainDeployment), "jobs")
	eq("a plain-directory scope, dev", names("apps/suite", "dev"), "jobs")
}

// covers P18 P22 SC-FAIL-CLOSED — the volumes of a namespace beyond main:
// resenc takes exactly their directory key and no other below the
// deployments level; the spawn hold mounts dev's on its first use and holds
// dev when they can't mount, or while the vault is sealed, and the primary's
// hold is today's; a blob request mounts on first use, and one nothing ever
// wrote reads as empty and creates nothing; a cap:containers change remounts
// dev's mounted filesystem volume in single-tenant mode; sealing stops a
// tile whose dev alone uses a file-backed resource.
func TestNamespaceVolumes(t *testing.T) {
	derive := func(string) ([]byte, error) { return make([]byte, 32), nil }
	fake := t.TempDir()
	bin := filepath.Join(fake, "gocryptfs")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := resenc.New(filepath.Join(fake, "ws"), bin, derive)
	if _, err := m.Ensure(".deployments/apps~cal/dev/fs/files", ".deployments/apps~cal/dev/fs", "files", false); err != nil {
		t.Errorf("resenc refuses a namespace's directory key: %v", err)
	}
	for _, key := range []string{".deployments/apps~cal/dev", ".deployments/apps~cal/dev/other", ".deployments/.x/dev/fs",
		".deployments/apps~cal/.d/fs", ".deployments", ".hidden", ".deployments/a/b/c/fs", ".deployments/../dev/fs"} {
		if _, err := m.Ensure(key+"/x", key, "x", false); err == nil {
			t.Errorf("resenc takes directory key %q", key)
		}
	}
	if got := m.Mounts(); len(got) != 1 || got[0] != (resenc.Mount{ScopeKey: ".deployments/apps~cal/dev/fs", Name: "files"}) {
		t.Errorf("Mounts() = %v", got)
	}

	b, f := newNSBroker(t)
	root := b.Reg.Root
	log := nsFakeVolumes(t, b)
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	ns := func(name string) string {
		return filepath.Join(root, "data", "resources-enc", ".deployments", "apps~calendar", "dev", "fs", name)
	}
	if b.DeploymentEncryptionHold(fxCalendar, "dev") {
		t.Error("dev is held with its volumes mountable")
	}
	for _, name := range []string{"files", "db", "pics", "devonly", "devpics"} {
		if !strings.Contains(log(), "-init -q -passfile /dev/stdin "+ns(name)+"\n") {
			t.Errorf("the spawn hold didn't mount dev's %s on its first use", name)
		}
	}
	if b.EncryptionHold(fxCalendar) {
		t.Error("the primary is held with main's volumes mounted")
	}

	// blob: dev's own blob, and a namespace volume nothing wrote to
	blob := func(method, rest, body string, p auth.Principal) (int, string) {
		h := map[string]func(http.ResponseWriter, *http.Request){"GET": b.apiBlobGet, "PUT": b.apiBlobPut, "DELETE": b.apiBlobDelete}[method]
		w := zeroDataCall(t, h, method, rest, body, p)
		return w.Code, w.Body.String()
	}
	mnt := filepath.Join(root, ".xbin", "resenc", ".deployments", "apps~calendar", "dev", "fs")
	for _, dir := range []string{ns("devpics"), filepath.Join(mnt, "devpics")} { // as if never used
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	if c, body := blob("GET", "res:apps/calendar/devpics", "", calDev); c != 200 || body != `{"entries":null}`+"\n" {
		t.Errorf("listing a volume nothing wrote to: %d %s", c, body)
	}
	if c, _ := blob("GET", "res:apps/calendar/devpics/a.png", "", calDev); c != 404 {
		t.Errorf("reading a volume nothing wrote to: %d", c)
	}
	if _, err := os.Lstat(ns("devpics")); err == nil {
		t.Error("reading a volume nothing wrote to created it")
	}
	if c, body := blob("PUT", "res:apps/calendar/devpics/a.png", "png", calDev); c != 200 {
		t.Errorf("dev's first blob write: %d %s", c, body)
	}
	if c, body := blob("GET", "res:apps/calendar/devpics/a.png", "", calDev); c != 200 || body != "png" {
		t.Errorf("dev's blob read: %d %q", c, body)
	}
	if c, _ := blob("GET", "res:apps/calendar/devpics/a.png", "", calMain); c != 404 {
		t.Errorf("main reads dev's blob: %d", c)
	}

	// cap:containers: dev's mounted filesystem volumes follow the grant
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: fxCalendar, Target: ContainersCap, Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	b.OnGrantChange = func(string) {}
	b.grantRestart(registry.Grant{From: fxCalendar, Target: ContainersCap, Role: "writer"})
	if !strings.Contains(log(), "-xbin-single-tenant "+ns("files")+" "+filepath.Join(mnt, "files")+"\n") ||
		strings.Contains(log(), "-xbin-single-tenant "+ns("db")) {
		t.Errorf("cap:containers didn't remount dev's filesystem volume, alone, single-tenant:\n%s", log())
	}

	// seal: a tile whose dev alone uses a file-backed resource stops
	var stopped []string
	b.StopBackend = func(tile string) { stopped = append(stopped, tile) }
	nsWrite(t, root, map[string]string{"apps/shop/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/shop/shots","role":"writer"}]}`})
	f.set(t, b, fxShop, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, `{"resources":{"shots":{"type":"blob"}}}`)})
	b.SealResources()
	if !slices.Contains(stopped, fxShop) || !slices.Contains(stopped, fxCalendar) || slices.Contains(stopped, fxEmail) {
		t.Errorf("seal stopped %v, want apps/calendar and apps/shop (its dev's blob)", stopped)
	}
	if len(b.resenc.Mounts()) != 0 {
		t.Errorf("seal left %v mounted", b.resenc.Mounts())
	}

	// sealed: dev is held (its kv can't decode, its volumes can't mount)
	b.barrier.Seal()
	if !b.DeploymentEncryptionHold(fxCalendar, "dev") {
		t.Error("dev isn't held while the vault is sealed")
	}
}

// covers P6 P18 SC-DATA 08-data §3.5 — with a real gocryptfs over FUSE
// (XBIN_GOCRYPTFS; skipped without it): dev's volume mounts on its first use
// under .xbin/resenc/.deployments/, what dev writes there lands encrypted in
// its own namespace's ciphertext, never in main's volume or in plaintext, and
// sealing unmounts it.
func TestNamespaceVolumeMounts(t *testing.T) {
	bin := os.Getenv("XBIN_GOCRYPTFS")
	if fi, err := os.Stat(bin); bin == "" || err != nil || fi.Mode()&0o111 == 0 {
		t.Skip("XBIN_GOCRYPTFS names no gocryptfs binary: skipping the FUSE test")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse absent: skipping the FUSE test")
	}
	b, f := newNSBroker(t)
	root := b.Reg.Root
	b.resenc = resenc.New(root, bin, func(label string) ([]byte, error) { return b.barrier.DeriveKey(label) })
	t.Cleanup(func() { b.resenc.UnmountAll() })
	if err := b.UnsealOrInit("namespace-pass"); err != nil {
		t.Fatal(err)
	}
	cpDev := nsCheckpoint(t, calDevScope)
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": cpDev})
	cal, _ := b.Reg.Component(fxCalendar)
	v, err := b.Reg.View(cal, registry.ViewCode{Deployment: "dev", Tree: "tree-dev", Root: cpDev})
	if err != nil {
		t.Fatal(err)
	}
	_, remap := b.DeploymentEnv(v, "dev")
	canon := filepath.Join(root, ".xbin", "resenc", "apps~calendar", "files")
	src := remap[canon].Src
	if want := filepath.Join(root, ".xbin", "resenc", ".deployments", "apps~calendar", "dev", "fs", "files"); src != want {
		t.Skipf("dev's volume didn't mount (%q, want %q): no FUSE mount allowed here?", src, want)
	}
	if !b.resenc.Mounted(".deployments/apps~calendar/dev/fs", "files") {
		t.Fatal("dev's volume is in the remap but not mounted")
	}
	if err := os.WriteFile(filepath.Join(src, "secret.txt"), []byte("dev data"), 0o600); err != nil {
		t.Fatal(err)
	}
	cipher := filepath.Join(root, "data", "resources-enc", ".deployments", "apps~calendar", "dev", "fs", "files")
	entries, err := os.ReadDir(cipher)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Contains(names, "gocryptfs.conf") || slices.Contains(names, "secret.txt") || len(names) < 3 {
		t.Errorf("dev's ciphertext dir holds %v: want gocryptfs.conf and an encrypted name", names)
	}
	if _, err := os.Lstat(filepath.Join(canon, "secret.txt")); err == nil {
		t.Error("dev's write reached main's volume")
	}
	b.SealResources()
	if b.resenc.Mounted(".deployments/apps~calendar/dev/fs", "files") {
		t.Error("sealing left dev's volume mounted")
	}
	if _, err := os.Lstat(filepath.Join(src, "secret.txt")); err == nil {
		t.Error("dev's plaintext is on disk after the seal")
	}
}
