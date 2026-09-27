package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
