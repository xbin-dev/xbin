package boot

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/events"
	ingressPkg "github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/watch"
)

// serve opens the gateway socket, the optional ingress listener and the
// console listener, prints the login line, and serves until ctx is done.
func (st *State) serve(ctx context.Context) error {
	cfg, run, brk, px := st.Cfg, st.Run, st.Broker, st.Proxy
	handler := st.Server.Handler()

	// Gateway socket: element backends call other elements / xbin APIs here
	// with their instance tokens (plans/auth.md §3). Same handler, same auth.
	gwSock := filepath.Join(run.RunDir, "gateway.sock")
	_ = os.Remove(gwSock)
	gwLn, err := net.Listen("unix", gwSock)
	if err != nil {
		return fmt.Errorf("gateway socket: %w", err)
	}
	gwSrv := &http.Server{
		Handler: handler,
		// Reap idle keep-alive conns from backends' pooled clients (the
		// SDK's IdleConnTimeout is 90s — under this) without touching
		// long-lived streams (WS is hijacked; SSE is never idle).
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := gwSrv.Serve(gwLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("gateway serve", "err", err)
		}
	}()

	// Boot reconcile: stand up stream listeners + forward doors for existing
	// bindings before traffic arrives.
	st.reconcileIngress()
	go st.lapseHeldCredentials(ctx) // held credentials take effect after 24 h (partitionops.go)

	// The builtin HTTP terminator (plans/ingress.md ING-3): a SECOND listener
	// — public, unauthenticated traffic never shares the console socket. It
	// serves only published tile routes (Host-routed, path-allowlisted); TLS
	// is bring-your-own-cert or none (a Tailscale/LB/reverse-proxy front, or
	// the Traefik tile for public ACME).
	var iSrv *http.Server
	if cfg.IngressListen != "" {
		ingressHandler := &ingressPkg.HTTPHandler{
			Source: broker.IngressSourceRuntime, Lookup: brk.IngressLookup,
			Forward: func(w http.ResponseWriter, r *http.Request, rt ingressPkg.Route) {
				px.ForwardIngress(w, r, rt, false)
			},
		}
		iln, err := net.Listen("tcp", cfg.IngressListen)
		if err != nil {
			return fmt.Errorf("ingress listener: %w", err)
		}
		if (cfg.IngressCert == "") != (cfg.IngressKey == "") {
			return fmt.Errorf("ingress TLS needs BOTH --ingress-cert and --ingress-key")
		}
		if cfg.IngressCert != "" {
			tc, err := ingressTLSConfig(cfg.IngressCert, cfg.IngressKey)
			if err != nil {
				return fmt.Errorf("ingress TLS: %w", err)
			}
			iln = tls.NewListener(iln, tc)
		}
		// Hairpin flows into the builtin terminator dial its local address.
		brk.IngressHTTPAddr = localDialAddr(cfg.IngressListen)
		iSrv = &http.Server{
			Handler:           ingressHandler,
			IdleTimeout:       120 * time.Second,
			ReadHeaderTimeout: 30 * time.Second,
		}
		go func() {
			if err := iSrv.Serve(iln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("ingress serve", "err", err)
			}
		}()
		slog.Info("ingress listener up", "addr", cfg.IngressListen, "tls", cfg.IngressCert != "")
	}

	ln := cfg.Listener
	if ln == nil {
		if ln, err = net.Listen("tcp", cfg.Listen); err != nil {
			return err
		}
	}

	// The printed URL should be the one a human can actually open: prefer
	// the configured public address over the raw listen socket.
	printURL := st.baseURL
	if st.externalURL != "" {
		printURL = st.externalURL
	}
	out := cfg.Stdout
	if out == nil {
		out = io.Writer(os.Stdout)
	}
	if cfg.NoAuth {
		slog.Warn("auth disabled")
		fmt.Fprintf(out, "\n  xbin (no auth): %s\n\n", printURL)
	} else {
		fmt.Fprintf(out, "\n  xbin login URL:\n  %s/login?token=%s\n\n", printURL, st.Auth.OwnerTokenValue())
	}

	httpSrv := &http.Server{
		Handler:           handler,
		IdleTimeout:       120 * time.Second, // reap idle browser/CLI keep-alives
		ReadHeaderTimeout: 30 * time.Second,
	}
	if cfg.Ready != nil {
		cfg.Ready(ln.Addr().String())
	}
	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		_ = httpSrv.Close()
	}()
	err = httpSrv.Serve(ln)
	st.stopWatch() // first: a save's batch would otherwise rebuild what StopAll stops, or write after it
	if st.Term != nil {
		st.Term.FlushAgents() // open agent conversations become history, not losses (term/history.go)
	}
	run.GoVersions.Stop() // the D166 check's lists end with xbind; a first pass resumes on the next boot
	run.StopAll()
	if st.TileSbx != nil {
		st.TileSbx.StopAll("xbind shut down") // synced, 15 s in all; its exit would end them unsynced
	}
	_ = gwSrv.Close()
	if iSrv != nil {
		_ = iSrv.Close()
	}
	brk.Close() // the KV database's file lock, the cron scheduler, the disk monitor, the resources' decrypted views
	// Open tiles keep their login binding across the restart (auth/framegens.go).
	st.Auth.FlushGens()
	if st.Push != nil {
		st.Push.Close()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// stopWatch ends what workspace changes drive, at shutdown: the watcher
// closes — a debounced batch still pending is dropped — and the watch loop
// finishes the batch it holds (rescan, provisioning, go.work…) and returns;
// then the drift counts that batch may have started for paused tiles end
// (deployments.StopWorkTrees). After it nothing of theirs writes into the
// workspace: a boot test's TempDir cleanup once found the workspace filling
// again as it emptied it.
func (st *State) stopWatch() {
	if st.watcher == nil {
		return
	}
	_ = st.watcher.Close()
	if st.watchDone != nil {
		<-st.watchDone
	}
	if st.Deployments != nil {
		st.Deployments.StopWorkTrees()
	}
}

// watchLoop reacts to each batch of workspace changes: the tile-level work
// (rescan, provisioning, pending grants, ingress, deps, go.work), then a
// reload and a rebuild per changed tile's live reload target (liveroute.go).
// dp is the deployments plane, which answers which deployment a save drives
// (LiveReload, Primary); a tile without a record drives main, as today (D119d),
// and a tile whose live reload is paused drives nothing (WorkTreeMoved).
// refreshViews re-renders open restricted terminals' go.work after the root
// one (term.RefreshViews).
func watchLoop(w *watch.Watcher, reg *registry.Registry, hub *events.Hub, run *runner.Runner, brk *broker.Broker, dp *deployments.Plane, reconcileIngress, refreshViews func()) {
	for ev := range w.C {
		if err := reg.Rescan(); err != nil {
			slog.Warn("rescan", "err", err)
		}
		brk.Provision()
		brk.SweepNamespaces() // after Provision, never inside it: the plane's answers must exist
		brk.RefreshPending()  // new `uses` requests → notify approvers (D33)
		reconcileIngress()    // manifest exposes / bindings may have changed on disk
		for _, p := range deps.Reconcile(reg) {
			slog.Debug("deps", "problem", p)
		}
		if err := deps.GoWork(reg, deps.SDKPath()); err != nil {
			slog.Warn("go.work", "err", err)
		}
		refreshViews()
		reload, restart := changedComponents(reg, ev.Paths)
		routeBatch(reload, restart, dp, hub, run.ChangedDeployment)
		run.WakeAlwaysOn() // a new tile, or the flag added
	}
}

// changedComponents maps one batch of changed workspace paths to the
// components they belong to. Every one reloads (web frames and native
// runtimes alike); restart marks the ones whose backend must be rebuilt —
// all but those whose only change is their native UI entry, which no
// backend reads (registry.Component.NativeOnlyChange). A save changes the
// work tree, so it is judged by the work tree's manifest (its view), even
// while the registry's component describes a pinned primary; without a
// pinned primary the view is the component itself.
func changedComponents(reg *registry.Registry, paths []string) (reload map[string]*registry.Component, restart map[string]bool) {
	reload, restart = map[string]*registry.Component{}, map[string]bool{}
	for _, p := range paths {
		c, rest, ok := reg.Resolve(p)
		if !ok {
			continue
		}
		reload[c.Path] = c
		if wt, err := reg.View(c, registry.ViewCode{}); err != nil || !wt.NativeOnlyChange(rest) {
			restart[c.Path] = true
		}
	}
	return reload, restart
}

// reapZombies handles PID-1 duty: adopt and reap orphaned grandchildren.
// Our own exec.Cmd Waits race benignly (they get ECHILD and treat it as exit).
func reapZombies() {
	c := make(chan os.Signal, 16)
	signal.Notify(c, syscall.SIGCHLD)
	for range c {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}

// ingressTLSConfig serves the BYO cert pair, re-loading it when the cert
// file changes on disk (a cert renewed in place picks up on the next
// handshake — no restart).
func ingressTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	var mu sync.Mutex
	var cached *tls.Certificate
	var mtime time.Time
	load := func() (*tls.Certificate, error) {
		fi, err := os.Stat(certFile)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		if cached != nil && fi.ModTime().Equal(mtime) {
			return cached, nil
		}
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			if cached != nil {
				slog.Warn("ingress TLS reload failed; keeping previous cert", "err", err)
				return cached, nil
			}
			return nil, err
		}
		cached, mtime = &c, fi.ModTime()
		return cached, nil
	}
	if _, err := load(); err != nil { // validate at startup
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return load() },
	}, nil
}

// localDialAddr maps a listen address to the address local (hairpin) flows
// dial: wildcard hosts become loopback.
func localDialAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
