package xbin

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
)

// SetSecret / DeleteSecret reach the component's OWN vault through the
// gateway with the instance token — the way a settings page stores a token
// (frames can't reach the vault API, D30).
func TestSetDeleteSecret(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	type call struct{ method, path, auth, body string }
	var mu sync.Mutex
	var calls []call
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(b)})
		mu.Unlock()
		if r.URL.Path == "/api/xbin/vault/apps/x/denied" {
			http.Error(w, `{"error":"no"}`, http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	t.Setenv("XBIN_COMPONENT", "apps/x")
	clientOnce = sync.Once{}
	defer func() { clientOnce = sync.Once{} }()

	if err := SetSecret("api-token-a b", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSecret("api-token-a b"); err != nil {
		t.Fatal(err)
	}
	if err := SetSecret("denied", "x"); err == nil {
		t.Fatal("a 403 must be an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("calls: %+v", calls)
	}
	put := calls[0]
	var body map[string]string
	_ = json.Unmarshal([]byte(put.body), &body)
	if put.method != "PUT" || put.path != "/api/xbin/vault/apps/x/api-token-a%20b" || put.auth != "Bearer tok" || body["value"] != "s3cret" {
		t.Fatalf("put: %+v", put)
	}
	if calls[1].method != "DELETE" || calls[1].path != put.path {
		t.Fatalf("delete: %+v", calls[1])
	}
}
