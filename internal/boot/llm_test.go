package boot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// covers D226 — the LLM connector through a real boot, configured by the
// host's deployment file alone: the routes are mounted behind xbind's own
// middleware, a streamed call reaches the client while the upstream still
// holds the rest (nothing between the proxy and the socket buffers it), it
// is metered, what the file sets is locked, a tile that isn't bound is
// refused, and the host's key appears in no answer.
func TestLLMInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	const hostKey = "sk-host-BOOT-SECRET-0123456789"
	hold := make(chan struct{})
	var auths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n")
		fl.Flush()
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "llm.key"), []byte(hostKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostFile := filepath.Join(dir, "llm.jsonc")
	if err := os.WriteFile(hostFile, []byte(`{
		// the host's
		"upstreams": {"house": {"baseURL": "`+up.URL+`/v1", "keyFile": "llm.key", "models": ["gpt-5"]}},
		"defaults": {"model": "house/gpt-5"},
		"quotas": {"default": {"tokensPerDay": 1000000}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(ws, ln)
	cfg.LLMConfig = hostFile
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	defer func() {
		cancel()
		select {
		case <-done:
			noneLeftBehind(t, ws) // a stopped xbind leaves no backend running (leftbehind_test.go)
		case <-time.After(30 * time.Second):
			t.Error("boot did not stop")
		}
	}()
	var base string
	select {
	case addr := <-ready:
		base = "http://" + addr + "/api/xbin"
	case err := <-done:
		t.Fatalf("boot ended: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("boot never served")
	}
	call := func(method, path string, hdr map[string]string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if bytes.Contains(b, []byte(hostKey)) {
			t.Errorf("%s %s answered the host's key", method, path)
		}
		return resp.StatusCode, b
	}

	// booted from the file: configured, its settings locked
	code, body := call("GET", "/llm/config", nil, nil)
	var view struct {
		Configured bool                      `json:"configured"`
		Locks      map[string]map[string]any `json:"locks"`
		Host       struct{ Active bool }     `json:"host"`
		Vault      string                    `json:"vault"`
	}
	if err := json.Unmarshal(body, &view); err != nil || code != 200 || !view.Configured || !view.Host.Active || view.Vault != "plaintext" ||
		view.Locks["upstreams.house"]["reason"] != "set by your host" || view.Locks["defaults.model"]["locked"] != true || view.Locks["quotas.default"] == nil {
		t.Fatalf("the booted configuration: %d %s (%v)", code, body, err)
	}
	if code, body := call("PUT", "/llm/config", nil, map[string]any{"defaults": map[string]any{"model": "house/other"}}); code != 409 || !strings.Contains(string(body), "set by your host") {
		t.Errorf("changing a locked setting: %d %s", code, body)
	}
	if code, body := call("DELETE", "/llm/upstreams/house", nil, nil); code != 409 {
		t.Errorf("removing the host's upstream: %d %s", code, body)
	}

	// a streamed call, through the whole server: the first chunk arrives while the upstream holds the rest
	req, _ := http.NewRequest("POST", base+"/llm/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if resp.StatusCode != 200 || !strings.Contains(line, "first") {
			close(hold)
			t.Fatalf("the stream's start: %d %q", resp.StatusCode, line)
		}
	case <-time.After(10 * time.Second):
		close(hold)
		t.Fatal("the first chunk did not arrive while the upstream held the stream: something buffers")
	}
	close(hold)
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), `"total_tokens":18`) || !strings.Contains(string(rest), "[DONE]") {
		t.Errorf("the stream's end: %q", rest)
	}
	if len(auths) != 1 || auths[0] != "Bearer "+hostKey {
		t.Errorf("the upstream was called with %q", auths)
	}
	// metered to the caller (--no-auth: the owner), under the default model's name
	var usage struct {
		Rows []struct {
			User, Model           string
			Requests, InputTokens int64
		}
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		_, body := call("GET", "/llm/usage", nil, nil)
		_ = json.Unmarshal(body, &usage)
		if len(usage.Rows) == 1 || time.Now().After(deadline) {
			break
		}
	}
	if len(usage.Rows) != 1 || usage.Rows[0].User != "owner" || usage.Rows[0].Model != "house/gpt-5" || usage.Rows[0].Requests != 1 || usage.Rows[0].InputTokens != 11 {
		t.Errorf("usage: %+v", usage.Rows)
	}

	// a tile that isn't bound is refused in OpenAI's shape; an LLM key nobody minted is a 401
	code, body = call("GET", "/frame-token?component=root", nil, nil)
	var tok struct{ Token string }
	if _ = json.Unmarshal(body, &tok); code != 200 || tok.Token == "" {
		t.Fatalf("frame token: %d %s", code, body)
	}
	code, body = call("POST", "/llm/v1/chat/completions", map[string]string{"X-XBin-Frame-Token": tok.Token}, map[string]any{"messages": []any{}})
	if code != 403 || !strings.Contains(string(body), `"code":"xbin_llm_not_bound"`) {
		t.Errorf("an unbound tile's frame: %d %s", code, body)
	}
	if code, body := call("GET", "/llm/v1/models", map[string]string{"Authorization": "Bearer xbl_nobody-minted-this"}, nil); code != 401 || !strings.Contains(string(body), `"invalid_api_key"`) {
		t.Errorf("an unknown LLM key: %d %s", code, body)
	}
	if code, _ := call("GET", "/whoami", map[string]string{"Authorization": "Bearer xbl_nobody-minted-this"}, nil); code != 401 {
		t.Errorf("an LLM key on another route: %d", code)
	}
	// the pickers offer xbin:llm now that it is configured
	if code, body := call("GET", "/llm", nil, nil); code != 200 || !strings.Contains(string(body), `"callable":true`) || !strings.Contains(string(body), `"ref":"xbin:llm"`) {
		t.Errorf("status: %d %s", code, body)
	}
}

// covers D226 — a deployment file that is invalid stops the boot, naming
// the variable.
func TestLLMHostFileStopsBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	bad := filepath.Join(t.TempDir(), "llm.json")
	if err := os.WriteFile(bad, []byte(`{"upstreams": {"x": {"baseURL": "ftp://nope"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := testConfig(ws, ln)
	cfg.LLMConfig = bad
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "XBIN_LLM_CONFIG") {
		t.Fatalf("Run with an invalid XBIN_LLM_CONFIG: %v", err)
	}
	// …and so does a grace that is no duration (on a workspace of its own:
	// the boot that just stopped half-way still holds the first one's store)
	ws2 := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws2); err != nil {
		t.Fatal(err)
	}
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	cfg = testConfig(ws2, ln2)
	cfg.LLMCancelGrace = "soon"
	if err := Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "XBIN_LLM_CANCEL_GRACE") {
		t.Fatalf("Run with an invalid XBIN_LLM_CANCEL_GRACE: %v", err)
	}
}
