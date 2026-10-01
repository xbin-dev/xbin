package boot

// goversions.go — the upgrade check of D166 (runner/goversionscheck.go):
// what each Go tile links older now that it builds with its own go.mod, and
// the go.mod lines that keep what it had. Booted with the registry (before
// any build can run, so an earlier xbind's builds tell an upgrade from a
// fresh workspace), started last, in the background; its alert reaches
// admins only, and its routes are admins'.

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/server"
)

// goVersionsDelay is how long after boot the first pass waits: the boot's
// own builds (always-on backends, the first requests) go first.
// Config.GoVersionsDelay overrides it.
const goVersionsDelay = 30 * time.Second

const goVersionsDocs = "/docs/changes/2026-09-30-go-build-workspace.md"

// bootGoVersions reads the check's state and decides whether its first pass
// is due, before any build runs (stepRegistry).
func (st *State) bootGoVersions() {
	delay := st.Cfg.GoVersionsDelay
	if delay <= 0 {
		delay = goVersionsDelay
	}
	gv := &runner.GoVersions{Run: st.Run, Path: filepath.Join(st.WS, "data", "go-build-versions.json"),
		Version: st.Cfg.Version, Delay: delay}
	if err := gv.Boot(); err != nil {
		slog.Warn("go build versions: the check's state", "err", err)
	}
	st.Run.GoVersions = gv
}

// stepGoVersions starts the check's first pass when it is due.
func (st *State) stepGoVersions() error {
	if gv := st.Run.GoVersions; gv != nil {
		gv.Start()
	}
	return nil
}

// registerGoVersionsAPI mounts the check's admin routes and its alert.
func (st *State) registerGoVersionsAPI(srv *server.Server) {
	gv, brk := st.Run.GoVersions, st.Broker
	if gv == nil {
		return
	}
	brk.AdminAlerts = func() []broker.Alert {
		msg, ok := gv.Alert()
		if !ok {
			return nil
		}
		return []broker.Alert{{Level: "warn", Kind: runner.GoVersionsAlertKind, Message: msg, Dismiss: "/go-build-versions/dismiss"}}
	}
	admin := func(w http.ResponseWriter, r *http.Request) bool {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			server.WriteError(w, http.StatusForbidden, "admin only", goVersionsDocs)
			return false
		}
		return true
	}
	srv.RegisterAPI("GET /go-build-versions", func(w http.ResponseWriter, r *http.Request) {
		if admin(w, r) {
			server.WriteJSON(w, http.StatusOK, gv.Report())
		}
	})
	srv.RegisterAPI("POST /go-build-versions/check", func(w http.ResponseWriter, r *http.Request) {
		if !admin(w, r) {
			return
		}
		switch started, err := gv.CheckAll(); {
		case errors.Is(err, runner.ErrGoVersionsNothing):
			server.WriteJSON(w, http.StatusOK, map[string]any{"running": false, "started": false, "reason": err.Error()})
		case err != nil:
			server.WriteError(w, http.StatusServiceUnavailable, err.Error())
		default:
			server.WriteJSON(w, http.StatusAccepted, map[string]any{"running": true, "started": started})
		}
	})
	srv.RegisterAPI("POST /go-build-versions/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !admin(w, r) {
			return
		}
		var body struct {
			Tile string `json:"tile"`
		}
		if err := server.DecodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
			server.WriteError(w, http.StatusBadRequest, "bad body: "+err.Error(), "/docs/protocol.md")
			return
		}
		if err := gv.Dismiss(body.Tile); errors.Is(err, runner.ErrGoVersionsUnknownTile) {
			server.WriteError(w, http.StatusNotFound, err.Error(), goVersionsDocs)
			return
		} else if err != nil {
			server.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		server.WriteJSON(w, http.StatusOK, gv.Report())
	})
}
