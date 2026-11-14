package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers D226 — "LLM by xbin" is a provider of the openai service: a tile
// binds its llm slot to xbin:llm the way it binds to a provider tile; the
// binding resolves to the connector's address in the tile's env and frame
// meta, and is what LLMBound answers from. Binding it is a workspace
// admin's act, it is offered only once the connector is configured, and
// nothing else binds to it.
func TestLLMBuiltinProvider(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"xbin.json":            `{"schema":1}`,
		"apps/agent/xbin.json": `{"runtime":"go","interfaces":{"llm":{"kind":"http","service":"openai","multi":true},"mcp":{"kind":"http","service":"mcp","multi":true},"net":{"kind":"net"}}}`,
		"apps/chat/xbin.json":  `{"interfaces":{"model":{"kind":"http","service":"openai"}}}`,
		"apps/gw/xbin.json":    `{"runtime":"go","provides":{"openai":{"kind":"http","service":"openai","role":"writer"}},"expose":{"roles":{"writer":"call models"}}}`,
		"apps/plain/xbin.json": `{"runtime":"go"}`,
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Users = st
	admin := auth.Principal{Owner: true}
	bind := func(p auth.Principal, method, body string) (int, string) {
		t.Helper()
		w := call(t, b.apiBindingSet, p, method, "/bindings", body, nil)
		return w.Code, w.Body.String()
	}
	options := func() []string {
		t.Helper()
		var out struct {
			Pending []pendingBind `json:"pending"`
		}
		w := call(t, b.apiBindingsList, admin, "GET", "/bindings", "", nil)
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, pb := range out.Pending {
			for _, o := range pb.Options {
				ids = append(ids, pb.Component+"."+pb.Slot+"="+o.ID)
			}
		}
		return ids
	}

	// not configured: the pickers offer what they always offered
	if got := options(); slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, LLMRef) }) {
		t.Errorf("an unconfigured connector is offered: %v", got)
	}
	configured := false
	b.LLMOffered = func() bool { return configured }
	configured = true
	got := options()
	for _, want := range []string{"apps/agent.llm=xbin:llm", "apps/agent.llm=apps/gw", "apps/chat.model=xbin:llm"} {
		if !slices.Contains(got, want) {
			t.Errorf("options lack %s: %v", want, got)
		}
	}
	// after the provider tiles: a prompt preselects its first option, and that stays what it was
	if i, j := slices.Index(got, "apps/agent.llm=xbin:llm"), slices.Index(got, "apps/agent.llm=apps/gw"); j < 0 || i < j {
		t.Errorf("the connector comes before the provider tile: %v", got)
	}
	if nonAdmin := b.bindOptions("apps/agent", registry.Iface{Kind: "http", Service: "openai", Multi: true}, false); len(nonAdmin) != 2 ||
		nonAdmin[0].ID != "apps/gw" || nonAdmin[1].ID != LLMRef || !nonAdmin[1].Blocked {
		t.Errorf("for someone who can't bind it, the connector is last and greyed: %+v", nonAdmin)
	}
	if slices.Contains(got, "apps/agent.mcp=xbin:llm") || slices.Contains(got, "apps/agent.net=xbin:llm") {
		t.Errorf("the connector is offered for a slot that isn't openai: %v", got)
	}

	if b.LLMBound("apps/agent") || b.LLMBound("apps/plain") || b.LLMBound("no/such") {
		t.Error("bound before any binding")
	}
	// only openai slots take it
	for _, body := range []string{
		`{"component":"apps/agent","slot":"mcp","provider":"xbin:llm"}`,
		`{"component":"apps/agent","slot":"net","provider":"xbin:llm"}`,
	} {
		if code, msg := bind(admin, "POST", body); code != 400 {
			t.Errorf("%s: %d %s", body, code, msg)
		}
	}
	// a workspace admin binds it, beside a provider tile on a multi slot
	if code, msg := bind(admin, "POST", `{"component":"apps/agent","slot":"llm","providers":["apps/gw","xbin:llm"]}`); code != 200 {
		t.Fatalf("bind: %d %s", code, msg)
	}
	if code, msg := bind(admin, "POST", `{"component":"apps/chat","slot":"model","provider":"xbin:llm"}`); code != 200 {
		t.Fatalf("bind a single slot: %d %s", code, msg)
	}
	if !b.LLMBound("apps/agent") || !b.LLMBound("apps/chat") || !b.LLMBound("apps/agent/editor") || b.LLMBound("apps/gw") || b.LLMBound("apps/plain") {
		t.Errorf("LLMBound: agent %v, chat %v, a window of agent %v, gw %v, plain %v", b.LLMBound("apps/agent"), b.LLMBound("apps/chat"),
			b.LLMBound("apps/agent/editor"), b.LLMBound("apps/gw"), b.LLMBound("apps/plain"))
	}
	// what the tile sees: the connector's address, as a provider tile's
	c, _ := reg.Component("apps/agent")
	env := b.EnvFor(c)
	wantEnv := `XBIN_IFACE_LLM=[{"provider":"apps/gw","service":"openai","url":"http://xbin/api/apps/gw"},{"provider":"xbin:llm","service":"openai","url":"http://xbin/api/xbin/llm"}]`
	if !slices.Contains(env, wantEnv) {
		t.Errorf("env: %q\nwant %s", env, wantEnv)
	}
	chat, _ := reg.Component("apps/chat")
	if env := b.EnvFor(chat); !slices.Contains(env, "XBIN_IFACE_MODEL_URL=http://xbin/api/xbin/llm") {
		t.Errorf("a single slot's env: %q", env)
	}
	if m, _ := b.HTTPInterfaces("apps/chat")["model"].(map[string]string); m["url"] != "/api/xbin/llm" || m["service"] != "openai" {
		t.Errorf("the frame's meta: %v", b.HTTPInterfaces("apps/chat"))
	}
	// the binding grants no call of any tile
	if role, ok := b.httpBindingRole("apps/agent", LLMRef); ok {
		t.Errorf("the builtin reads as a tile grant: %s", role)
	}

	// an unbind takes effect at once
	if code, msg := bind(admin, "DELETE", `{"component":"apps/chat","slot":"model"}`); code != 200 {
		t.Fatalf("unbind: %d %s", code, msg)
	}
	if b.LLMBound("apps/chat") {
		t.Error("still bound after the unbind")
	}

	// not a workspace admin: refused, whatever else they may bind
	if _, err := st.Upsert(users.User{ID: "bob", Role: "user", Tiles: map[string]string{"apps/chat": "write"}}, "pw-of-bob-123456"); err != nil {
		t.Fatal(err)
	}
	bob := principalFor(t, st, "bob")
	code, msg := bind(bob, "POST", `{"component":"apps/chat","slot":"model","provider":"xbin:llm"}`)
	if code != 403 || !strings.Contains(msg, "workspace-admin act") {
		t.Errorf("a non-admin binding the connector: %d %s", code, msg)
	}
	if b.LLMBound("apps/chat") {
		t.Error("a refused bind took effect")
	}
}
