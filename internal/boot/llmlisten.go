package boot

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// llmlisten.go — the LLM-only listener (D226, docs/llm.md "Sandboxes made
// by a sandbox manager"): the connector's model routes for LLM keys on a
// unix socket of its own, which a tile sandbox created with net.llm reaches
// at its gateway (tilesbx/netcfg.go) — the one thing of xbind's such a
// sandbox reaches, whatever its egress.

// llmSockPath is where the LLM-only listener's socket lives: beside the
// gateway's, in xbind's run directory, which no sandbox sees.
func (st *State) llmSockPath() string { return filepath.Join(st.Run.RunDir, "llm.sock") }

// llmSock is the socket a tile sandbox's relay forwards (tilesbx.Deps): ""
// until the listener is up.
func (st *State) llmSock() string {
	if p := st.llmListening.Load(); p != nil {
		return *p
	}
	return ""
}

// serveLLMOnly opens the LLM-only listener: the connector's
// OpenAI-compatible routes for LLM keys, nothing else, on a unix socket.
// A tile sandbox created with net.llm reaches it at its gateway
// (tilesbx/netcfg.go) — the one thing of xbind's such a sandbox reaches,
// whatever its egress. nil without a connector.
func (st *State) serveLLMOnly() (*http.Server, error) {
	if st.LLM == nil {
		return nil, nil
	}
	sock := st.llmSockPath()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(sock, 0o600)
	srv := &http.Server{Handler: st.LLM.KeyOnlyHandler(), IdleTimeout: 120 * time.Second, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("llm listener serve", "err", err)
		}
	}()
	st.llmListening.Store(&sock)
	return srv, nil
}
