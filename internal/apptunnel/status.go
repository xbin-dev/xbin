package apptunnel

import (
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar"
)

// Alert is one admin alert about the tunnel (boot hands them to the
// broker's AdminAlerts).
type Alert struct {
	Level   string // "warn" | "crit"
	Message string
}

// AlertKind is the alerts' kind.
const AlertKind = "app-tunnel"

// downAfter: a tunnel on but not running this long is worth an alert.
const downAfter = 2 * time.Minute

// Alerts: crit while the workspace requires the tunnel and it is down
// (every app is locked out); warn after three failed starts or two
// minutes down.
func (s *Service) Alerts() []Alert {
	st := s.runner.Status()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled {
		return nil
	}
	up := st.State == sidecar.StateRunning || st.State == sidecar.StateDegraded
	now := s.opt.Now()
	switch {
	case up:
		s.downSince = time.Time{}
		return nil
	case s.downSince.IsZero():
		s.downSince = now
	}
	why := st.Error
	if why == "" {
		why = string(st.State)
	}
	if s.cfg.TunnelOnly {
		return []Alert{{Level: "crit", Message: "the app tunnel is down and this workspace refuses the xbin app off it — every app is locked out: " + why}}
	}
	if st.Restarts >= 3 || now.Sub(s.downSince) >= downAfter || st.State == sidecar.StateUnavailable {
		return []Alert{{Level: "warn", Message: "the app tunnel is on but not running — apps paired with it can't connect: " + why}}
	}
	return nil
}
