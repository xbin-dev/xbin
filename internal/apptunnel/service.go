// Package apptunnel is the xbin app's iroh tunnel on xbind's side (D204,
// native/spec/tunnel.md, docs/app-tunnel.md): the settings and keys, the
// sidecar's allowlist, the Noise responder over every stream the sidecar
// hands over, the pairing state machine (the confirmation code compared on
// both screens), the tunnel listener the HTTP server serves on with each
// connection marked as its device's (auth.Transport), and the admin and
// device routes. The iroh endpoint itself is internal/apptunnel/sidecar's
// (sidecar.Runner); Noise is internal/apptunnel/noise.
package apptunnel

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/noise"
	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/users"
)

// Limits on tunnel connections (plans/app-tunnel.md §5.1).
const (
	maxConnsPerDevice = 64
	maxConns          = 1024
	ackTimeout        = 10 * time.Second
	resyncEvery       = time.Minute
	deliverTimeout    = 30 * time.Second
)

// Options configure a Service.
type Options struct {
	Dir      string // data/app-tunnel
	Users    *users.Store
	Auth     *auth.Auth
	Hub      *events.Hub
	IsAdmin  func(auth.Principal) bool
	Runner   sidecar.Runner // nil: none in this build (state "unavailable")
	EnvRelay string         // XBIN_IROH_RELAY: overrides the admin's relay
	// OnDeviceRemoved runs for a device a pairing replaced (the broker's
	// hook: its push registration goes, as for any removal).
	OnDeviceRemoved func(userID, deviceID string)
	Now             func() time.Time
}

// Service is the app tunnel.
type Service struct {
	opt      Options
	runner   sidecar.Runner
	envRelay wire.Relay
	envSet   bool
	ln       *connListener
	ctx      context.Context
	cancel   context.CancelFunc
	window   time.Duration // the confirmation window (confirmWindow; tests shorten it)

	mu        sync.Mutex
	cfg       Config
	keys      *keys
	started   bool
	streams   <-chan sidecar.Stream
	conns     map[string]map[*tunnelConn]struct{} // device id → its live connections
	nconns    int
	allowed   map[string]string // device id → endpoint, as last told to the sidecar
	synced    map[string]uint64 // device id → the config generation it last fetched
	downSince time.Time         // enabled and not running since
	pairings  map[string]*pairing
	bySel     map[[noise.SelectorLen]byte]*pairing
	attempts  map[string][]time.Time // endpoint → recent pairing attempts
}

// New loads the settings and, when the tunnel is on, starts it.
func New(opt Options) (*Service, error) {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	cfg, err := loadConfig(opt.Dir)
	if err != nil {
		return nil, err
	}
	envRelay, envSet, err := envRelay(opt.EnvRelay)
	if err != nil {
		return nil, err
	}
	s := &Service{opt: opt, runner: opt.Runner, envRelay: envRelay, envSet: envSet, ln: newConnListener(),
		cfg: cfg, conns: map[string]map[*tunnelConn]struct{}{}, allowed: map[string]string{}, synced: map[string]uint64{},
		pairings: map[string]*pairing{}, bySel: map[[noise.SelectorLen]byte]*pairing{}, attempts: map[string][]time.Time{},
		window: confirmWindow}
	if s.runner == nil {
		s.runner = Unavailable()
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if opt.Auth != nil {
		opt.Auth.SetTunnelPolicy(s.Required)
	}
	s.mu.Lock()
	if cfg.Enabled {
		if err := s.startLocked(); err != nil {
			slog.Warn("app tunnel: not started", "err", err)
		}
	}
	s.mu.Unlock()
	go s.resyncLoop()
	return s, nil
}

// Listener is what the tunnel's HTTP server serves: each connection is a
// paired device's, after its Noise handshake (Transport tells whose).
func (s *Service) Listener() net.Listener { return s.ln }

// Enabled reports the tunnel on.
func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Enabled
}

// Required reports the workspace refusing the app's traffic off the tunnel
// (auth.SetTunnelPolicy reads it).
func (s *Service) Required() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Enabled && s.cfg.TunnelOnly
}

// Close stops the tunnel and its connections (xbind shutting down).
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	s.stopLocked()
	s.mu.Unlock()
	_ = s.ln.Close()
}

// startLocked (re)starts the sidecar with the current settings.
func (s *Service) startLocked() error {
	if s.keys == nil {
		k, err := loadOrCreateKeys(s.opt.Dir)
		if err != nil {
			return err
		}
		s.keys = &k
	}
	allow := s.allowLocked()
	err := s.runner.Start(s.ctx, sidecar.Config{Key: s.keys.iroh, Relay: s.relayLocked(), Allow: allow, Pairing: s.pairingOpenLocked()})
	s.started = true
	if s.downSince.IsZero() {
		s.downSince = s.opt.Now()
	}
	if ch := s.runner.Streams(); ch != nil && ch != s.streams {
		s.streams = ch
		go s.acceptLoop(ch)
	}
	return err
}

// stopLocked stops the sidecar and drops every tunnel connection.
func (s *Service) stopLocked() {
	if s.started {
		s.runner.Stop()
	}
	s.started, s.downSince = false, time.Time{}
	for _, cs := range s.conns {
		for c := range cs {
			go c.Close()
		}
	}
}

// relayLocked is the relay the sidecar runs with: the environment's, else
// the admin's.
func (s *Service) relayLocked() wire.Relay {
	if s.envSet {
		r := s.envRelay
		if r.Mode == RelayCustom && s.cfg.Relay.Mode == RelayCustom {
			r.Token = s.cfg.Relay.Token
		}
		return r
	}
	return s.cfg.wireRelay()
}

// allowLocked is the tunnel/1 allowlist: the EndpointIds of the paired
// devices of people who may sign in. It records what it told.
func (s *Service) allowLocked() []string {
	s.allowed = map[string]string{}
	seen := map[string]bool{}
	var out []string
	if s.opt.Users == nil {
		return out
	}
	for _, td := range s.opt.Users.TunnelDevices() {
		if td.Disabled || td.Device.Endpoint == "" {
			continue
		}
		s.allowed[td.Device.ID] = td.Device.Endpoint
		if !seen[td.Device.Endpoint] {
			seen[td.Device.Endpoint] = true
			out = append(out, td.Device.Endpoint)
		}
	}
	return out
}

// Resync tells the sidecar the allowlist again and waits for its ack, and
// closes the connections of devices no longer on it (removed, or their
// person disabled). Nothing while the tunnel is off.
func (s *Service) Resync(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	before := s.allowed
	allow := s.allowLocked()
	var gone []*tunnelConn
	var dropped []string
	for dev, cs := range s.conns {
		if _, ok := s.allowed[dev]; !ok {
			for c := range cs {
				gone = append(gone, c)
			}
		}
	}
	for dev, ep := range before {
		if _, ok := s.allowed[dev]; !ok && !contains(allow, ep) {
			dropped = append(dropped, ep)
		}
	}
	s.mu.Unlock()
	for _, c := range gone {
		c.Close()
	}
	for _, ep := range dropped {
		s.runner.Drop(ep)
	}
	return s.runner.SetAllow(ctx, allow)
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// DeviceRemoved drops a removed device's tunnel: its connections close and
// its EndpointId leaves the allowlist (the broker's OnDeviceRemoved).
func (s *Service) DeviceRemoved(_, deviceID string) {
	s.mu.Lock()
	var cs []*tunnelConn
	for c := range s.conns[deviceID] {
		cs = append(cs, c)
	}
	delete(s.synced, deviceID)
	s.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
	ctx, cancel := context.WithTimeout(s.ctx, ackTimeout)
	defer cancel()
	if err := s.Resync(ctx); err != nil {
		slog.Warn("app tunnel: allowlist after a device removal", "device", deviceID, "err", err)
	}
	s.changed()
}

// UserChanged re-checks the allowlist (a person signed out everywhere,
// disabled or deleted: the broker's OnUserSignedOut).
func (s *Service) UserChanged(string, bool) {
	ctx, cancel := context.WithTimeout(s.ctx, ackTimeout)
	defer cancel()
	if err := s.Resync(ctx); err != nil {
		slog.Warn("app tunnel: allowlist resync", "err", err)
	}
}

// resyncLoop re-tells the allowlist once a minute: a device change that
// reached no hook (an edited users.json) still lands.
func (s *Service) resyncLoop() {
	t := time.NewTicker(resyncEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(s.ctx, ackTimeout)
			_ = s.Resync(ctx)
			cancel()
		}
	}
}

// changed publishes the bodiless app-tunnel event (admin consoles and
// pairing dialogs refresh).
func (s *Service) changed() {
	if s.opt.Hub != nil {
		s.opt.Hub.Publish(events.Event{Type: "app-tunnel"})
	}
}

// acceptLoop hands every stream to its ALPN's handler, never blocking on
// one.
func (s *Service) acceptLoop(ch <-chan sidecar.Stream) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case st, ok := <-ch:
			if !ok {
				return
			}
			switch st.Header.ALPN {
			case wire.ALPNTunnel:
				go s.serveTunnel(st)
			case wire.ALPNPair:
				go s.servePair(st)
			default:
				_ = st.Conn.Close()
			}
		}
	}
}

var errUnknownDevice = errors.New("app tunnel: not a paired device")

// deviceByKey finds the paired device holding a Noise static key.
func (s *Service) deviceByKey(peer [32]byte) (users.TunnelDevice, bool) {
	if s.opt.Users == nil {
		return users.TunnelDevice{}, false
	}
	key := base64.RawURLEncoding.EncodeToString(peer[:])
	for _, td := range s.opt.Users.TunnelDevices() {
		if td.Device.TunnelKey == key {
			return td, true
		}
	}
	return users.TunnelDevice{}, false
}

// serveTunnel runs IK on a tunnel/1 stream and hands the connection to the
// HTTP server: only a paired device's key, from the EndpointId on its
// record, of a person who may sign in.
func (s *Service) serveTunnel(st sidecar.Stream) {
	s.mu.Lock()
	k := s.keys
	s.mu.Unlock()
	if k == nil {
		_ = st.Conn.Close()
		return
	}
	var dev users.TunnelDevice
	c, err := noise.Accept(st.Conn, noise.Config{Static: k.noise, ServerEndpoint: k.endpoint(), DeviceEndpoint: st.Header.Endpoint},
		func(peer [32]byte, _ []byte) ([]byte, error) {
			d, ok := s.deviceByKey(peer)
			if !ok || d.Disabled || d.Device.Endpoint != st.Header.EndpointHex() {
				return nil, errUnknownDevice
			}
			dev = d
			return nil, nil
		})
	if err != nil {
		slog.Debug("app tunnel: handshake refused", "endpoint", st.Header.EndpointHex(), "err", err)
		_ = st.Conn.Close()
		return
	}
	tc := &tunnelConn{Conn: c, svc: s, t: auth.Transport{Tunnel: true, DeviceID: dev.Device.ID, UserID: dev.UserID, Endpoint: dev.Device.Endpoint}}
	if !s.track(tc) {
		_ = c.Close()
		return
	}
	if !s.ln.deliver(tc) {
		_ = tc.Close()
	}
}

// track registers a connection under the limits.
func (s *Service) track(c *tunnelConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.allowed[c.t.DeviceID]; !ok && s.opt.Users != nil {
		// the allowlist hasn't caught up with a fresh pairing: it is told
		// before "enrolled" is sent, so this is a removed device
		if _, _, found := s.opt.Users.FindDevice(c.t.DeviceID); !found {
			return false
		}
	}
	cs := s.conns[c.t.DeviceID]
	if s.nconns >= maxConns || len(cs) >= maxConnsPerDevice {
		return false
	}
	if cs == nil {
		cs = map[*tunnelConn]struct{}{}
		s.conns[c.t.DeviceID] = cs
	}
	cs[c] = struct{}{}
	s.nconns++
	return true
}

func (s *Service) untrack(c *tunnelConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs := s.conns[c.t.DeviceID]; cs != nil {
		if _, ok := cs[c]; ok {
			delete(cs, c)
			s.nconns--
			if len(cs) == 0 {
				delete(s.conns, c.t.DeviceID)
			}
		}
	}
}

// tunnelConn is one device connection the HTTP server reads.
type tunnelConn struct {
	*noise.Conn
	svc  *Service
	t    auth.Transport
	once sync.Once
}

// Transport is whose connection this is.
func (c *tunnelConn) Transport() auth.Transport { return c.t }

func (c *tunnelConn) RemoteAddr() net.Addr { return tunnelAddr(c.t.ClientKey()) }

func (c *tunnelConn) Close() error {
	c.once.Do(func() { c.svc.untrack(c) })
	return c.Conn.Close()
}

type tunnelAddr string

func (a tunnelAddr) Network() string { return "app-tunnel" }
func (a tunnelAddr) String() string  { return string(a) }

// ConnContext is the tunnel HTTP server's: it marks every request of a
// connection as its device's. A connection that isn't a tunnel's (it
// can't be) is marked as a tunnel with no device, which the gate refuses.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if tc, ok := c.(interface{ Transport() auth.Transport }); ok {
		return auth.WithTransport(ctx, tc.Transport())
	}
	return auth.WithTransport(ctx, auth.Transport{Tunnel: true})
}

// connListener is the in-memory net.Listener the tunnel HTTP server
// serves.
type connListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newConnListener() *connListener {
	return &connListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

// deliver hands c to the HTTP server, or gives up (false) after a while
// when nothing serves the listener.
func (l *connListener) deliver(c net.Conn) bool {
	t := time.NewTimer(deliverTimeout)
	defer t.Stop()
	select {
	case l.ch <- c:
		return true
	case <-l.done:
	case <-t.C:
	}
	return false
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *connListener) Addr() net.Addr { return tunnelAddr("app-tunnel") }
