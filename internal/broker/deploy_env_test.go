package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
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
