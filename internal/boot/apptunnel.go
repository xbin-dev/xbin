package boot

import (
	"log/slog"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/apptunnel"
	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/server"
)

// irohRunner is the iroh endpoint the app tunnel runs: the sidecar
// supervisor (internal/apptunnel/sidecar) — the binary XBIN_IROH names,
// else the one embedded in xbind, extracted and run in its sandbox (with
// --isolate, there or not at all, D78). A build without one reads
// "unavailable"; the tunnel's settings and routes work either way. A
// seam for tests.
var irohRunner = func(st *State) sidecar.Runner {
	return sidecar.New(sidecar.Options{RunDir: st.Run.RunDir, Workspace: st.WS, Isolated: st.Cfg.Isolate, Log: slog.Default()})
}

// setupAppTunnel starts the app tunnel (D204, internal/apptunnel): off until
// an admin turns it on. Its routes answer either way, and serve's tunnel
// listener serves the same handler as the console's.
func (st *State) setupAppTunnel(srv *server.Server) error {
	brk := st.Broker
	svc, err := apptunnel.New(apptunnel.Options{
		Dir: filepath.Join(st.WS, "data", "app-tunnel"), Users: st.Users, Auth: st.Auth, Hub: st.Hub,
		IsAdmin: brk.IsAdmin, Runner: irohRunner(st), EnvRelay: st.Cfg.IrohRelay,
		// a device a pairing replaced goes the way of any removed device
		// (its push registration, its tunnel): the broker's hook, as chained
		OnDeviceRemoved: func(user, device string) {
			if f := brk.OnDeviceRemoved; f != nil {
				f(user, device)
			}
		},
	})
	if err != nil {
		return err
	}
	st.AppTunnel = svc
	svc.Register(srv)
	prevRemoved := brk.OnDeviceRemoved
	brk.OnDeviceRemoved = func(user, device string) {
		if prevRemoved != nil {
			prevRemoved(user, device)
		}
		svc.DeviceRemoved(user, device) // its connections close, the sidecar forgets it
	}
	prevOut := brk.OnUserSignedOut
	brk.OnUserSignedOut = func(id string, deleted bool) {
		if prevOut != nil {
			prevOut(id, deleted)
		}
		svc.UserChanged(id, deleted) // a disabled or deleted person's devices leave the allowlist
	}
	prevAlerts := brk.AdminAlerts // a single func: chained, never overwritten
	brk.AdminAlerts = func() []broker.Alert {
		var out []broker.Alert
		if prevAlerts != nil {
			out = prevAlerts()
		}
		for _, a := range svc.Alerts() {
			out = append(out, broker.Alert{Level: a.Level, Kind: apptunnel.AlertKind, Message: a.Message, System: true})
		}
		return out
	}
	return nil
}
