//go:build linux && integration

package isolated

// wsagent_real_test.go — the workspace agent against a REAL model
// (plans/wsagent/real-model-run-2026-11-17.md; hack/wsagent-real-run.sh runs
// it). Everything else in this package scripts the model; this file is the
// rig for the manual scenario list (builtin-templates/agent/_backend/
// testdata/ws-real-model-scenarios.md): an xbind as an install runs one,
// with the LLM connector's upstream set by a host file to an endpoint the
// caller names, a seeded synthetic workspace, and each scenario driven in a
// fresh session the way a person would — approving what the scenario says
// to approve, denying what it says to deny.
//
// It is skipped unless XBIN_REAL_LLM_BASE is set, so `go test` and CI never
// call a provider:
//
//	XBIN_REAL_LLM_BASE     the upstream's base URL (https://…/v1)
//	XBIN_REAL_LLM_MODEL    the model's id AT THE UPSTREAM (may hold a slash)
//	XBIN_REAL_LLM_KEYFILE  a file holding the key, one line (never the key)
//	XBIN_REAL_OUT          where transcripts and the graded table go
//	XBIN_REAL_ADDR         the address xbind listens on (default: a free port)
//	XBIN_REAL_QUOTA        tokens per person and day (default 3000000)
//	XBIN_REAL_SCENARIOS    a comma list of scenario ids to run (default: all)
//	XBIN_REAL_TILE_AGENT   "real" (default): tile agents are the rootfs's coding
//	                       agents on the connector, doing real work in the
//	                       synthetic tiles; "fake": the scripted one, in
//	                       Invoices (cheaper, and a model that checks the
//	                       tile agent's claims finds them false — which
//	                       derails the scenarios after it)
//
// The grade it writes is mechanical — which tools the model called, what
// reached xbind, a few words of its answer — and each row names what it
// looked at. Read the transcript beside it before quoting a grade: wording,
// tone and "did it claim more than it checked" are a reader's call.
//
// The key is read by xbind from the file (the host file's keyFile); this
// test never opens it except to make sure nothing it writes contains it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	realMaxCalls = 60 // a scenario past this many model calls is stopped
	realUp       = "real"
	realUpHosted = "hosted" // the same endpoint, not marked private: a "hosted" model to switch to
)

type realCfg struct {
	base, model, keyFile, out, addr string
	quota                           int64
	only                            map[string]bool
	fakeAgent                       bool // tile agents are the scripted one
}

func realConfig(t *testing.T) realCfg {
	c := realCfg{base: os.Getenv("XBIN_REAL_LLM_BASE"), model: os.Getenv("XBIN_REAL_LLM_MODEL"), keyFile: os.Getenv("XBIN_REAL_LLM_KEYFILE"),
		out: os.Getenv("XBIN_REAL_OUT"), addr: os.Getenv("XBIN_REAL_ADDR"), quota: 3_000_000}
	if c.base == "" {
		t.Skip("no XBIN_REAL_LLM_BASE: the real-model run is manual (hack/wsagent-real-run.sh)")
	}
	if c.model == "" || c.keyFile == "" || c.out == "" {
		t.Fatal("XBIN_REAL_LLM_MODEL, XBIN_REAL_LLM_KEYFILE and XBIN_REAL_OUT are needed with XBIN_REAL_LLM_BASE")
	}
	if q := os.Getenv("XBIN_REAL_QUOTA"); q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n <= 0 {
			t.Fatalf("XBIN_REAL_QUOTA %q: a number of tokens", q)
		}
		c.quota = n
	}
	c.fakeAgent = os.Getenv("XBIN_REAL_TILE_AGENT") == "fake"
	if s := os.Getenv("XBIN_REAL_SCENARIOS"); s != "" {
		c.only = map[string]bool{}
		for _, id := range strings.Split(s, ",") {
			c.only[strings.TrimSpace(id)] = true
		}
	}
	if err := os.MkdirAll(filepath.Join(c.out, "transcripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	return c
}

// realEnv is the rig: a production xbind on the real upstream.
type realEnv struct {
	*wsEnv
	cfg   realCfg
	model string // the connector's name of the model: real/<upstream id>
	rows  []realRow
	key   string // only to check that nothing written holds it
	// stepsFile is what the scripted coding agent does with a brief that
	// carries no script of its own (acptest's --steps-file): the stand-in
	// for a real coding agent's work in the scenarios that need one to
	// behave a certain way.
	stepsFile string
}

// steps sets what the scripted coding agent does next ("" = it echoes).
func (e *realEnv) steps(cmds ...string) {
	e.t.Helper()
	if !e.cfg.fakeAgent {
		return // a real coding agent does the brief's work
	}
	if err := os.WriteFile(e.stepsFile, []byte(strings.Join(cmds, " ||| ")+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// permissions answers every permission request of person's tile agents
// with option ("once", "always", "no") until none is pending for a
// moment; it returns how many it answered.
func (e *realEnv) permissions(person, option string, wait time.Duration) int {
	e.t.Helper()
	n, quiet := 0, 0
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) && quiet < 4 {
		answered := false
		for _, s := range e.tileAgents(person) {
			if s.Pending == 0 {
				continue
			}
			var detail struct {
				Permissions []struct {
					Pid     string
					Options []struct {
						OptionID, Kind string
						Explicit       bool
					}
				}
			}
			e.d.Must(e.t, "GET", "/api/xbin/term/sessions/"+s.ID, nil, 200, e.as(person)...).Decode(e.t, &detail)
			for _, p := range detail.Permissions {
				// the option of the kind asked for, as the agent names it (a real one's ids are its own)
				want, id := map[string]string{"once": "allow_once", "always": "allow_always", "no": "reject_once"}[option], ""
				for _, o := range p.Options {
					if o.Kind == want && !o.Explicit && id == "" {
						id = o.OptionID
					}
				}
				for _, o := range p.Options { // it offers no "always" for this request: once
					if id == "" && o.Kind == "allow_once" && option != "no" {
						id = o.OptionID
					}
				}
				if id == "" {
					id = option
				}
				if r := e.d.Call(e.t, "POST", "/api/xbin/term/sessions/"+s.ID+"/permissions/"+p.Pid, map[string]any{"optionId": id}, e.as(person)...); r.Status == 200 {
					n, answered = n+1, true
				}
			}
		}
		if answered {
			quiet = 0
		} else {
			quiet++
		}
		time.Sleep(500 * time.Millisecond)
	}
	return n
}

// work answers a real tile agent's permission requests ("always") until
// it is at rest — idle, with nothing pending — or the time is up; with
// the scripted agent it answers what its script asks. It returns how many
// answers the person gave.
func (e *realEnv) work(person string, timeout time.Duration) int {
	e.t.Helper()
	if e.cfg.fakeAgent {
		return e.permissions(person, "once", 40*time.Second)
	}
	n, rest := 0, 0
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && rest < 6 {
		n += e.permissions(person, "always", 3*time.Second)
		busy := false
		for _, s := range e.tileAgents(person) {
			busy = busy || s.Status != "idle" || s.Pending > 0
		}
		if busy {
			rest = 0
		} else {
			rest++
		}
	}
	return n
}

// waiting waits until a tile agent of person's waits on a permission.
func (e *realEnv) waiting(person string, timeout time.Duration) bool {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, s := range e.tileAgents(person) {
			if s.Pending > 0 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// approveAll approves person's pending requests (each once) and returns
// how many there were.
func (e *realEnv) approveAll(person string) int {
	e.t.Helper()
	n := 0
	for _, r := range e.requests(person) {
		if r.State == "pending" {
			e.d.Must(e.t, "POST", "/api/xbin/delegate/requests/"+r.ID+"/approve", map[string]any{"grant": "once"}, 200, e.as(person)...)
			n++
		}
	}
	return n
}

// endAgents cancels and closes person's tile agent sessions, so the next
// scenario starts with none.
func (e *realEnv) endAgents(person string) {
	e.t.Helper()
	for _, s := range e.tileAgents(person) {
		e.d.Call(e.t, "DELETE", "/api/xbin/term/sessions/"+s.ID, nil, e.as(person)...)
	}
}

// realRow is one graded scenario.
type realRow struct {
	ID, Name, Grade, Why string
	Calls                int64
	In, Out              int64
	Wall                 time.Duration
}

// startReal boots the rig. fakeTile is where the scripted coding agent's
// binary is planted (the scenarios that need a tile agent to behave a
// certain way use it; the benchmark uses a real one).
func startReal(t *testing.T, fakeTile string) *realEnv {
	t.Helper()
	cfg := realConfig(t)
	a := xbindtest.Require(t)
	if a.Gocryptfs == "" {
		xbindtest.Skip(t, "no gocryptfs: people's partitions keep their data in encrypted volumes")
	}
	a = a.WithSealedBuilds(t)
	dir, err := os.MkdirTemp("", "xbindtest-wsreal-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { xbindtest.RemoveTree(dir) })
	ws := filepath.Join(dir, "ws")
	acp := filepath.Join(ws, fakeTile, ".bin", "fakeacp")
	keyFile, err := filepath.Abs(cfg.keyFile)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("the key file: %v", err)
	}
	// the host's file: the endpoint twice — once marked private (the model
	// sessions run on), once not (a "hosted" model for the model-rule
	// scenarios) — and a per-person quota, so a loop can't run away
	host := map[string]any{
		"upstreams": map[string]any{
			realUp:       map[string]any{"baseURL": cfg.base, "keyFile": keyFile, "models": []string{cfg.model}, "private": true},
			realUpHosted: map[string]any{"baseURL": cfg.base, "keyFile": keyFile, "models": []string{cfg.model}},
		},
		"defaults": map[string]any{"model": realUp + "/" + cfg.model},
		"quotas":   map[string]any{"default": map[string]any{"tokensPerDay": cfg.quota}},
	}
	hb, _ := json.MarshalIndent(host, "", " ")
	hostFile := filepath.Join(dir, "llm.jsonc")
	if err := os.WriteFile(hostFile, append(hb, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Production: true, WS: ws, Addr: cfg.addr,
		Env: []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault", "XBIN_AGENT_FAKE=" + acp + " --steps-file=" + acp + ".steps", "XBIN_LLM_CONFIG=" + hostFile}})
	if err := os.MkdirAll(filepath.Dir(acp), 0o755); err != nil {
		t.Fatal(err)
	}
	build := execCommand(a.Repo, "go", "build", "-o", acp, "./hack/fakeacp")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hack/fakeacp: %v\n%s", err, out)
	}
	e := &realEnv{wsEnv: &wsEnv{sealedEnv: &sealedEnv{d: d, sess: map[string]string{}, frames: map[string]xbindtest.Header{}}, t: t, signedIn: map[string]time.Time{}},
		cfg: cfg, model: realUp + "/" + cfg.model, key: strings.TrimSpace(string(kb)), stepsFile: acp + ".steps"}
	t.Cleanup(func() { e.writeTable(t) })
	return e
}

// --- a turn with a model that isn't scripted ---------------------------------------------

// realTurn is what one turn left in the conversation.
type realTurn struct {
	msgs   []wsMsg
	Answer string // the last assistant text
	Status string // the run's status when it settled
}

// calls are the turn's tool calls, in order: "name {args}".
func (tn realTurn) calls() []string {
	var out []string
	for _, m := range tn.msgs {
		for _, tc := range m.ToolCalls {
			out = append(out, tc.Function.Name+" "+tc.Function.Arguments)
		}
	}
	return out
}

// n counts the turn's calls of a tool.
func (tn realTurn) n(tool string) int {
	n := 0
	for _, c := range tn.calls() {
		if strings.HasPrefix(c, tool+" ") {
			n++
		}
	}
	return n
}

// said is every assistant text of the turn, joined.
func (tn realTurn) said() string {
	var b strings.Builder
	for _, m := range tn.msgs {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			b.WriteString(m.Content + "\n")
		}
	}
	return b.String()
}

// results are the turn's tool results of a tool, in order.
func (tn realTurn) results(tool string) []string {
	var out []string
	for _, m := range tn.msgs {
		if m.Role == "tool" && m.Name == tool {
			out = append(out, m.Content)
		}
	}
	return out
}

// status is the session's run status and what it waits on.
func (c *wsConv) status() (status, waiting string) {
	c.e.t.Helper()
	var out struct {
		Sessions []struct {
			ID              int64
			Status, Waiting string
		}
	}
	c.e.d.Must(c.e.t, "GET", wsAPI+"/ws/sessions", nil, 200, c.fr).Decode(c.e.t, &out)
	for _, s := range out.Sessions {
		if fmt.Sprint(s.ID) == c.run {
			return s.Status, s.Waiting
		}
	}
	return "", ""
}

// settle waits until the conversation is at rest after message id `after`:
// its run neither running nor queued, twice in a row, with something new
// said (or a failure). It returns what the turn left.
func (c *wsConv) settle(after int64, timeout time.Duration) realTurn {
	c.e.t.Helper()
	deadline := time.Now().Add(timeout)
	var turn realTurn
	quiet := 0
	for {
		st, _ := c.status()
		turn.msgs = wsAfter(c.messages(), after)
		turn.Status = st
		spoke := false
		for _, m := range turn.msgs {
			if m.Role == "assistant" {
				spoke = true
				if strings.TrimSpace(m.Content) != "" {
					turn.Answer = m.Content
				}
			}
		}
		busy := st == "running" || st == "queued" || st == "awaiting"
		if !busy && (spoke || st == "error" || st == "waiting_input") {
			quiet++
		} else {
			quiet = 0
		}
		if quiet >= 3 {
			return turn
		}
		if time.Now().After(deadline) {
			c.e.t.Errorf("%s's conversation %s did not settle within %s (status %s):%s", c.person, c.run, timeout, st, wsTail(turn.msgs, 8))
			return turn
		}
		time.Sleep(700 * time.Millisecond)
	}
}

// tell sends the person's message and waits for the turn to settle.
func (c *wsConv) tell(text string) realTurn {
	c.e.t.Helper()
	after := c.last()
	c.e.d.Must(c.e.t, "POST", wsAPI+"/runs/"+c.run+"/message", map[string]any{"text": text}, 200, c.fr)
	return c.settle(after, 6*time.Minute)
}

// wake waits for the conversation to be woken after `after` (a decision,
// a tile agent's end) and to settle again.
func (c *wsConv) wake(after int64, timeout time.Duration) realTurn {
	c.e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range wsAfter(c.messages(), after) {
			if m.Role == "user" && strings.HasPrefix(m.Content, "[workspace updates") {
				return c.settle(after, timeout)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return realTurn{msgs: wsAfter(c.messages(), after), Status: "no-wake"}
}

// --- usage, transcripts, the table -------------------------------------------------------

type realUsage struct{ Requests, In, Out int64 }

// usage is everything the connector metered so far (the owner reads it).
func (e *realEnv) usage() realUsage {
	e.t.Helper()
	var out struct {
		Totals struct{ Requests, InputTokens, OutputTokens int64 }
	}
	e.d.Must(e.t, "GET", "/api/xbin/llm/usage", nil, 200).Decode(e.t, &out)
	return realUsage{out.Totals.Requests, out.Totals.InputTokens, out.Totals.OutputTokens}
}

var realSecretRE = regexp.MustCompile(`\b(xbl_|xbs_|xbt_|sk-)[A-Za-z0-9_\-]{8,}|Bearer [A-Za-z0-9_\-\.]{12,}`)

// redact takes credentials out of what is kept: the upstream's key, should
// it ever appear, and anything shaped like a token of xbind's.
func (e *realEnv) redact(s string) string {
	if e.key != "" {
		s = strings.ReplaceAll(s, e.key, "[redacted-key]")
	}
	return realSecretRE.ReplaceAllString(s, "[redacted]")
}

// transcriptOf writes a conversation as a person would read it plus what
// the model called and was answered.
func (e *realEnv) transcriptOf(c *wsConv) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s's session %s (model %s)\n\n", c.person, c.run, c.model)
	for _, m := range c.messages() {
		text := m.Content
		switch m.Role {
		case "tool":
			if len(text) > 6000 {
				text = text[:6000] + fmt.Sprintf("… [%d more bytes]", len(m.Content)-6000)
			}
			fmt.Fprintf(&b, "  ← %s: %s\n\n", m.Name, strings.ReplaceAll(text, "\n", "\n     "))
		case "assistant":
			if strings.TrimSpace(text) != "" {
				fmt.Fprintf(&b, "AGENT: %s\n\n", text)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "  → %s %s\n", tc.Function.Name, tc.Function.Arguments)
			}
			if len(m.ToolCalls) > 0 {
				b.WriteString("\n")
			}
		case "user":
			who := "PERSON"
			if strings.HasPrefix(text, "[workspace updates") {
				who = "XBIN"
			}
			fmt.Fprintf(&b, "%s: %s\n\n", who, text)
		default:
			fmt.Fprintf(&b, "(%s) %s\n\n", m.Role, text)
		}
	}
	if raw, err := json.MarshalIndent(c.cards(), "", " "); err == nil {
		fmt.Fprintf(&b, "### cards\n%s\n", raw)
	}
	return e.redact(b.String())
}

// scenario runs one: fn drives it and returns the grade and its reason.
func (e *realEnv) scenario(t *testing.T, id, name string, fn func(t *testing.T) (grade, why string, convs []*wsConv)) {
	if e.cfg.only != nil && !e.cfg.only[id] {
		return
	}
	t.Run(id+" "+name, func(t *testing.T) {
		e.in(t)
		before, start := e.usage(), time.Now()
		grade, why, convs := fn(t)
		// nothing of this scenario waits into the next: what its agent still asks for is denied
		for person := range e.sess {
			for _, r := range e.requests(person) {
				if r.State == "pending" {
					e.d.Call(t, "POST", "/api/xbin/delegate/requests/"+r.ID+"/deny", map[string]any{}, e.as(person)...)
				}
			}
		}
		row := realRow{ID: id, Name: name, Grade: grade, Why: why, Wall: time.Since(start)}
		after := e.usage()
		row.Calls, row.In, row.Out = after.Requests-before.Requests, after.In-before.In, after.Out-before.Out
		if row.Calls > realMaxCalls {
			row.Why += fmt.Sprintf(" (over the cap: %d model calls)", row.Calls)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# Scenario %s — %s\n\ngrade (mechanical): %s — %s\nmodel calls %d, tokens in %d out %d, wall %s\n\n", id, name, grade, why, row.Calls, row.In, row.Out, row.Wall.Round(time.Second))
		for _, c := range convs {
			if c != nil {
				b.WriteString(e.transcriptOf(c) + "\n")
			}
		}
		file := filepath.Join(e.cfg.out, "transcripts", "s"+id+".md")
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			t.Error(err)
		}
		e.record(row)
		t.Logf("scenario %s: %s — %s (%d calls, %d+%d tokens, %s) → %s", id, grade, why, row.Calls, row.In, row.Out, row.Wall.Round(time.Second), file)
	})
}

// record keeps a graded row: in this run's table and in rows.jsonl, which
// later boots of the same run (other scenarios, a scenario run again) add
// to — the table is written from all of it, the newest row of each id.
func (e *realEnv) record(row realRow) {
	e.rows = append(e.rows, row)
	b, _ := json.Marshal(row)
	f, err := os.OpenFile(filepath.Join(e.cfg.out, "rows.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		e.t.Error(err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append([]byte(e.redact(string(b))), '\n'))
}

// writeTable writes the graded table, table.md, from rows.jsonl.
func (e *realEnv) writeTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(e.cfg.out, "rows.jsonl"))
	if err != nil {
		return
	}
	byID := map[string]realRow{}
	for _, line := range strings.Split(string(raw), "\n") {
		var r realRow
		if json.Unmarshal([]byte(line), &r) == nil && r.ID != "" {
			byID[r.ID] = r
		}
	}
	e.rows = e.rows[:0]
	for _, r := range byID {
		e.rows = append(e.rows, r)
	}
	sort.SliceStable(e.rows, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimRight(e.rows[i].ID, "ab"))
		b, _ := strconv.Atoi(strings.TrimRight(e.rows[j].ID, "ab"))
		return a < b
	})
	var b strings.Builder
	var calls, in, out int64
	fmt.Fprintf(&b, "| # | Scenario | Grade | Why (mechanical) | Model calls | Tokens in | Tokens out | Wall |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range e.rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %d | %d | %s |\n", r.ID, r.Name, r.Grade, strings.ReplaceAll(r.Why, "|", "\\|"), r.Calls, r.In, r.Out, r.Wall.Round(time.Second))
		calls, in, out = calls+r.Calls, in+r.In, out+r.Out
	}
	fmt.Fprintf(&b, "\nTotal: %d model calls, %d tokens in, %d tokens out (model %s).\n", calls, in, out, e.model)
	if err := os.WriteFile(filepath.Join(e.cfg.out, "table.md"), []byte(e.redact(b.String())), 0o644); err != nil {
		t.Error(err)
	}
}

// has reports whether s holds any of the words (case-insensitive).
func has(s string, words ...string) bool {
	s = strings.ToLower(s)
	return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(s, strings.ToLower(w)) })
}

// grade3 turns checks into a grade: every must → pass; a must failed →
// fail; only shoulds failed → partial. why names what failed.
func grade3(must, should map[string]bool) (string, string) {
	var bad, soft []string
	for k, ok := range must {
		if !ok {
			bad = append(bad, k)
		}
	}
	for k, ok := range should {
		if !ok {
			soft = append(soft, k)
		}
	}
	sort.Strings(bad)
	sort.Strings(soft)
	switch {
	case len(bad) > 0:
		return "fail", "not: " + strings.Join(append(bad, soft...), "; ")
	case len(soft) > 0:
		return "partial", "not: " + strings.Join(soft, "; ")
	}
	return "pass", "every check held"
}
