package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// zsBxMain marks a re-exec of this test binary as bx itself: TestMain then
// runs main() with the arguments it was given, so a table row runs exactly
// what a shell would (flag parsing, dispatch, exit codes).
const zsBxMain = "XBIN_TEST_RUN_BX_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(zsBxMain) == "1" {
		os.Args[0] = "bx"
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// zsXbind is a stand-in xbind recording every request bx sends. It answers
// the shapes bx decodes (lists as lists), 404 to a follow stream (so an
// agent command ends), and {} to everything else.
type zsXbind struct {
	mu   sync.Mutex
	reqs []string
}

func (x *zsXbind) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	line := r.Method + " " + r.URL.RequestURI()
	if len(body) > 0 {
		line += " " + string(body)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer zs-token" {
		line += " [Authorization: " + got + "]"
	}
	if ct := r.Header.Get("Content-Type"); (len(body) > 0) != (ct == "application/json") {
		line += " [Content-Type: " + ct + "]"
	}
	x.mu.Lock()
	x.reqs = append(x.reqs, line)
	x.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Query().Get("follow") == "1" {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"no such session"}`)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/xbin/components":
		fmt.Fprint(w, `[{"path":"apps/x","runtime":"node","hasIndex":true},{"path":"notes+ideas","hasIndex":true}]`)
	case "GET /api/xbin/builtins", "GET /api/xbin/templates", "GET /api/xbin/builtins/updates",
		"GET /api/xbin/term/sessions", "GET /api/xbin/agent/history":
		fmt.Fprint(w, `[]`)
	case "GET /api/xbin/orgs":
		fmt.Fprint(w, `{"orgs":[{"id":"acme","name":"Acme"}]}`)
	case "GET /api/xbin/owner/preview":
		fmt.Fprint(w, `{"allowed":true}`)
	case "POST /api/xbin/term/sessions":
		fmt.Fprint(w, `{"id":"s9","provider":"claude","mode":"plan","cwd":"apps/x"}`)
	default:
		fmt.Fprint(w, `{}`)
	}
}

func (x *zsXbind) take() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := x.reqs
	x.reqs = nil
	return out
}

// covers PO-13 P5 SC-ZERO — every bx invocation docs/bx.md documents today
// sends the same requests: a table of invocations, each run as bx against
// a recording xbind, compared with the requests (method, URI, body) and the
// exit code it produces today. A tile whose own path holds + stays one
// tile: bx never splits it, the server resolves the ref. bx logs reads the
// tile's log file (no request). Hand-maintained goldens: changing a row is
// a compat change (12-compat.md), never a regeneration.
func TestBxTodayInvocationsUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("runs bx once per documented invocation")
	}
	x := &zsXbind{}
	srv := httptest.NewServer(x)
	defer srv.Close()
	ws := t.TempDir()
	for rel, body := range map[string]string{
		"xbin.json": `{}`,
		".xbin/log/" + util.CompKey("notes+ideas") + ".log": "--- gen 1 start ---\nnotes+ideas listening\n",
		"apps/x/xbin.json":  `{"runtime":"node"}`,
		"0001-fix.patch":    "From 0000000000000000000000000000000000000000 Mon Sep 17 00:00:00 2001\nSubject: [PATCH] fix\n\n---\n",
		"apps/x/index.html": `<!doctype html><html><head></head><body><img src="/c/apps/x/a.png"></body></html>`,
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// run executes bx args in a clean environment: the recording xbind, a
	// token, the workspace; comp is $XBIN_COMPONENT ("" = a host shell).
	run := func(comp string, args ...string) (string, string) {
		cmd := exec.Command(exe, args...)
		cmd.Dir = ws
		cmd.Env = []string{zsBxMain + "=1", "PATH=" + os.Getenv("PATH"), "HOME=" + ws,
			"XBIN_URL=" + srv.URL, "XBIN_TOKEN=zs-token", "XBIN_WORKSPACE=" + ws}
		if comp != "" {
			cmd.Env = append(cmd.Env, "XBIN_COMPONENT="+comp)
		}
		var stdout bytes.Buffer
		cmd.Stdout, cmd.Stdin = &stdout, strings.NewReader("")
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("bx %s: %v", strings.Join(args, " "), err)
		}
		out := strings.Join(append(x.take(), fmt.Sprintf("exit %d", code)), "\n")
		return out, stdout.String()
	}

	rows := []struct {
		comp string   // $XBIN_COMPONENT ("" = a host shell)
		args []string // what the shell passes
		want []string // the requests in order, then the exit code
	}{
		{"", []string{"ls"}, []string{
			"GET /api/xbin/components",
			"exit 0",
		}},
		{"apps/x", []string{"status"}, []string{
			"GET /api/xbin/tile-status?component=apps%2Fx",
			"exit 0",
		}},
		{"", []string{"status"}, []string{
			"GET /api/xbin/backends",
			"GET /api/xbin/status",
			"exit 0",
		}},
		{"", []string{"status", "apps/x"}, []string{
			"GET /api/xbin/tile-status?component=apps%2Fx",
			"exit 0",
		}},
		{"", []string{"status", "notes+ideas"}, []string{
			"GET /api/xbin/tile-status?component=notes%2Bideas",
			"exit 0",
		}},
		{"apps/x", []string{"status", "--all"}, []string{
			"GET /api/xbin/backends",
			"GET /api/xbin/status",
			"exit 0",
		}},
		{"", []string{"new", "apps/y", "--runtime", "go", "--expose", "--title", "Pretty Y", "--owner", "user:ana"}, []string{
			`POST /api/xbin/create {"expose":true,"owner":"user:ana","path":"apps/y","runtime":"go","title":"Pretty Y"}`,
			"exit 0",
		}},
		{"", []string{"tile", "ls"}, []string{
			"GET /api/xbin/builtins",
			"exit 0",
		}},
		{"", []string{"tile", "import", "welcome", "as", "apps/w"}, []string{
			`POST /api/xbin/builtins/import {"name":"welcome","path":"apps/w"}`,
			"exit 0",
		}},
		{"", []string{"template", "ls"}, []string{
			"GET /api/xbin/templates",
			"exit 0",
		}},
		{"", []string{"template", "new", "templates/agent", "as", "apps/agent1"}, []string{
			`POST /api/xbin/templates/new {"path":"apps/agent1","source":"templates/agent"}`,
			"exit 0",
		}},
		{"", []string{"template", "updates"}, []string{
			"GET /api/xbin/templates/updates",
			"exit 0",
		}},
		{"", []string{"builtin", "updates"}, []string{
			"GET /api/xbin/builtins/updates",
			"exit 0",
		}},
		{"", []string{"builtin", "update", "scaffold", "--merge"}, []string{
			`POST /api/xbin/builtins/update {"id":"scaffold","mode":"merge"}`,
			"exit 0",
		}},
		{"", []string{"user", "ls"}, []string{
			"GET /api/xbin/users",
			"GET /api/xbin/orgs",
			"exit 0",
		}},
		{"", []string{"user", "add", "ana", "--email", "ana@example.com", "--invite", "--tiles", "apps/x=write,notes+ideas=read"}, []string{
			`POST /api/xbin/users {"email":"ana@example.com","id":"ana","tiles":{"apps/x":"write","notes+ideas":"read"}}`,
			"exit 0",
		}},
		{"", []string{"user", "set", "ana", "--admin"}, []string{
			`PATCH /api/xbin/users/ana {"id":"ana","role":"admin"}`,
			"exit 0",
		}},
		{"", []string{"user", "invite", "ana"}, []string{
			"POST /api/xbin/users/ana/invite",
			"exit 0",
		}},
		{"", []string{"user", "signout", "ana", "--devices"}, []string{
			"DELETE /api/xbin/users/ana/sessions?devices=1",
			"exit 0",
		}},
		{"", []string{"user", "rm", "ana"}, []string{
			"DELETE /api/xbin/users/ana",
			"exit 0",
		}},
		{"", []string{"defaults"}, []string{
			"GET /api/xbin/defaults",
			"exit 0",
		}},
		{"", []string{"defaults", "set", "--tile-creation", "org-only", "--org", "acme:write"}, []string{
			"GET /api/xbin/defaults",
			`PUT /api/xbin/defaults {"newUsers":{"canCreate":null,"netSets":null,"noPersonalTiles":false,"noTerminal":false,"orgs":[{"level":"write","org":"acme"}],"sets":null,"termApi":false,"termNet":false,"tiles":null},"tileCreation":"org-only"}`,
			"exit 0",
		}},
		{"", []string{"org", "ls"}, []string{
			"GET /api/xbin/orgs",
			"exit 0",
		}},
		{"", []string{"org", "add", "acme"}, []string{
			`POST /api/xbin/orgs {"id":"acme"}`,
			"exit 0",
		}},
		{"", []string{"org", "set", "acme", "--sets", "+dev", "--net", "+corp"}, []string{
			"GET /api/xbin/orgs",
			`PATCH /api/xbin/orgs/acme {"netSets":["corp"],"sets":["dev"]}`,
			"exit 0",
		}},
		{"", []string{"org", "member", "acme", "ana", "--level", "write", "--create"}, []string{
			`PUT /api/xbin/orgs/acme/members/ana {"create":true,"level":"write"}`,
			"exit 0",
		}},
		{"", []string{"org", "sso-groups", "acme", "--add", "eng:write"}, []string{
			"GET /api/xbin/orgs",
			`PUT /api/xbin/orgs/acme/sso-groups {"rules":[{"group":"eng","level":"write"}]}`,
			"exit 0",
		}},
		{"", []string{"org", "policy"}, []string{
			"GET /api/xbin/policy",
			"exit 0",
		}},
		{"", []string{"org", "policy", "acme", "--set", `[{"tiles":["apps/*"],"deny":["net:*"]}]`}, []string{
			`PUT /api/xbin/orgs/acme/policy {"policy":[{"deny":["net:*"],"tiles":["apps/*"]}]}`,
			"exit 0",
		}},
		{"", []string{"org", "rm", "acme"}, []string{
			"DELETE /api/xbin/orgs/acme",
			"exit 0",
		}},
		{"", []string{"netset", "ls"}, []string{
			"GET /api/xbin/net-sets",
			"exit 0",
		}},
		{"", []string{"netset", "set", "corp", "--rules", "internet,lan:10.0.0.0/8"}, []string{
			`PUT /api/xbin/net-sets/corp {"rules":["internet","lan:10.0.0.0/8"]}`,
			"exit 0",
		}},
		{"", []string{"netset", "rm", "corp"}, []string{
			"DELETE /api/xbin/net-sets/corp",
			"exit 0",
		}},
		{"", []string{"owner", "apps/x"}, []string{
			"GET /api/xbin/owner?tile=apps/x",
			"exit 0",
		}},
		{"", []string{"owner", "notes+ideas", "--preview", "org:acme"}, []string{
			"GET /api/xbin/owner/preview?tile=notes%2Bideas&to=org%3Aacme",
			"exit 0",
		}},
		{"", []string{"owner", "notes+ideas", "--transfer", "user:ana", "--yes"}, []string{
			"GET /api/xbin/owner/preview?tile=notes%2Bideas&to=user%3Aana",
			`POST /api/xbin/owner {"tile":"notes+ideas","to":"user:ana"}`,
			"exit 0",
		}},
		{"", []string{"chrome"}, []string{
			"GET /api/xbin/chrome",
			"exit 0",
		}},
		{"", []string{"chrome", "approve", "apps/x"}, []string{
			`PUT /api/xbin/chrome {"approved":true,"path":"apps/x"}`,
			"exit 0",
		}},
		{"", []string{"chrome", "revoke", "apps/x"}, []string{
			`PUT /api/xbin/chrome {"approved":false,"path":"apps/x"}`,
			"exit 0",
		}},
		{"", []string{"permset", "ls"}, []string{
			"GET /api/xbin/permission-sets",
			"exit 0",
		}},
		{"", []string{"permset", "set", "dev", "--allow", "net:internet,cap:open-links", "--term-net"}, []string{
			`PUT /api/xbin/permission-sets/dev {"allow":["net:internet","cap:open-links"],"termNet":true}`,
			"exit 0",
		}},
		{"", []string{"permset", "rm", "dev"}, []string{
			"DELETE /api/xbin/permission-sets/dev",
			"exit 0",
		}},
		{"", []string{"access", "apps/x"}, []string{
			"GET /api/xbin/access?tile=apps/x",
			"GET /api/xbin/access-requests",
			"exit 0",
		}},
		{"", []string{"access", "notes+ideas", "set", "user:ana=write"}, []string{
			`PUT /api/xbin/access {"id":"ana","kind":"user","level":"write","tile":"notes+ideas"}`,
			"exit 0",
		}},
		{"", []string{"access", "apps/x", "rm", "user:ana"}, []string{
			`PUT /api/xbin/access {"id":"ana","kind":"user","level":"","tile":"apps/x"}`,
			"exit 0",
		}},
		{"", []string{"access", "apps/x", "request", "write"}, []string{
			`POST /api/xbin/access-requests {"level":"write","tile":"apps/x"}`,
			"exit 0",
		}},
		{"", []string{"access", "apps/x", "approve", "ana", "read"}, []string{
			`POST /api/xbin/access-requests/approve {"level":"read","tile":"apps/x","user":"ana"}`,
			"exit 0",
		}},
		{"apps/x", []string{"code", "prs"}, []string{
			"GET /api/xbin/code/prs?target=apps/x&state=open",
			"exit 0",
		}},
		{"", []string{"code", "prs", "notes+ideas", "--all"}, []string{
			"GET /api/xbin/code/prs?target=notes+ideas",
			"exit 0",
		}},
		{"apps/x", []string{"code", "prs", "--from", "--state=merged"}, []string{
			"GET /api/xbin/code/prs?from=1&state=merged",
			"exit 0",
		}},
		{"apps/x", []string{"code", "pr", "apps/b", "--title", "fix overflow", "-m", "why", "0001-fix.patch"}, []string{
			`POST /api/xbin/code/prs {"base":"","message":"why","series":"From 0000000000000000000000000000000000000000 Mon Sep 17 00:00:00 2001\nSubject: [PATCH] fix\n\n---\n","target":"apps/b","title":"fix overflow"}`,
			"exit 0",
		}},
		{"apps/x", []string{"code", "pr", "show", "3"}, []string{
			"GET /api/xbin/code/pr?target=apps/x&n=3",
			"exit 0",
		}},
		{"apps/x", []string{"code", "pr", "fetch", "3"}, []string{
			"GET /api/xbin/code/pr/series?target=apps/x&n=3",
			"exit 0",
		}},
		{"apps/x", []string{"code", "pr", "comment", "3", "-m", "looks good"}, []string{
			`POST /api/xbin/code/pr/comment {"body":"looks good","n":3,"target":"apps/x"}`,
			"exit 0",
		}},
		{"apps/x", []string{"code", "pr", "close", "3", "--merged", "-m", "applied"}, []string{
			`POST /api/xbin/code/pr/state {"comment":"applied","n":3,"state":"merged","target":"apps/x"}`,
			"exit 0",
		}},
		{"", []string{"api", "apps/x"}, []string{
			"GET /api/xbin/components/apps/x",
			"exit 0",
		}},
		{"", []string{"api", "notes+ideas"}, []string{
			"GET /api/xbin/components/notes+ideas",
			"exit 0",
		}},
		{"", []string{"grants"}, []string{
			"GET /api/xbin/grants",
			"exit 0",
		}},
		{"", []string{"grant", "apps/a", "apps/b:reader"}, []string{
			`POST /api/xbin/grants {"from":"apps/a","role":"reader","target":"apps/b"}`,
			"exit 0",
		}},
		{"", []string{"grant", "apps/email", "res:apps/calendar/bus:reader"}, []string{
			`POST /api/xbin/grants {"from":"apps/email","role":"reader","target":"res:apps/calendar/bus"}`,
			"exit 0",
		}},
		{"", []string{"grant", "--revoke", "apps/a", "apps/b:reader"}, []string{
			`DELETE /api/xbin/grants {"from":"apps/a","role":"reader","target":"apps/b"}`,
			"exit 0",
		}},
		{"", []string{"iface"}, []string{
			"GET /api/xbin/bindings",
			"exit 0",
		}},
		{"", []string{"bind", "apps/x", "net=internet"}, []string{
			`POST /api/xbin/bindings {"component":"apps/x","provider":"internet","slot":"net"}`,
			"exit 0",
		}},
		{"", []string{"bind", "notes+ideas", "llm+=apps/p#1"}, []string{
			"GET /api/xbin/bindings",
			`POST /api/xbin/bindings {"component":"notes+ideas","providers":["apps/p#1"],"slot":"llm"}`,
			"exit 0",
		}},
		{"", []string{"bind", "apps/x", "llm-=apps/p#1"}, []string{
			"GET /api/xbin/bindings",
			`DELETE /api/xbin/bindings {"component":"apps/x","slot":"llm"}`,
			"exit 0",
		}},
		{"", []string{"bind", "--unset", "apps/x", "net"}, []string{
			`DELETE /api/xbin/bindings {"component":"apps/x","slot":"net"}`,
			"exit 0",
		}},
		{"", []string{"expose", "apps/x", "web=backend", "--host", "shop.example.com"}, []string{
			`POST /api/xbin/bindings {"component":"apps/x","host":"shop.example.com","provider":"backend","slot":"web"}`,
			"exit 0",
		}},
		{"", []string{"unexpose", "apps/x", "web"}, []string{
			`DELETE /api/xbin/bindings {"component":"apps/x","slot":"web"}`,
			"exit 0",
		}},
		{"", []string{"ingress"}, []string{
			"GET /api/xbin/ingress",
			"exit 0",
		}},
		{"", []string{"ingress", "routes"}, []string{
			"GET /api/xbin/ingress",
			"exit 0",
		}},
		{"", []string{"vault", "status"}, []string{
			"GET /api/xbin/vault-status",
			"exit 0",
		}},
		{"", []string{"vault", "seal"}, []string{
			"POST /api/xbin/vault-seal {}",
			"exit 0",
		}},
		{"", []string{"vault", "ls", "notes+ideas"}, []string{
			"GET /api/xbin/vault/notes+ideas",
			"exit 0",
		}},
		{"", []string{"vault", "set", "apps/x", "API_KEY", "s3cret"}, []string{
			`PUT /api/xbin/vault/apps/x/API_KEY {"value":"s3cret"}`,
			"exit 0",
		}},
		{"", []string{"vault", "rm", "apps/x", "API_KEY"}, []string{
			"DELETE /api/xbin/vault/apps/x/API_KEY",
			"exit 0",
		}},
		{"apps/x", []string{"agent", "ls"}, []string{
			"GET /api/xbin/term/sessions?cwd=",
			"exit 0",
		}},
		{"", []string{"agent", "ls", "--tile", "notes+ideas"}, []string{
			"GET /api/xbin/term/sessions?cwd=notes+ideas",
			"exit 0",
		}},
		{"", []string{"agent", "history", "--tile", "apps/x"}, []string{
			"GET /api/xbin/agent/history?cwd=apps/x",
			"exit 0",
		}},
		{"", []string{"agent", "run", "--tile", "apps/x", "--provider", "claude", "--mode", "plan", "hello there"}, []string{
			`POST /api/xbin/term/sessions {"cwd":"apps/x","kind":"agent","mode":"plan","name":"","net":"","provider":"claude","vm":false}`,
			`POST /api/xbin/term/sessions/s9/prompt {"text":"hello there"}`,
			"GET /api/xbin/term/sessions/s9/events?since=0&follow=1",
			"exit 1",
		}},
		{"", []string{"agent", "send", "s1", "and", "then"}, []string{
			"GET /api/xbin/term/sessions/s1/events?since=0",
			`POST /api/xbin/term/sessions/s1/prompt {"text":"and then"}`,
			"GET /api/xbin/term/sessions/s1/events?since=0&follow=1",
			"exit 1",
		}},
		{"", []string{"agent", "attach", "s1", "--since", "4"}, []string{
			"GET /api/xbin/term/sessions/s1/events?since=4&follow=1",
			"exit 1",
		}},
		{"", []string{"agent", "permit", "s1", "p1", "once"}, []string{
			`POST /api/xbin/term/sessions/s1/permissions/p1 {"decision":"allow_once"}`,
			"exit 0",
		}},
		{"", []string{"agent", "set", "s1", "model", "sonnet"}, []string{
			`POST /api/xbin/term/sessions/s1/options {"id":"model","value":"sonnet"}`,
			"exit 0",
		}},
		{"", []string{"agent", "stop", "s1"}, []string{
			"DELETE /api/xbin/term/sessions/s1",
			"exit 0",
		}},
		{"", []string{"cron", "ls"}, []string{
			"GET /api/xbin/cron/jobs",
			"exit 0",
		}},
		{"", []string{"enable", "apps/x"}, []string{
			`POST /api/xbin/lifecycle {"component":"apps/x","state":"enabled"}`,
			"exit 0",
		}},
		{"", []string{"disable", "notes+ideas"}, []string{
			`POST /api/xbin/lifecycle {"component":"notes+ideas","state":"disabled"}`,
			"exit 0",
		}},
		{"", []string{"hide", "apps/x"}, []string{
			`POST /api/xbin/lifecycle {"component":"apps/x","state":"hidden"}`,
			"exit 0",
		}},
		{"", []string{"unhide", "apps/x"}, []string{
			`POST /api/xbin/lifecycle {"component":"apps/x","state":"enabled"}`,
			"exit 0",
		}},
		{"", []string{"offload", "apps/x", "--full"}, []string{
			`POST /api/xbin/lifecycle {"component":"apps/x","state":"offloaded-full"}`,
			"exit 0",
		}},
		{"", []string{"backup", "notes+ideas"}, []string{
			`POST /api/xbin/backup {"component":"notes+ideas"}`,
			"exit 0",
		}},
		{"", []string{"backups", "apps/x"}, []string{
			"GET /api/xbin/backups?component=apps/x",
			"exit 0",
		}},
		{"", []string{"restore", "apps/x", "--version", "v1"}, []string{
			`POST /api/xbin/restore {"component":"apps/x","file":"","version":"v1"}`,
			"exit 0",
		}},
		{"", []string{"restore", "apps/x", "--file", "data/a.txt"}, []string{
			`POST /api/xbin/restore {"component":"apps/x","file":"data/a.txt","version":""}`,
			"exit 0",
		}},
		{"", []string{"backup-schedule"}, []string{
			"GET /api/xbin/backup-schedule",
			"exit 0",
		}},
		{"", []string{"backup-schedule", "apps/x", "--every", "24h", "--keep", "3"}, []string{
			`POST /api/xbin/backup-schedule {"component":"apps/x","retention":3,"schedule":"@every 24h"}`,
			"exit 0",
		}},
		{"", []string{"backup-schedule", "apps/x", "--cron", "0 3 * * *"}, []string{
			`POST /api/xbin/backup-schedule {"component":"apps/x","retention":0,"schedule":"0 3 * * *"}`,
			"exit 0",
		}},
		{"", []string{"backup-schedule", "apps/x", "--rm"}, []string{
			"DELETE /api/xbin/backup-schedule?component=apps/x",
			"exit 0",
		}},
		{"", []string{"fix", "assets", "apps/x"}, []string{
			"exit 0",
		}},
	}
	for _, r := range rows {
		got, _ := run(r.comp, r.args...)
		if want := strings.Join(r.want, "\n"); got != want {
			t.Errorf("bx %s (XBIN_COMPONENT=%q):\n%s\nwant\n%s", strings.Join(r.args, " "), r.comp, got, want)
		}
	}

	// bx logs reads the named tile's log file directly — a + in the name
	// is part of the name.
	got, out := run("", "logs", "notes+ideas")
	if got != "exit 0" || out != "--- gen 1 start ---\nnotes+ideas listening\n" {
		t.Errorf("bx logs notes+ideas: %s, printed %q", got, out)
	}
	if got, _ := run("apps/x", "logs", "apps/x"); got != "exit 1" {
		t.Errorf("bx logs of a tile with no log: %s, want exit 1", got)
	}
}

// --- the tile-deployments commands (WP-26) ---

const (
	dlCP  = "c:3f2a1c9" // what main runs while pinned
	dlCP2 = "c:7b19e02" // a fresh checkpoint of the work tree
	dlCP0 = "c:1e9d0aa" // an earlier checkpoint in main's deploy log
)

// dlFake is a stand-in xbind for the tile-deployments commands. Answers are
// keyed "METHOD /path" (plus " dry" for a dry run); each key holds a queue
// whose last answer repeats. Every request is recorded as "METHOD URI BODY".
type dlFake struct {
	mu   sync.Mutex
	reqs []string
	ans  map[string][]dlAns
}

type dlAns struct {
	status int
	body   string
}

func newDLFake() *dlFake { return &dlFake{ans: map[string][]dlAns{}} }

func (f *dlFake) on(key string, status int, body string) *dlFake {
	f.ans[key] = append(f.ans[key], dlAns{status, body})
	return f
}

func (f *dlFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	var m map[string]any
	if json.Unmarshal(b, &m) == nil && m["dryRun"] == true {
		key += " dry"
	}
	line := r.Method + " " + r.URL.RequestURI()
	if len(b) > 0 {
		line += " " + string(b)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, line)
	a := dlAns{http.StatusNotFound, `{"error":"no fixture for ` + key + `"}`}
	if q := f.ans[key]; len(q) > 0 {
		a = q[0]
		if len(q) > 1 {
			f.ans[key] = q[1:]
		}
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.status)
	io.WriteString(w, a.body)
}

func (f *dlFake) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.reqs
	f.reqs = nil
	return out
}

// Route keys of the fake.
const (
	dlGet    = "GET /api/xbin/deployments"
	dlLog    = "GET /api/xbin/deployments/log"
	dlPost   = "POST /api/xbin/deployments/"
	dlGetURI = "GET /api/xbin/deployments?tile=apps%2Fx"
)

// dlSt is a State (11-contract §1.1) as a fixture builds it.
type dlSt map[string]any

func (s dlSt) json() string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// with returns a copy of s with the given fields set.
func (s dlSt) with(kv ...any) dlSt {
	c := dlSt{}
	for k, v := range s {
		c[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		c[kv[i].(string)] = kv[i+1]
	}
	return c
}

var (
	dlOK       = map[string]any{"ok": true}
	dlNoRecord = map[string]any{"ok": false, "why": "apps/x has no deployments yet: pause live reload or add a deployment first", "kind": "state"}
)

func dlCallerCan(can map[string]any) map[string]any {
	return map[string]any{"level": "terminal", "manager": false, "humanSession": false, "readOnly": false, "can": can}
}

// dlDep is a deployment row: pinned to cp, or following the work tree.
func dlDep(name, cp string, primary bool, can map[string]any) map[string]any {
	d := map[string]any{"name": name, "primary": primary, "liveReload": cp == "", "checkpoint": nil,
		"status": map[string]any{"state": "healthy", "gen": 3}, "url": "/c/apps/x/", "api": "/api/apps/x/", "can": can}
	if cp != "" {
		d["checkpoint"] = map[string]any{"id": cp, "hash": "…", "feed": "work-tree"}
	}
	return d
}

func dlAllCan() map[string]any {
	return map[string]any{"deploy": dlOK, "rollback": dlOK, "attach": dlOK, "restart": dlOK}
}

// dlZero is a tile without a record: live reload on main, which follows the
// work tree; only pausing is open.
func dlZero() dlSt {
	return dlSt{"tile": "apps/x", "record": false, "schema": 0, "seq": 0, "features": []string{"live-reload/1"},
		"owner": "", "view": "full", "primary": "main", "liveReload": "main", "lastLiveReload": "main",
		"protectedPrimary": false,
		"deployments":      []any{dlDep("main", "", true, map[string]any{"deploy": dlNoRecord, "rollback": dlNoRecord, "attach": dlNoRecord})},
		"caller":           dlCallerCan(map[string]any{"pause": dlOK, "resume": dlNoRecord, "reloadNow": dlNoRecord})}
}

// dlPaused: live reload paused by ana 12 minutes ago, main pinned to cp,
// three files changed since.
func dlPaused(cp string) dlSt {
	return dlZero().with("record", true, "schema", 1, "seq", 4, "liveReload", "",
		"liveReloadSince", map[string]any{"at": time.Now().Add(-12 * time.Minute).UTC().Format(time.RFC3339), "by": "user:ana"},
		"workTree", map[string]any{"changed": 3, "since": cp},
		"deployments", []any{dlDep("main", cp, true, dlAllCan())},
		"caller", dlCallerCan(map[string]any{"pause": dlOK, "resume": dlOK, "reloadNow": dlOK}))
}

// dlAttached: a record, live reload on main, which follows the work tree.
func dlAttached() dlSt {
	return dlPaused(dlCP).with("seq", 7, "liveReload", "main", "workTree", nil,
		"deployments", []any{dlDep("main", "", true, dlAllCan())})
}

func dlAnswer(st dlSt, kv ...any) string {
	m := map[string]any{"state": st}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func dlEntry(id int, how, cp, prev, result, phase, errText string) map[string]any {
	e := map[string]any{"id": id, "deployment": "main", "how": how, "checkpoint": cp, "previous": prev,
		"by": "user:ana", "via": "session", "result": result}
	if phase != "" {
		e["phase"] = phase
	}
	if errText != "" {
		e["error"] = errText
	}
	return e
}

func dlLogAnswer(e map[string]any) string {
	b, _ := json.Marshal(map[string]any{"entry": e})
	return string(b)
}

// dlImpact is a dry run's report: code from → to on main (to "" = no capture).
func dlImpact(from, to string, pauses bool, affects string) map[string]any {
	imp := map[string]any{"code": nil, "data": "none", "pausesLiveReload": pauses, "stops": []string{},
		"affects": affects, "reloads": []string{}}
	if to != "" {
		imp["code"] = map[string]any{"deployment": "main", "from": from, "to": to, "files": 3, "added": 40, "removed": 12}
	}
	if affects == "everyone" {
		imp["reloads"] = []string{"main"}
	}
	return imp
}

// dlCmds are the commands' functions, without dcCommand's os.Exit.
var dlCmds = map[string]func([]string) error{"live-reload": cmdLiveReload, "deploy": cmdDeploy, "rollback": cmdRollback}

type dlResult struct {
	code     int
	out, err string
}

// dlRun runs one tile-deployments command in-process against the xbind at
// url: as a terminal answering stdin (tty), or not.
func dlRun(t *testing.T, url string, tty bool, stdin string, args ...string) dlResult {
	t.Helper()
	t.Setenv("XBIN_URL", url)
	t.Setenv("XBIN_TOKEN", "dl-token")
	var out, errb bytes.Buffer
	oOut, oErr, oIn, oTTY, oPoll := dcOut, dcErr, dcIn, dcIsTerminal, dcPoll
	defer func() { dcOut, dcErr, dcIn, dcIsTerminal, dcPoll = oOut, oErr, oIn, oTTY, oPoll }()
	dcOut, dcErr, dcIn = &out, &errb, strings.NewReader(stdin)
	dcIsTerminal = func() bool { return tty }
	dcPoll = time.Millisecond
	code := dcRun(dlCmds[args[0]], args[1:])
	return dlResult{code, out.String(), errb.String()}
}

// dlExec runs bx as a shell would (the re-exec of TestMain), in dir with env.
func dlExec(t *testing.T, dir string, env []string, args ...string) (string, string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{zsBxMain + "=1", "PATH=" + os.Getenv("PATH"), "HOME=" + dir, "XBIN_WORKSPACE=" + dir}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, strings.NewReader("")
	err = cmd.Run()
	var ee *exec.ExitError
	code := 0
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("bx %s: %v", strings.Join(args, " "), err)
	}
	return out.String(), errb.String(), code
}

// covers SC-AGENT-BX PO-13 — the argument grammar of 11-contract §9.1, in the
// compat rule 6 superset: flags anywhere among the positionals, --flag value
// and --flag=value, each boolean's --no- pair with the last one winning, an
// unknown flag or a dangling value flag as a usage error, and positionals
// right-aligned, the tile falling back to $XBIN_COMPONENT.
func TestParseDeploymentArgs(t *testing.T) {
	flags := []string{"--to", "--checkpoint", "--yes", "--dry-run", "--json", "--wait"}
	for _, r := range []struct {
		args []string
		want dcArgs
	}{
		{[]string{"--to", "main"}, dcArgs{to: "main"}},
		{[]string{"apps/x", "--to=main", "--yes"}, dcArgs{pos: []string{"apps/x"}, to: "main", yes: true}},
		{[]string{"--yes", "apps/x", "--to", "main", "--no-yes"}, dcArgs{pos: []string{"apps/x"}, to: "main"}},
		{[]string{"--no-wait", "--to", "main", "--dry-run", "--json"}, dcArgs{to: "main", noWait: true, dryRun: true, json: true}},
		{[]string{"--no-wait", "--wait", "--no-dry-run", "--dry-run", "--json", "--no-json"}, dcArgs{dryRun: true}},
		{[]string{"--checkpoint", "c:3f2a1c9", "apps/x+dev"}, dcArgs{pos: []string{"apps/x+dev"}, checkpoint: "c:3f2a1c9"}},
		{[]string{"--checkpoint=c:3f2a1c9aabbccdd00112233445566778899aabbccddeeff00112233445566ff"}, dcArgs{checkpoint: "c:3f2a1c9aabbccdd00112233445566778899aabbccddeeff00112233445566ff"}},
		{[]string{"--", "--odd"}, dcArgs{pos: []string{"--odd"}}},
	} {
		got, err := parseDeploymentArgs("deploy", r.args, flags...)
		if err != nil || fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", r.want) {
			t.Errorf("%q: got %+v, %v; want %+v", r.args, got, err, r.want)
		}
	}
	for _, r := range []struct {
		cmd  string
		args []string
		want string
	}{
		{"deploy", []string{"--bogus"}, "unknown flag --bogus"},
		{"deploy", []string{"--to"}, "--to needs a value"},
		{"deploy", []string{"--to", "Main"}, `--to "Main": deployment names are lowercase letters`},
		{"deploy", []string{"--to", "apps/x+dev"}, `--to "apps/x+dev": deployment names`},
		{"deploy", []string{"--checkpoint", "3f2a1c9"}, `--checkpoint "3f2a1c9": a checkpoint id is c:`},
		{"deploy", []string{"--checkpoint=c:3f2a"}, `--checkpoint "c:3f2a"`},
		{"deploy", []string{"--yes=1"}, "--yes takes no value"},
		{"deploy", []string{"--no-to", "main"}, "unknown flag --no-to"},
		{"live-reload", []string{"--yes"}, "unknown flag --yes"},
	} {
		fl := flags
		if r.cmd == "live-reload" {
			fl = liveReloadFlags[""]
		}
		_, err := parseDeploymentArgs(r.cmd, r.args, fl...)
		var e *dcError
		if !errors.As(err, &e) || e.code != exitUsage || !strings.HasPrefix(e.msg, r.want) {
			t.Errorf("%s %q: got %v, want a usage error %q", r.cmd, r.args, err, r.want)
		}
	}

	// Positionals are right-aligned: the tile is the first only at full arity.
	split := func(arity int, pos ...string) string {
		tile, rest, err := dcArgs{pos: pos}.split("deploy", arity)
		if err != nil {
			var e *dcError
			if !errors.As(err, &e) || e.code != exitUsage {
				t.Fatalf("split %q: not a usage error: %v", pos, err)
			}
			return "usage"
		}
		return tile + " " + strings.Join(rest, ",")
	}
	t.Setenv("XBIN_COMPONENT", "apps/here")
	for _, r := range []struct {
		arity int
		pos   []string
		want  string
	}{
		{1, nil, "apps/here "},
		{1, []string{"apps/x/"}, "apps/x "},
		{1, []string{"notes+ideas"}, "notes+ideas "},
		{1, []string{"apps/x", "more"}, "usage"},
		{2, []string{"dev"}, "apps/here dev"},
		{2, []string{"apps/x", "dev"}, "apps/x dev"},
		{2, []string{"apps/x"}, "usage"}, // a / or + makes it a tile ref: the name is missing
		{2, []string{"apps+dev"}, "usage"},
	} {
		if got := split(r.arity, r.pos...); got != r.want {
			t.Errorf("arity %d %q: got %q, want %q", r.arity, r.pos, got, r.want)
		}
	}
	t.Setenv("XBIN_COMPONENT", "")
	if got := split(1); got != "usage" {
		t.Errorf("no tile and no $XBIN_COMPONENT: got %q", got)
	}

	// The subcommand is the first positional, wherever flags put it.
	for _, r := range []struct {
		args []string
		sub  string
		rest string
	}{
		{[]string{"pause", "apps/x"}, "pause", "apps/x"},
		{[]string{"--to", "now", "resume"}, "resume", "--to now"},
		{[]string{"--json", "attach", "--to=dev"}, "attach", "--json --to=dev"},
		{[]string{"apps/x", "pause"}, "", "apps/x pause"},
		{[]string{"--json"}, "", "--json"},
	} {
		sub, rest := liveReloadSub(r.args)
		if sub != r.sub || strings.Join(rest, " ") != r.rest {
			t.Errorf("%q: sub %q rest %q", r.args, sub, rest)
		}
	}
}

// covers SC-AGENT-BX 12-compat — the exit codes of 11-contract §9.1: 0 done,
// 1 failed (a deploy that ran and failed, a Can of kind state, any other
// refusal, a network error), 2 usage, 3 refused (a Can of kind authority or
// policy, or HTTP 403), 4 not confirmed (declined, or a guarded command with
// no terminal and no --yes), 5 still running when bx stopped waiting, 6 no
// tile deployments. Existing commands keep exiting 1 on every error, and
// the codes reach the shell through main.
func TestBxExitCodes(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "")
	paused := dlPaused(dlCP)
	deployDry := dlAnswer(paused, "impact", dlImpact(dlCP, dlCP2, false, "everyone"))
	for _, r := range []struct {
		name  string
		fake  func(f *dlFake)
		tty   bool
		stdin string
		args  []string
		code  int
		reqs  []string // the requests' method and path, in order (nil: unchecked)
		say   string   // in stderr
	}{
		{"pause, waited to ok", func(f *dlFake) {
			f.on(dlGet, 200, dlZero().json()).
				on(dlPost+"live-reload/pause dry", 200, dlAnswer(dlZero(), "impact", dlImpact("", "", true, "nobody"))).
				on(dlPost+"live-reload/pause", 200, dlAnswer(dlPaused(dlCP2), "deploy", dlEntry(1, "pause", dlCP2, "", "running", "materialize", ""))).
				on(dlLog, 200, dlLogAnswer(dlEntry(1, "pause", dlCP2, "", "running", "swap", ""))).
				on(dlLog, 200, dlLogAnswer(dlEntry(1, "pause", dlCP2, "", "ok", "", "")))
		}, false, "", []string{"live-reload", "pause", "apps/x"}, 0,
			[]string{dlGet, dlPost + "live-reload/pause", dlPost + "live-reload/pause", dlLog, dlLog}, "pause 1 main c:7b19e02 … materialize … swap … ok"},
		{"a deploy that ran and failed", func(f *dlFake) {
			f.on(dlGet, 200, paused.json()).on(dlPost+"deploy dry", 200, deployDry).
				on(dlPost+"deploy", 200, dlAnswer(paused, "deploy", dlEntry(5, "deploy", dlCP2, dlCP, "queued", "", ""))).
				on(dlLog, 200, dlLogAnswer(dlEntry(5, "deploy", dlCP2, dlCP, "failed", "build", "go build: exit status 1")))
		}, false, "", []string{"deploy", "apps/x", "--to", "main", "--yes"}, 1, nil,
			"Deploy to main failed — main keeps running c:3f2a1c9\n  go build: exit status 1"},
		{"a Can of kind state", func(f *dlFake) { f.on(dlGet, 200, dlZero().json()) },
			false, "", []string{"live-reload", "now", "apps/x", "--yes"}, 1, []string{dlGet}, "bx: apps/x has no deployments yet"},
		{"a 409", func(f *dlFake) {
			f.on(dlGet, 200, paused.json()).
				on(dlPost+"live-reload/now dry", 409, `{"error":"the deployments of apps/x changed (seq 5); reload and retry","docs":"/docs/protocol.md"}`)
		}, false, "", []string{"live-reload", "now", "apps/x", "--yes"}, 1, nil, "bx: the deployments of apps/x changed (seq 5); reload and retry\n"},
		{"usage: no --to", nil, false, "", []string{"deploy", "apps/x"}, 2, []string{}, "--to names the deployment"},
		{"usage: too many", nil, false, "", []string{"live-reload", "pause", "apps/x", "apps/y"}, 2, []string{}, "too many arguments"},
		{"a Can of kind authority", func(f *dlFake) {
			f.on(dlGet, 200, dlZero().with("caller", dlCallerCan(map[string]any{"pause": map[string]any{"ok": false, "why": "pausing live reload needs terminal-level access on apps/x", "kind": "authority"}})).json())
		}, false, "", []string{"live-reload", "pause", "apps/x"}, 3, []string{dlGet}, "bx: pausing live reload needs terminal-level access on apps/x\n"},
		{"a Can of kind policy", func(f *dlFake) {
			f.on(dlGet, 200, dlZero().with("caller", dlCallerCan(map[string]any{"pause": map[string]any{"ok": false, "why": "pinning a backend to a checkpoint needs isolation (--isolate)", "kind": "policy"}})).json())
		}, false, "", []string{"live-reload", "pause", "apps/x", "--yes"}, 3, []string{dlGet}, "needs isolation (--isolate)"},
		{"HTTP 403", func(f *dlFake) {
			f.on(dlGet, 200, paused.json()).
				on(dlPost+"deploy dry", 403, `{"error":"the primary of apps/x (main) is protected: only tile managers change its code, and not from a terminal or agent session","docs":"/docs/auth.md"}`)
		}, false, "", []string{"deploy", "apps/x", "--to", "main", "--yes"}, 3, nil, "not from a terminal or agent session" + protectedHint + "\n"},
		{"guarded, no terminal, no --yes", func(f *dlFake) { f.on(dlGet, 200, paused.json()).on(dlPost+"deploy dry", 200, deployDry) },
			false, "", []string{"deploy", "apps/x", "--to", "main"}, 4, []string{dlGet, dlPost + "deploy"}, "add --yes"},
		{"guarded, declined", func(f *dlFake) { f.on(dlGet, 200, paused.json()).on(dlPost+"deploy dry", 200, deployDry) },
			true, "n\n", []string{"deploy", "apps/x", "--to", "main"}, 4, []string{dlGet, dlPost + "deploy"}, "Deploy the work tree to main? [y/N] "},
		{"guarded, confirmed", func(f *dlFake) {
			f.on(dlGet, 200, paused.json()).on(dlPost+"deploy dry", 200, deployDry).on(dlPost+"deploy", 200, dlAnswer(paused, "unchanged", true))
		}, true, "y\n", []string{"deploy", "apps/x", "--to", "main"}, 0, []string{dlGet, dlPost + "deploy", dlPost + "deploy"}, ""},
		{"still running", func(f *dlFake) {
			f.on(dlGet, 200, paused.json()).on(dlPost+"deploy dry", 200, deployDry).
				on(dlPost+"deploy", 200, dlAnswer(paused, "deploy", dlEntry(6, "deploy", dlCP2, dlCP, "running", "build", ""))).
				on(dlLog, 200, dlLogAnswer(dlEntry(6, "deploy", dlCP2, dlCP, "running", "build", "")))
		}, false, "", []string{"deploy", "apps/x", "--to", "main", "--yes"}, 5, nil, "deploy 6 on main is still running"},
		{"an xbind without tile deployments", func(f *dlFake) { f.on(dlGet, 404, "404 page not found\n") },
			false, "", []string{"rollback", "apps/x", "--to", "main"}, 6, []string{dlGet}, oldXbindMsg},
	} {
		t.Run(r.name, func(t *testing.T) {
			if r.name == "still running" {
				old := dcWaitMax
				dcWaitMax = 40 * time.Millisecond
				defer func() { dcWaitMax = old }()
			}
			f := newDLFake()
			if r.fake != nil {
				r.fake(f)
			}
			srv := httptest.NewServer(f)
			defer srv.Close()
			got := dlRun(t, srv.URL, r.tty, r.stdin, r.args...)
			if got.code != r.code || !strings.Contains(got.err, r.say) {
				t.Errorf("bx %s: exit %d, want %d; stderr %q wants %q\nstdout %s", strings.Join(r.args, " "), got.code, r.code, got.err, r.say, got.out)
			}
			if r.reqs != nil {
				var paths []string
				for _, l := range f.take() {
					m, uri, _ := strings.Cut(l, " ")
					uri, _, _ = strings.Cut(uri, " ")
					p, _, _ := strings.Cut(uri, "?")
					paths = append(paths, m+" "+p)
				}
				if strings.Join(paths, "\n") != strings.Join(r.reqs, "\n") {
					t.Errorf("bx %s sent:\n%s\nwant\n%s", strings.Join(r.args, " "), strings.Join(paths, "\n"), strings.Join(r.reqs, "\n"))
				}
			}
		})
	}

	// A network error is 1.
	srv := httptest.NewServer(newDLFake())
	srv.Close()
	if got := dlRun(t, srv.URL, false, "", "live-reload", "apps/x"); got.code != exitFailed {
		t.Errorf("network error: exit %d", got.code)
	}

	// Through main: the new codes reach the shell; existing commands keep 1.
	f := newDLFake().on(dlGet, 200, paused.json()).on(dlPost+"deploy dry", 200, deployDry).
		on("GET /api/xbin/backups", 403, `{"error":"admin only","docs":"/docs/auth.md"}`)
	live := httptest.NewServer(f)
	defer live.Close()
	old := httptest.NewServer(http.NewServeMux())
	defer old.Close()
	env := func(url string) []string { return []string{"XBIN_URL=" + url, "XBIN_TOKEN=dl-token"} }
	dir := t.TempDir()
	for _, r := range []struct {
		url  string
		args []string
		code int
	}{
		{live.URL, []string{"deploy", "apps/x", "--to", "main"}, exitNotConfirmed}, // stdin isn't a terminal
		{old.URL, []string{"live-reload", "apps/x"}, exitNoDeployments},
		{old.URL, []string{"deploy", "apps/x", "--to", "main", "--dry-run"}, exitNoDeployments},
		{live.URL, []string{"backups", "apps/x"}, 1},
		{old.URL, []string{"status", "apps/x"}, 1},
		{old.URL, []string{"ls"}, 1},
		{old.URL, []string{"live-reload", "apps/x", "--frobnicate"}, exitUsage},
	} {
		if _, stderr, code := dlExec(t, dir, env(r.url), r.args...); code != r.code {
			t.Errorf("bx %s: exit %d, want %d (%s)", strings.Join(r.args, " "), code, r.code, stderr)
		}
	}
}

// covers 12-compat PO-13 — against an xbind that predates tile deployments
// (Go's mux: a 404 or a 405 without a JSON error), every new command says so
// and exits 6 after its one GET, sending nothing else; a JSON 404 is the
// server's own refusal (exit 1).
func TestBxOldXbind(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "apps/x")
	var mu sync.Mutex
	var seen []string
	record := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Method+" "+r.URL.Path)
			mu.Unlock()
			h.ServeHTTP(w, r)
		})
	}
	notFound := httptest.NewServer(record(http.NewServeMux()))
	defer notFound.Close()
	m := http.NewServeMux()
	m.HandleFunc("POST /api/xbin/deployments", func(http.ResponseWriter, *http.Request) {}) // GET: 405
	notAllowed := httptest.NewServer(record(m))
	defer notAllowed.Close()
	for _, url := range []string{notFound.URL, notAllowed.URL} {
		for _, args := range [][]string{
			{"live-reload"}, {"live-reload", "--json"}, {"live-reload", "pause"}, {"live-reload", "now", "--yes"},
			{"live-reload", "resume", "apps/x"}, {"live-reload", "attach", "--to", "main"},
			{"deploy", "--to", "main", "--yes"}, {"rollback", "apps/x", "--to", "main", "--json"},
		} {
			seen = nil
			got := dlRun(t, url, false, "", args...)
			if got.code != exitNoDeployments || got.err != "bx: "+oldXbindMsg+"\n" || got.out != "" {
				t.Errorf("bx %s: exit %d, stderr %q, stdout %q", strings.Join(args, " "), got.code, got.err, got.out)
			}
			if len(seen) != 1 || seen[0] != "GET /api/xbin/deployments" {
				t.Errorf("bx %s sent %q", strings.Join(args, " "), seen)
			}
		}
	}
	f := newDLFake().on(dlGet, 404, `{"error":"no such tile: apps/x","docs":"/docs/protocol.md"}`)
	srv := httptest.NewServer(f)
	defer srv.Close()
	if got := dlRun(t, srv.URL, false, "", "live-reload", "pause"); got.code != exitFailed || got.err != "bx: no such tile: apps/x\n" {
		t.Errorf("a JSON 404: exit %d, %q", got.code, got.err)
	}
}

// covers SC-AGENT-BX — --json prints the route's answer verbatim, one object
// and nothing else on stdout: the state, a dry run's {state, impact}, an
// operation's {state, deploy} (waited on or not), the server's {error, docs}
// for a refusal, and the Can object bx refused on; the report and the
// progress go to stderr.
func TestBxJSON(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "")
	zero := dlZero().json()
	dry := dlAnswer(dlZero(), "impact", dlImpact("", "", true, "nobody"))
	op := dlAnswer(dlPaused(dlCP2), "deploy", dlEntry(1, "pause", dlCP2, "", "running", "materialize", ""))
	refusal := `{"error":"pausing live reload needs terminal-level access on apps/x","docs":"/docs/auth.md"}`
	can := map[string]any{"ok": false, "why": "pinning a backend to a checkpoint needs isolation (--isolate)", "kind": "policy"}
	canJSON, _ := json.Marshal(can)
	for _, r := range []struct {
		name string
		fake func(f *dlFake)
		args []string
		code int
		out  string
		err  string // in stderr
	}{
		{"the state", func(f *dlFake) { f.on(dlGet, 200, zero) }, []string{"live-reload", "apps/x", "--json"}, 0, zero, ""},
		{"the reader view", func(f *dlFake) {
			f.on(dlGet, 200, `{"tile":"apps/x","record":true,"view":"reader","primary":"main","liveReload":""}`)
		}, []string{"live-reload", "--json", "apps/x"}, 0, `{"tile":"apps/x","record":true,"view":"reader","primary":"main","liveReload":""}`, ""},
		{"a dry run", func(f *dlFake) { f.on(dlGet, 200, zero).on(dlPost+"live-reload/pause dry", 200, dry) },
			[]string{"live-reload", "pause", "apps/x", "--dry-run", "--json"}, 0, dry, "Pause live reload on apps/x\n"},
		{"an operation, not waited on", func(f *dlFake) {
			f.on(dlGet, 200, zero).on(dlPost+"live-reload/pause dry", 200, dry).on(dlPost+"live-reload/pause", 200, op)
		}, []string{"live-reload", "pause", "apps/x", "--json", "--no-wait"}, 0, op, "accepted (running)"},
		{"an operation, waited on", func(f *dlFake) {
			f.on(dlGet, 200, zero).on(dlPost+"live-reload/pause dry", 200, dry).on(dlPost+"live-reload/pause", 200, op).
				on(dlLog, 200, dlLogAnswer(dlEntry(1, "pause", dlCP2, "", "ok", "", "")))
		}, []string{"live-reload", "pause", "apps/x", "--json"}, 0, op, "… ok ("},
		{"a refusal", func(f *dlFake) { f.on(dlGet, 200, zero).on(dlPost+"live-reload/pause dry", 403, refusal) },
			[]string{"live-reload", "pause", "apps/x", "--json"}, exitRefused, refusal, "bx: pausing live reload needs"},
		{"a Can bx refused on", func(f *dlFake) {
			f.on(dlGet, 200, dlZero().with("caller", dlCallerCan(map[string]any{"pause": can})).json())
		}, []string{"live-reload", "pause", "apps/x", "--json"}, exitRefused, string(canJSON), "needs isolation"},
	} {
		t.Run(r.name, func(t *testing.T) {
			f := newDLFake()
			r.fake(f)
			srv := httptest.NewServer(f)
			defer srv.Close()
			got := dlRun(t, srv.URL, false, "", r.args...)
			if got.code != r.code || got.out != r.out+"\n" || !strings.Contains(got.err, r.err) {
				t.Errorf("bx %s: exit %d\nstdout %s\nwant   %s\nstderr %q (wants %q)", strings.Join(r.args, " "), got.code, got.out, r.out, got.err, r.err)
			}
			dec := json.NewDecoder(strings.NewReader(got.out))
			var v map[string]any
			if err := dec.Decode(&v); err != nil || dec.More() {
				t.Errorf("stdout is not one JSON object: %v", err)
			}
		})
	}
}

// covers SC-AGENT-BX — every command that changes where saves go says so:
// pausing live reload, attaching it, resuming it, and a code move onto the
// live reload target, which pauses it; the report says it before acting,
// the result after, and `bx live-reload` shows it (10-ux §12.1).
func TestBxSaysWhereSavesGo(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "")
	attached := dlAttached()
	pausedAfter := dlPaused(dlCP2).with("seq", 8)
	withDev := func(st dlSt, mainCP, devCP string) dlSt {
		return st.with("deployments", []any{dlDep("main", mainCP, true, dlAllCan()), dlDep("dev", devCP, false, dlAllCan())})
	}
	for _, r := range []struct {
		name string
		fake func(f *dlFake)
		args []string
		say  []string
	}{
		{"pause", func(f *dlFake) {
			f.on(dlGet, 200, dlZero().json()).
				on(dlPost+"live-reload/pause dry", 200, dlAnswer(dlZero(), "impact", dlImpact("", "", true, "nobody"))).
				on(dlPost+"live-reload/pause", 200, dlAnswer(dlPaused(dlCP2), "unchanged", true))
		}, []string{"live-reload", "pause", "apps/x"}, []string{
			"Pause live reload on apps/x\n",
			"  Pauses   live reload — saves stop reaching main until bx live-reload now or bx live-reload resume\n",
			"  Affects  nobody now\n",
			"Live reload paused — main is pinned to c:7b19e02. Saves stop reaching main until bx live-reload now or bx live-reload resume.\n",
		}},
		{"deploy onto the live reload target", func(f *dlFake) {
			f.on(dlGet, 200, attached.json()).
				on(dlPost+"deploy dry", 200, dlAnswer(attached, "impact", dlImpact(dlCP, dlCP2, true, "everyone"))).
				on(dlPost+"deploy", 200, dlAnswer(pausedAfter, "deploy", dlEntry(9, "deploy", dlCP2, "", "ok", "", "")))
		}, []string{"deploy", "apps/x", "--to", "main", "--yes"}, []string{
			"Deploy the work tree to main (the primary of apps/x)\n",
			"  Code     main runs c:7b19e02 · 3 files, +40 −12 against c:3f2a1c9\n",
			"  Data     main keeps its data\n",
			"  Pauses   live reload — later saves won't reach main until you resume (bx live-reload resume)\n",
			"  Affects  everyone using apps/x: frames reload once; WebSocket and SSE connections drop at the 30 s drain\n",
			"main now runs c:7b19e02.\n",
			"Live reload paused — main is pinned to c:7b19e02. Saves stop reaching main",
		}},
		{"roll back the live reload target", func(f *dlFake) {
			f.on(dlGet, 200, attached.json()).
				on(dlPost+"rollback dry", 200, dlAnswer(attached, "impact", dlImpact(dlCP, dlCP0, true, "everyone"))).
				on(dlPost+"rollback", 200, dlAnswer(dlPaused(dlCP0), "deploy", dlEntry(10, "rollback", dlCP0, dlCP, "ok", "", "")))
		}, []string{"rollback", "apps/x", "--to", "main", "--yes"}, []string{
			"Roll back main to c:1e9d0aa (the primary of apps/x)\n",
			"  Data     stays as it is — a roll back moves code, not state\n",
			"main now runs c:1e9d0aa again (rolled back from c:3f2a1c9).\n",
			"Live reload paused — main is pinned to c:1e9d0aa.",
		}},
		{"resume onto main, back to the zero state", func(f *dlFake) {
			f.on(dlGet, 200, dlPaused(dlCP).json()).
				on(dlPost+"live-reload/resume dry", 200, dlAnswer(dlPaused(dlCP), "impact", dlImpact(dlCP, dlCP2, false, "everyone"))).
				on(dlPost+"live-reload/resume", 200, dlAnswer(dlZero(), "deploy", dlEntry(11, "resume", dlCP2, dlCP, "ok", "", "")))
		}, []string{"live-reload", "resume", "apps/x", "--yes"}, []string{
			"Resume live reload on main (the primary of apps/x)\n",
			"  Code     main switches to the work tree now: 3 files (+40 −12) changed since main's c:3f2a1c9 ship at once, then every save reaches main\n",
			"Live reload: main — saves reach main again, and everyone using apps/x.\n",
			"apps/x is back to plain live reload, with no deployments.\n",
		}},
		{"attach to dev", func(f *dlFake) {
			f.on(dlGet, 200, withDev(attached, "", dlCP).json()).
				on(dlPost+"live-reload/attach dry", 200, dlAnswer(withDev(attached, "", dlCP), "impact", dlImpact(dlCP, dlCP2, false, "deployment"))).
				on(dlPost+"live-reload/attach", 200, dlAnswer(withDev(attached.with("liveReload", "dev"), dlCP2, ""), "unchanged", true))
		}, []string{"live-reload", "attach", "apps/x", "--to", "dev"}, []string{
			"Attach live reload to dev\n",
			"  Affects  only people using apps/x+dev\n",
			"Live reload: dev — saves reach apps/x+dev; main is pinned to c:7b19e02.\n",
		}},
		{"the state, paused", func(f *dlFake) { f.on(dlGet, 200, dlPaused(dlCP).json()) }, []string{"live-reload", "apps/x"}, []string{
			"apps/x: paused by ana 12m ago — 3 files changed since c:3f2a1c9 (main)\n",
			"  main  primary  pinned to c:3f2a1c9   healthy g3\n",
			"  bx live-reload now ships the work tree to main once; bx live-reload resume follows every save again\n",
		}},
		{"the state, zero", func(f *dlFake) { f.on(dlGet, 200, dlZero().json()) }, []string{"live-reload", "apps/x"}, []string{
			"apps/x: main — every save reaches everyone (no deployments)\n",
		}},
		{"the state, attached to dev", func(f *dlFake) { f.on(dlGet, 200, withDev(attached.with("liveReload", "dev"), dlCP2, "").json()) },
			[]string{"live-reload", "apps/x"}, []string{"apps/x: dev — saves reach apps/x+dev; main pinned to c:7b19e02\n"}},
	} {
		t.Run(r.name, func(t *testing.T) {
			f := newDLFake()
			r.fake(f)
			srv := httptest.NewServer(f)
			defer srv.Close()
			got := dlRun(t, srv.URL, false, "", r.args...)
			if got.code != 0 {
				t.Fatalf("bx %s: exit %d: %s", strings.Join(r.args, " "), got.code, got.err)
			}
			for _, s := range r.say {
				if !strings.Contains(got.out, s) {
					t.Errorf("bx %s: stdout lacks %q:\n%s", strings.Join(r.args, " "), s, got.out)
				}
			}
		})
	}

	// A code move onto the live reload target that fails still says live
	// reload paused: the record moved when the request was committed.
	failed := newDLFake().on(dlGet, 200, attached.json()).
		on(dlPost+"deploy dry", 200, dlAnswer(attached, "impact", dlImpact(dlCP, dlCP2, true, "everyone"))).
		on(dlPost+"deploy", 200, dlAnswer(dlPaused(dlCP), "deploy", dlEntry(12, "deploy", dlCP2, dlCP, "running", "build", ""))).
		on(dlLog, 200, dlLogAnswer(dlEntry(12, "deploy", dlCP2, dlCP, "failed", "build", "exit status 1")))
	fsrv := httptest.NewServer(failed)
	defer fsrv.Close()
	if got := dlRun(t, fsrv.URL, false, "", "deploy", "apps/x", "--to", "main", "--yes"); got.code != exitFailed ||
		!strings.Contains(got.out, "Live reload paused — main is pinned to c:3f2a1c9. Saves stop reaching main") {
		t.Errorf("a failed deploy onto the live reload target: exit %d\n%s%s", got.code, got.out, got.err)
	}

	// Under --json the words go to stderr, stdout keeping only the answer.
	f := newDLFake().on(dlGet, 200, dlZero().json()).
		on(dlPost+"live-reload/pause dry", 200, dlAnswer(dlZero(), "impact", dlImpact("", "", true, "nobody"))).
		on(dlPost+"live-reload/pause", 200, dlAnswer(dlPaused(dlCP2), "unchanged", true))
	srv := httptest.NewServer(f)
	defer srv.Close()
	if got := dlRun(t, srv.URL, false, "", "live-reload", "pause", "apps/x", "--json"); !strings.Contains(got.err, "Live reload paused — main is pinned to c:7b19e02.") || strings.Contains(got.out, "Live reload") {
		t.Errorf("--json: stdout %q stderr %q", got.out, got.err)
	}
}

// covers P21 SC-SAFE-DEPLOY — what a code move sends names what its report
// showed (11-contract §9.1): a deploy from the work tree sends the dry run's
// capture as expect, a roll back its checkpoint, reload now its expect;
// guarded commands send the state's seq, a protected primary's deploy the
// capture as checkpoint; pausing names nothing; the dry run is the same
// request with dryRun.
func TestBxNamesReviewedCode(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "apps/x")
	paused := dlPaused(dlCP)
	for _, r := range []struct {
		st    dlSt
		route string
		imp   map[string]any
		args  []string
		dry   string
		real  string
	}{
		{paused, "deploy", dlImpact(dlCP, dlCP2, false, "everyone"), []string{"deploy", "--to", "main", "--yes"},
			`{"deployment":"main","dryRun":true,"tile":"apps/x"}`, `{"deployment":"main","expect":"c:7b19e02","seq":4,"tile":"apps/x"}`},
		{paused, "deploy", dlImpact(dlCP, dlCP0, false, "everyone"), []string{"deploy", "--to", "main", "--checkpoint", dlCP0, "--yes"},
			`{"checkpoint":"c:1e9d0aa","deployment":"main","dryRun":true,"tile":"apps/x"}`, `{"checkpoint":"c:1e9d0aa","deployment":"main","seq":4,"tile":"apps/x"}`},
		{paused.with("protectedPrimary", true), "deploy", dlImpact(dlCP, dlCP2, false, "everyone"), []string{"deploy", "--to", "main", "--yes"},
			`{"deployment":"main","dryRun":true,"tile":"apps/x"}`, `{"checkpoint":"c:7b19e02","deployment":"main","seq":4,"tile":"apps/x"}`},
		{paused, "rollback", dlImpact(dlCP, dlCP0, false, "everyone"), []string{"rollback", "--to", "main", "--yes"},
			`{"deployment":"main","dryRun":true,"tile":"apps/x"}`, `{"checkpoint":"c:1e9d0aa","deployment":"main","seq":4,"tile":"apps/x"}`},
		{paused, "live-reload/now", dlImpact(dlCP, dlCP2, false, "everyone"), []string{"live-reload", "now", "--yes"},
			`{"dryRun":true,"tile":"apps/x"}`, `{"expect":"c:7b19e02","seq":4,"tile":"apps/x"}`},
		{dlZero(), "live-reload/pause", dlImpact("", "", true, "nobody"), []string{"live-reload", "pause"},
			`{"dryRun":true,"tile":"apps/x"}`, `{"tile":"apps/x"}`},
		{dlAttached().with("selected", "dev", "deployments", []any{dlDep("main", "", true, dlAllCan()), dlDep("dev", dlCP, false, dlAllCan())}),
			"deploy", dlImpact(dlCP, dlCP2, false, "deployment"), []string{"deploy", "apps/x+dev"}, // not the primary: no --yes needed
			`{"dryRun":true,"tile":"apps/x+dev"}`, `{"expect":"c:7b19e02","tile":"apps/x+dev"}`},
	} {
		f := newDLFake().on(dlGet, 200, r.st.json()).
			on(dlPost+r.route+" dry", 200, dlAnswer(r.st, "impact", r.imp)).
			on(dlPost+r.route, 200, dlAnswer(r.st, "unchanged", true))
		srv := httptest.NewServer(f)
		got := dlRun(t, srv.URL, false, "", r.args...)
		srv.Close()
		reqs := f.take()
		want := []string{"GET /api/xbin/deployments?tile=" + url.QueryEscape(r.st["tile"].(string)),
			"POST /api/xbin/deployments/" + r.route + " " + r.dry, "POST /api/xbin/deployments/" + r.route + " " + r.real}
		if strings.Contains(r.args[len(r.args)-1], "+") {
			want[0] = "GET /api/xbin/deployments?tile=apps%2Fx%2Bdev"
		}
		if got.code != 0 || strings.Join(reqs, "\n") != strings.Join(want, "\n") {
			t.Errorf("bx %s: exit %d (%s)\nsent\n%s\nwant\n%s", strings.Join(r.args, " "), got.code, got.err, strings.Join(reqs, "\n"), strings.Join(want, "\n"))
		}
	}
}

// covers Q23 SC-AGENT-BX — bx logs reads GET /logs where the log file can't
// answer: a workspace whose .xbin/log it can't see (.xbin is masked in an
// isolated terminal) gets the whole log up to the route's tail, -f the
// follow stream, and xbind's refusal exits 1; a visible .xbin/log is read as
// today, with no request.
func TestBxLogsWhereTheFileCantAnswer(t *testing.T) {
	ws := t.TempDir()
	for _, d := range []string{".xbin", "apps/x"} {
		if err := os.MkdirAll(filepath.Join(ws, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "xbin.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newDLFake().
		on("GET /api/xbin/logs", 200, "--- gen 1 start ---\nhello\n").
		on("GET /api/xbin/logs", 200, "--- gen 1 start ---\nfollowing\n").
		on("GET /api/xbin/logs", 404, `{"error":"no logs yet — the backend hasn't started","docs":"/docs/protocol.md"}`)
	srv := httptest.NewServer(f)
	defer srv.Close()
	env := []string{"XBIN_URL=" + srv.URL, "XBIN_TOKEN=dl-token", "XBIN_COMPONENT=apps/x"}
	for _, r := range []struct {
		args []string
		out  string
		code int
		req  string
	}{
		{[]string{"logs", "apps/x"}, "--- gen 1 start ---\nhello\n", 0, "GET /api/xbin/logs?component=apps%2Fx&tail=1048576"},
		{[]string{"logs", "-f", "apps/x"}, "--- gen 1 start ---\nfollowing\n", 0, "GET /api/xbin/logs?component=apps%2Fx&follow=1"},
		{[]string{"logs", "apps/x"}, "", 1, "GET /api/xbin/logs?component=apps%2Fx&tail=1048576"},
	} {
		out, stderr, code := dlExec(t, ws, env, r.args...)
		if reqs := f.take(); out != r.out || code != r.code || len(reqs) != 1 || reqs[0] != r.req {
			t.Errorf("bx %s: exit %d, stdout %q, stderr %q, sent %q", strings.Join(r.args, " "), code, out, stderr, reqs)
		}
	}
	if _, stderr, _ := dlExec(t, ws, env, "logs", "apps/x"); !strings.Contains(stderr, "bx: no logs yet — the backend hasn't started (404 Not Found)") {
		t.Errorf("refusal: %q", stderr)
	}
	f.take()
	key := util.CompKey("apps/x")
	if err := os.MkdirAll(filepath.Join(ws, ".xbin", "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".xbin", "log", key+".log"), []byte("from the file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _, code := dlExec(t, ws, env, "logs", "apps/x"); out != "from the file\n" || code != 0 || len(f.take()) != 0 {
		t.Errorf("a visible .xbin/log: exit %d, %q", code, out)
	}
}
