//go:build linux && integration

// Package llm drives xbind's LLM connector end to end (D226, docs/llm.md):
// an `xbind --isolate` built from this tree (test/xbindtest), auth on, with
// hack/fakeopenai — the scripted OpenAI-compatible server the UI harness
// uses — as the upstream, twice: once from the host's deployment file
// (XBIN_LLM_CONFIG, its key in a file) and once added by the admin with
// `bx llm` (its key in the vault). No real provider is ever called.
//
// TestConnector, in order:
//   - booted from the deployment file: its settings are in force and
//     locked, and a write to one is refused naming it;
//   - `bx llm upstream add` and `test` against the daemon;
//   - a streamed Chat Completions call is relayed as it arrives (the
//     fake's chunks reach the client spread over time, not at once) with
//     the usage chunk the caller didn't ask for, and is metered to the
//     person; a streamed Responses call the same;
//   - a client that goes away ends the upstream's call after the grace
//     (XBIN_LLM_CANCEL_GRACE=1s here; the fake, 6 s into nothing, records
//     the hang-up), and the call is still metered — as an estimate;
//   - a made-up model name is metered as <upstream>/(unknown), and a
//     malformed one refused;
//   - a per-user quota stops at its limit with 429, naming the quota and
//     the UTC midnight it resets at, and holds nobody else;
//   - a tile is refused until an admin binds its llm slot to xbin:llm, a
//     non-admin can't bind it, and the bound tile's call — a frame's, with
//     nothing but its frame token — is metered to the person driving it
//     and to the tile;
//   - neither upstream key appears in any answer, in xbind's log, or in
//     the files under data/llm (the vault's sealed keys aside);
//   - the counters and the settings survive a restart.
//
// Needs unprivileged user namespaces and the rootfs (xbindtest.Require);
// it skips saying so without them. Ports: free ones, or — a run that shares
// its host with others — XBIN_LLM_TEST_PORT=<n> pins the fake to n and
// xbind to n+2.
//
//	go test -tags=integration -count=1 ./test/llm/
//
// On a dev box run it with the Bash sandbox disabled.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/portlease"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestMain(m *testing.M) { xbindtest.Main(m) }

const (
	hostKey  = "sk-host-INTEGRATION-SECRET-aaaa1111"
	adminKey = "sk-admin-INTEGRATION-SECRET-bbbb2222"
	pw       = "a-password-123456"
)

// addrs are where the fake and xbind listen: free ports, or the pinned ones.
func addrs(t *testing.T) (fake, xbind string) {
	t.Helper()
	if v := os.Getenv("XBIN_LLM_TEST_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("XBIN_LLM_TEST_PORT=%q: a port", v)
		}
		return fmt.Sprintf("127.0.0.1:%d", n), fmt.Sprintf("127.0.0.1:%d", n+2)
	}
	return portlease.Addr(t), "" // xbind: xbindtest picks
}

// startFake builds hack/fakeopenai and runs it on addr until the test ends.
func startFake(t *testing.T, a *xbindtest.Assets, addr string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakeopenai")
	build := exec.Command("go", "build", "-o", bin, "./hack/fakeopenai")
	build.Dir = a.Repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hack/fakeopenai: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "-addr", addr)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	url := "http://" + addr
	xbindtest.Eventually(t, 20*time.Second, "fakeopenai answering", func() (bool, string) {
		r, err := http.Get(url + "/v1/models")
		if err != nil {
			return false, err.Error()
		}
		r.Body.Close()
		return r.StatusCode == 200, r.Status
	})
	return url
}

// bx runs the tree's bx against the daemon as the owner.
func bx(t *testing.T, d *xbindtest.Daemon, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(d.A.Bin, "bx"), args...)
	cmd.Env = append(os.Environ(), "XBIN_URL="+d.URL, "XBIN_TOKEN="+d.Token())
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func bearer(tok string) xbindtest.Header { return xbindtest.H("Authorization", "Bearer "+tok) }

func chat(model, text string, stream bool) map[string]any {
	b := map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}}
	if model != "" {
		b["model"] = model
	}
	if stream {
		b["stream"] = true
	}
	return b
}

type usageRow struct {
	Day, User, Component, Model   string
	Requests, Failed, Estimated   int64
	InputTokens, OutputTokens     int64
	CachedTokens, ReasoningTokens int64
	Cost                          float64
}

func TestConnector(t *testing.T) {
	a := xbindtest.Require(t)
	fakeAddr, xbindAddr := addrs(t)
	fake := startFake(t, a, fakeAddr)

	began := time.Now()
	step := func(what string) { t.Helper(); t.Logf("%5.1fs  %s", time.Since(began).Seconds(), what) }

	// every answer the test reads, for the secrets check at the end
	var seen bytes.Buffer
	note := func(b []byte) []byte { seen.Write(b); seen.WriteByte('\n'); return b }

	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "house.key"), []byte(hostKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostFile := filepath.Join(hostDir, "llm.jsonc")
	if err := os.WriteFile(hostFile, []byte(`{
		// the host's settings: locked for the workspace's admins
		"people": "everyone",
		"upstreams": {"house": {"baseURL": "`+fake+`/v1", "keyFile": "house.key", "models": ["fake-chat"],
			"prices": {"fake-chat": {"in": 1, "out": 2}}}},
		"defaults": {"model": "house/fake-chat"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Addr: xbindAddr, Env: []string{"XBIN_LLM_CONFIG=" + hostFile, "XBIN_LLM_CANCEL_GRACE=1s"}})
	call := func(method, path string, body any, hdrs ...xbindtest.Header) xbindtest.Resp {
		t.Helper()
		r := d.Call(t, method, path, body, hdrs...)
		note(r.Body)
		return r
	}
	must := func(want int, method, path string, body any, hdrs ...xbindtest.Header) xbindtest.Resp {
		t.Helper()
		r := call(method, path, body, hdrs...)
		if r.Status != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, r.Status, want, r)
		}
		return r
	}
	errCode := func(r xbindtest.Resp) string {
		var e struct {
			Error struct{ Message, Type, Code, Quota, Resets string }
		}
		_ = json.Unmarshal(r.Body, &e)
		return e.Error.Code
	}
	usage := func(query string, hdrs ...xbindtest.Header) []usageRow {
		t.Helper()
		var out struct{ Rows []usageRow }
		must(200, "GET", "/api/xbin/llm/usage"+query, nil, hdrs...).Decode(t, &out)
		return out.Rows
	}

	d.AddUser(t, "bob", pw, "user", map[string]string{"apps/talk": "read"})
	d.AddUser(t, "cy", pw, "user", map[string]string{"apps/talk": "write"})
	bob, cy := bearer(d.Login(t, "bob", pw)), bearer(d.Login(t, "cy", pw))

	// ---- booted from the deployment file: in force, and locked ----
	step("booted from the deployment file: in force, and locked")
	var view struct {
		Configured bool
		People     string
		Upstreams  []struct {
			Name, Source string
			Locked       bool
			Key          struct {
				Set          bool
				Source, Hint string
			}
		}
		Locks map[string]struct {
			Locked bool
			Reason string
		}
		Vault string
	}
	must(200, "GET", "/api/xbin/llm/config", nil).Decode(t, &view)
	if !view.Configured || view.People != "everyone" || len(view.Upstreams) != 1 || view.Upstreams[0].Name != "house" || !view.Upstreams[0].Locked ||
		view.Upstreams[0].Key.Source != "host" || view.Upstreams[0].Key.Hint != "" || view.Locks["defaults.model"].Reason != "set by your host" ||
		!view.Locks["people"].Locked || !view.Locks["upstreams.house"].Locked || view.Vault != "open" {
		t.Fatalf("the configuration booted from the file: %+v", view)
	}
	for what, r := range map[string]xbindtest.Resp{
		"the default model":   call("PUT", "/api/xbin/llm/config", map[string]any{"defaults": map[string]any{"model": "mine/fake-chat"}}),
		"the people switch":   call("PUT", "/api/xbin/llm/config", map[string]any{"people": "nobody"}),
		"the host's upstream": call("PUT", "/api/xbin/llm/upstreams/house", map[string]any{"baseURL": "https://elsewhere.example/v1"}),
		"removing it":         call("DELETE", "/api/xbin/llm/upstreams/house", nil),
	} {
		var e struct {
			Error  string
			Locked []string
			Reason string
		}
		if _ = json.Unmarshal(r.Body, &e); r.Status != 409 || len(e.Locked) != 1 || e.Reason != "set by your host" || !strings.Contains(e.Error, "set by your host") {
			t.Errorf("changing %s, which the host's file sets: %d %s", what, r.Status, r)
		}
	}
	if out, err := bx(t, d, "llm", "default", "set", "mine/fake-chat"); err == nil || !strings.Contains(out, "set by your host") {
		t.Errorf("bx llm default set on a locked default: %v\n%s", err, out)
	}
	// a person who is no admin reads no settings
	if r := call("GET", "/api/xbin/llm/config", nil, bob); r.Status != 403 {
		t.Errorf("GET /llm/config as bob: %d", r.Status)
	}

	// ---- the admin's own upstream, with bx ----
	step("the admin's own upstream, with bx")
	keyFile := filepath.Join(t.TempDir(), "mine.key")
	if err := os.WriteFile(keyFile, []byte(adminKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := bx(t, d, "llm", "upstream", "add", "mine", fake, "--key-file", keyFile)
	note([]byte(out))
	if err != nil || !strings.Contains(out, "upstream mine") || !strings.Contains(out, "key …2222") || !strings.Contains(out, "upstream house") || !strings.Contains(out, "[set by your host]") {
		t.Fatalf("bx llm upstream add: %v\n%s", err, out)
	}
	out, err = bx(t, d, "llm", "upstream", "test", "mine", "fake-chat")
	note([]byte(out))
	if err != nil || !strings.Contains(out, "upstream mine: ok") {
		t.Fatalf("bx llm upstream test: %v\n%s", err, out)
	}
	out, _ = bx(t, d, "llm", "models")
	note([]byte(out))
	for _, m := range []string{"house/fake-chat", "mine/fake-chat", "mine/gpt-5-fake", "xbin/default → house/fake-chat"} {
		if !strings.Contains(out, m) {
			t.Errorf("bx llm models lacks %q:\n%s", m, out)
		}
	}

	// ---- a streamed call: relayed as it arrives, metered ----
	step("a streamed call: relayed as it arrives, metered")
	stream := func(path string, body any, h xbindtest.Header) (lines []string, at []time.Duration) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", d.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(h.K, h.V)
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: %d %s %s", path, resp.StatusCode, resp.Header.Get("Content-Type"), b)
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "data:") {
				note([]byte(l))
				lines, at = append(lines, l), append(at, time.Since(start))
			}
		}
		return lines, at
	}
	lines, at := stream("/api/xbin/llm/v1/chat/completions", chat("", "hello", true), bob) // no model: the host's default
	// the fake thinks 150 ms and then says five words 40 ms apart: a relay
	// that held the stream back would hand everything over at once
	if n := len(lines); n < 7 || at[n-1]-at[0] < 150*time.Millisecond {
		t.Errorf("the stream arrived in %d events within %v of each other: it was buffered\n%v", len(lines), at[len(at)-1]-at[0], at)
	}
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, `"Hello "`) || !strings.Contains(all, `"prompt_tokens":100`) || !strings.HasSuffix(all, "data: [DONE]") {
		t.Errorf("the stream: %s", all)
	}
	lines, _ = stream("/api/xbin/llm/v1/responses", map[string]any{"model": "mine/gpt-5-fake", "stream": true, "input": []any{map[string]any{"role": "user", "content": "hello"}}}, bob)
	if all := strings.Join(lines, "\n"); !strings.Contains(all, `"type":"response.completed"`) || !strings.Contains(all, `"input_tokens":100`) {
		t.Errorf("the Responses stream: %s", all)
	}
	xbindtest.Eventually(t, 10*time.Second, "bob's two calls metered", func() (bool, string) {
		rows := usage("", bob)
		if len(rows) != 2 {
			return false, fmt.Sprint(rows)
		}
		house, mine := rows[0], rows[1]
		ok := house.User == "bob" && house.Component == "" && house.Model == "house/fake-chat" && house.Requests == 1 && house.InputTokens == 100 && house.OutputTokens == 20 &&
			house.ReasoningTokens == 9 && house.Cost > 0.000139 && house.Cost < 0.000141 && // 100 in at $1/M + 20 out at $2/M
			mine.Model == "mine/gpt-5-fake" && mine.InputTokens == 100 && mine.OutputTokens == 20 && mine.ReasoningTokens == 7 && mine.Cost == 0
		return ok, fmt.Sprintf("%+v", rows)
	})
	if rows := usage("", cy); len(rows) != 0 {
		t.Errorf("cy reads rows that aren't hers: %+v", rows)
	}
	if r := call("GET", "/api/xbin/llm/usage?user=bob", nil, cy); r.Status != 403 {
		t.Errorf("cy reading bob's usage: %d", r.Status)
	}

	// ---- a client that goes away ends the upstream call ----
	step("a client that goes away ends the upstream call")
	type fakeReq struct {
		ID       int64
		Last     string
		End      int64
		Canceled bool
	}
	fakeReqs := func() []fakeReq {
		var out []fakeReq
		r, err := http.Get(fake + "/debug/requests")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		_ = json.NewDecoder(r.Body).Decode(&out)
		return out
	}
	ctx, cancel := context.WithCancel(context.Background())
	// the fake's subagent "slow job" waits 6 s before it answers: room enough for a loaded machine
	slow := chat("house/fake-chat", "slow job", true)
	slow["messages"] = append([]any{map[string]any{"role": "system", "content": "You are a subagent"}}, slow["messages"].([]any)...)
	raw, _ := json.Marshal(slow)
	req, _ := http.NewRequestWithContext(ctx, "POST", d.URL+"/api/xbin/llm/v1/chat/completions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(cy.K, cy.V)
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	xbindtest.Eventually(t, 10*time.Second, "the slow call reaching the fake", func() (bool, string) {
		rs := fakeReqs()
		return len(rs) > 0 && strings.Contains(rs[len(rs)-1].Last, "slow job"), fmt.Sprint(len(rs))
	})
	cancel()
	if err := <-done; err == nil {
		t.Error("the cancelled call returned an answer")
	}
	xbindtest.Eventually(t, 4*time.Second, "the fake seeing its caller hang up, before its 6 s are over", func() (bool, string) {
		rs := fakeReqs()
		last := rs[len(rs)-1]
		return last.Canceled && last.End != 0, fmt.Sprintf("%+v", last)
	})
	// …and the call was not free: it is cy's, charged an estimate of what was sent
	xbindtest.Eventually(t, 10*time.Second, "the abandoned call metered as an estimate", func() (bool, string) {
		rows := usage("", cy)
		return len(rows) == 1 && rows[0].Requests == 1 && rows[0].Estimated == 1 && rows[0].InputTokens > 10 && rows[0].Cost > 0, fmt.Sprintf("%+v", rows)
	})

	// ---- a caller's own model names make no rows of their own ----
	step("a caller's own model names make no rows of their own")
	for i := 0; i < 5; i++ {
		must(200, "POST", "/api/xbin/llm/v1/chat/completions", chat(fmt.Sprintf("mine/made-up-%d", i), "hello", false), cy) // the fake answers any name
	}
	if rows := usage("?model=" + "mine/(unknown)"); len(rows) != 1 || rows[0].Requests != 5 || rows[0].User != "cy" {
		t.Errorf("five made-up model names: %+v", rows)
	}
	for _, row := range usage("") {
		if strings.Contains(row.Model, "made-up") {
			t.Errorf("a caller's own model name has a usage row: %+v", row)
		}
	}
	if r := call("POST", "/api/xbin/llm/v1/chat/completions", chat("mine/"+strings.Repeat("x", 300), "hello", false), cy); r.Status != 400 || errCode(r) != "xbin_llm_model" {
		t.Errorf("a model name past the cap: %d %s", r.Status, r)
	}
	if r := call("POST", "/api/xbin/llm/v1/chat/completions", `{"model":"house/fake-chat","stream":"true","messages":[{"role":"user","content":"hello"}]}`, cy, xbindtest.H("Content-Type", "application/json")); r.Status != 400 {
		t.Errorf("a stream flag that is no boolean: %d %s", r.Status, r)
	}

	// ---- a per-user quota stops at its limit and names its reset ----
	step("a per-user quota stops at its limit and names its reset")
	// bob has used 240 tokens; with 300 a day one more call starts, and the next is refused
	out, err = bx(t, d, "llm", "quota", "set", "user:bob", "tokens/day=300")
	note([]byte(out))
	if err != nil || !strings.Contains(out, "user:bob") || !strings.Contains(out, "tokens/day=300") {
		t.Fatalf("bx llm quota set: %v\n%s", err, out)
	}
	must(200, "POST", "/api/xbin/llm/v1/chat/completions", chat("", "hello", false), bob)
	r := call("POST", "/api/xbin/llm/v1/chat/completions", chat("", "hello", false), bob)
	var refusal struct {
		Error struct {
			Message, Type, Code, Quota, Resets string
			Limit, Used                        float64
		}
	}
	_ = json.Unmarshal(r.Body, &refusal)
	midnight := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).Format(time.RFC3339)
	e := refusal.Error
	// (a run across UTC midnight would see the next day's reset — and no refusal: the window is new)
	if r.Status != 429 || e.Code != "xbin_llm_quota_exceeded" || e.Type != "insufficient_quota" || e.Quota != "user.tokensPerDay" || e.Resets != midnight ||
		e.Limit != 300 || e.Used != 360 || !strings.Contains(e.Message, "resets at "+midnight) || r.Header.Get("Retry-After") == "" {
		t.Errorf("at the quota: %d %s (Retry-After %q; midnight %s)", r.Status, r, r.Header.Get("Retry-After"), midnight)
	}
	before := len(fakeReqs())
	if r := call("POST", "/api/xbin/llm/v1/responses", map[string]any{"model": "mine/gpt-5-fake", "input": "hello"}, bob); r.Status != 429 {
		t.Errorf("the quota on the other wire: %d", r.Status)
	}
	if len(fakeReqs()) != before {
		t.Error("a call refused by the quota reached the upstream")
	}
	must(200, "POST", "/api/xbin/llm/v1/chat/completions", chat("", "hello", false), cy) // nobody else is held
	out, _ = bx(t, d, "llm", "quota", "show", "bob")
	note([]byte(out))
	if !strings.Contains(out, "tokens/day (user, stop): 360 of 300") || !strings.Contains(out, "reached (stop)") {
		t.Errorf("bx llm quota show bob:\n%s", out)
	}

	// ---- a tile calls by its binding, and the call is the person's ----
	step("a tile calls by its binding, and the call is the person's")
	d.WriteTile(t, "apps/talk", map[string]string{
		"xbin.json":  `{"title": "Talk", "interfaces": {"llm": {"kind": "http", "service": "openai"}}}`,
		"index.html": `<!doctype html><title>talk</title>`,
	})
	var ft struct{ Token string }
	must(200, "GET", "/api/xbin/frame-token?component=apps/talk", nil, cy).Decode(t, &ft)
	frame := xbindtest.FrameHeader(ft.Token) // what a sandboxed tile page holds: nothing else
	if r := call("POST", "/api/xbin/llm/v1/chat/completions", chat("", "hello", false), frame); r.Status != 403 || errCode(r) != "xbin_llm_not_bound" {
		t.Errorf("an unbound tile: %d %s", r.Status, r)
	}
	bind := map[string]string{"component": "apps/talk", "slot": "llm", "provider": "xbin:llm"}
	if r := call("POST", "/api/xbin/bindings", bind, cy); r.Status != 403 { // cy writes the tile; binding the connector is an admin's
		t.Errorf("a non-admin binding xbin:llm: %d %s", r.Status, r)
	}
	must(200, "POST", "/api/xbin/bindings", bind)
	must(200, "POST", "/api/xbin/llm/v1/chat/completions", chat("", "hello", false), frame)
	xbindtest.Eventually(t, 10*time.Second, "the tile's call metered to cy and the tile", func() (bool, string) {
		rows := usage("?component=apps/talk")
		return len(rows) == 1 && rows[0].User == "cy" && rows[0].Component == "apps/talk" && rows[0].Requests == 1 && rows[0].InputTokens == 100, fmt.Sprintf("%+v", rows)
	})
	// the frame is not its person: it reads no usage and no settings
	for _, path := range []string{"/api/xbin/llm/usage", "/api/xbin/llm/config", "/api/xbin/llm/quotas"} {
		if r := call("GET", path, nil, frame); r.Status != 403 {
			t.Errorf("a tile's frame on %s: %d", path, r.Status)
		}
	}
	must(200, "DELETE", "/api/xbin/bindings", map[string]string{"component": "apps/talk", "slot": "llm"})
	if r := call("POST", "/api/xbin/llm/v1/chat/completions", chat("", "hello", false), frame); r.Status != 403 {
		t.Errorf("after the unbind: %d", r.Status)
	}

	// ---- a restart keeps the counters and the settings ----
	step("a restart keeps the counters and the settings")
	total := func() (requests, in int64) {
		for _, row := range usage("") {
			requests, in = requests+row.Requests, in+row.InputTokens
		}
		return
	}
	reqsBefore, inBefore := total()
	d.Restart(t)
	if reqs, in := total(); reqs != reqsBefore || in != inBefore || reqs < 12 {
		t.Errorf("usage after a restart: %d requests, %d input tokens; before: %d, %d", reqs, in, reqsBefore, inBefore)
	}
	must(200, "GET", "/api/xbin/llm/config", nil).Decode(t, &view)
	if len(view.Upstreams) != 2 || view.Upstreams[1].Name != "mine" || view.Upstreams[1].Key.Hint != "…2222" {
		t.Errorf("the settings after a restart: %+v", view.Upstreams)
	}
	must(200, "POST", "/api/xbin/llm/upstreams/mine/test", map[string]any{"model": "fake-chat"}) // its key was read back out of the vault

	// ---- the keys are nowhere they shouldn't be ----
	step("the keys are nowhere they shouldn't be")
	logged, err := os.ReadFile(d.LogPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) == 0 || !bytes.Contains(logged, []byte("llm")) {
		t.Errorf("xbind's log (%d bytes) doesn't mention the connector: is this the right file?", len(logged))
	}
	leaks := map[string][]byte{"an answer": seen.Bytes(), "xbind's log": logged}
	for _, f := range []string{"config.json", "keys.json"} {
		b, err := os.ReadFile(filepath.Join(d.WS, "data", "llm", f))
		if err != nil {
			t.Fatal(err)
		}
		leaks["data/llm/"+f] = b
	}
	months, _ := filepath.Glob(filepath.Join(d.WS, "data", "llm", "usage", "*.json"))
	if len(months) != 1 {
		t.Errorf("usage files: %v", months)
	}
	for _, f := range months {
		leaks[f], _ = os.ReadFile(f)
	}
	for where, b := range leaks {
		for name, key := range map[string]string{"the host's": hostKey, "the admin's": adminKey} {
			if bytes.Contains(b, []byte(key)) {
				t.Errorf("%s upstream key appears in %s", name, where)
			}
		}
		if bytes.Contains(b, []byte("Hello from the fake model")) && where != "an answer" {
			t.Errorf("a model's answer appears in %s", where)
		}
		if bytes.Contains(b, []byte("made-up-")) && where != "an answer" {
			t.Errorf("a caller's own model name appears in %s", where)
		}
	}
}
