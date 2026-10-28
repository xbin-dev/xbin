package apptunnel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/noise"
	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// Pairing (native/spec/tunnel.md §2, §5–§6): a signed-in person mints a
// tunnel code (the QR); the phone runs IKpsk2 with the code's selector and
// psk, says who it is, and both screens show the confirmation code; the
// minting session — the same person, the same login — answers "Codes
// match" or "They don't". States:
//
//	minted → handshaken → confirm → enrolled | rejected | abandoned | expired
//
// handshaken: the Noise handshake is done, the phone's first message is
// awaited (only it proves the psk, IKpsk2); confirm: the codes are shown.
// The code's 5 minutes run from the mint to the handshake, then a 2-minute
// window to confirm. A stream that ends before the confirmation abandons
// the pairing: the confirmation answers 409 and no device is made.

const (
	stMinted     = "minted"
	stHandshaken = "handshaken"
	stConfirm    = "confirm"
	stEnrolled   = "enrolled"
	stRejected   = "rejected"
	stAbandoned  = "abandoned"
	stExpired    = "expired"

	confirmWindow      = 2 * time.Minute
	helloTimeout       = 10 * time.Second
	pairingKeep        = 10 * time.Minute // a finished pairing still answers GET this long
	maxPairings        = 256
	maxPairingsPerUser = 8
	pairAttempts       = 10 // per endpoint per minute
)

// ErrTunnelOff is a pairing asked of a tunnel that isn't running.
var ErrTunnelOff = errors.New("the app tunnel isn't running — check it in the admin console (workspace → app)")

type pairing struct {
	id, code, userID, gen, origin string
	sel                           [noise.SelectorLen]byte
	psk                           []byte
	state                         string
	minted, expires               time.Time
	name, platform, publicKey     string
	noiseKey, endpoint            string
	sas                           string
	deviceID, replaced            string
	reason, errText               string
	deciding, sawLimit            bool
	finished                      time.Time
	decide                        chan decision
}

// decision is the confirmation's last word to the phone: a message, then
// close; result says whether it was written.
type decision struct {
	msg    []byte
	result chan error
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// MintPairing opens a pairing for p (a person's own login session) with the
// origin the device will sign: an auth tunnel code (never redeemable over
// POST /devices/enroll), a fresh psk, the link.
func (s *Service) MintPairing(p auth.Principal, origin string) (*server.TunnelMint, error) {
	if p.UserID == "" || p.Component != "" || !validSessionGen(p.Gen) {
		return nil, errors.New("pairing a device needs your own sign-in")
	}
	m, err := s.mintPairing(p, origin)
	if err != nil {
		return nil, err
	}
	// Answer once the sidecar admits pair/1: a pasted link (or a fast
	// phone) dials the moment it has the code. Outside the lock; a sidecar
	// slow to acknowledge still gets the code — a scanning person is slower.
	ctx, cancel := context.WithTimeout(context.Background(), pairSyncWait)
	defer cancel()
	if err := s.runner.Sync(ctx); err != nil {
		slog.Warn("app tunnel: the sidecar didn't confirm pairing was open", "err", err)
	}
	return m, nil
}

// pairSyncWait bounds MintPairing's wait for the sidecar's acknowledgement.
const pairSyncWait = 10 * time.Second

func (s *Service) mintPairing(p auth.Principal, origin string) (*server.TunnelMint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled || s.keys == nil {
		return nil, ErrTunnelOff
	}
	st := s.runner.Status()
	if st.Ticket == "" || (st.State != sidecar.StateRunning && st.State != sidecar.StateDegraded) {
		return nil, ErrTunnelOff
	}
	s.sweepLocked()
	n := 0
	for _, pr := range s.pairings {
		if pr.userID == p.UserID && pr.finished.IsZero() {
			n++
		}
	}
	if len(s.pairings) >= maxPairings || n >= maxPairingsPerUser {
		return nil, errors.New("too many pairings in progress — finish or wait for one")
	}
	code, exp, err := s.opt.Auth.MintEnrollCodeTunnel(p.UserID, origin)
	if err != nil {
		return nil, err
	}
	psk := make([]byte, noise.KeyLen)
	if _, err := rand.Read(psk); err != nil {
		return nil, err
	}
	pr := &pairing{id: randomID(), code: code, sel: noise.Selector(code), psk: psk, userID: p.UserID, gen: p.Gen,
		origin: origin, state: stMinted, minted: s.opt.Now(), expires: exp, decide: make(chan decision, 1)}
	s.pairings[pr.id] = pr
	s.bySel[pr.sel] = pr
	s.updatePairingLocked()
	q := "u=" + url.QueryEscape(origin) + "&c=" + code + "&tv=1&t=" + url.QueryEscape(st.Ticket) +
		"&k=" + s.keys.serverKey() + "&p=" + base64.RawURLEncoding.EncodeToString(psk)
	if r := s.relayLocked(); r.Token != "" {
		q += "&r=" + url.QueryEscape(r.Token)
	}
	required := s.cfg.TunnelOnly
	if required {
		q += "&rq=1"
	}
	return &server.TunnelMint{Pairing: pr.id, Code: code, URL: "xbin://enroll?" + q, Expires: exp, Required: required,
		Ticket: st.Ticket, ServerKey: s.keys.serverKey()}, nil
}

func validSessionGen(g string) bool { return len(g) > 2 && g[:2] == "s." }

// pairingOpenLocked: a pairing is waiting for a phone.
func (s *Service) pairingOpenLocked() bool {
	for _, pr := range s.pairings {
		if pr.finished.IsZero() {
			return true
		}
	}
	return false
}

// updatePairingLocked tells the sidecar whether pair/1 is admitted.
func (s *Service) updatePairingLocked() {
	if s.started {
		s.runner.SetPairing(s.pairingOpenLocked())
	}
}

// sweepLocked expires minted pairings past their code and forgets
// finished ones past pairingKeep.
func (s *Service) sweepLocked() {
	now := s.opt.Now()
	changed := false
	for id, pr := range s.pairings {
		if pr.state == stMinted && now.After(pr.expires) {
			s.finishLocked(pr, stExpired, "expired", "the code expired")
			changed = true
		}
		if !pr.finished.IsZero() && now.Sub(pr.finished) > pairingKeep {
			delete(s.pairings, id)
			delete(s.bySel, pr.sel)
		}
	}
	if changed {
		s.updatePairingLocked()
	}
}

// finishLocked ends a pairing in state with reason / text.
func (s *Service) finishLocked(pr *pairing, state, reason, text string) {
	pr.state, pr.reason, pr.errText, pr.finished = state, reason, text, s.opt.Now()
	delete(s.bySel, pr.sel)
}

// throttledLocked counts a pairing attempt from endpoint: false past the
// per-minute budget.
func (s *Service) throttledLocked(endpoint string) bool {
	now := s.opt.Now()
	recent := s.attempts[endpoint][:0]
	for _, t := range s.attempts[endpoint] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= pairAttempts {
		s.attempts[endpoint] = recent
		return true
	}
	s.attempts[endpoint] = append(recent, now)
	if len(s.attempts) > 4096 {
		for k, ts := range s.attempts {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= time.Minute {
				delete(s.attempts, k)
			}
		}
	}
	return false
}

type hello struct {
	V         int    `json:"v"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	PublicKey string `json:"publicKey"`
}

// servePair runs one pair/1 stream.
func (s *Service) servePair(st sidecar.Stream) {
	defer st.Conn.Close()
	s.mu.Lock()
	k := s.keys
	throttled := s.throttledLocked(st.Header.EndpointHex())
	s.mu.Unlock()
	if k == nil || throttled {
		return
	}
	var pr *pairing
	c, err := noise.Accept(st.Conn, noise.Config{Pair: true, Static: k.noise, ServerEndpoint: k.endpoint(), DeviceEndpoint: st.Header.Endpoint},
		func(_ [32]byte, payload []byte) ([]byte, error) {
			var sel [noise.SelectorLen]byte
			copy(sel[:], payload)
			s.mu.Lock()
			defer s.mu.Unlock()
			s.sweepLocked()
			p := s.bySel[sel]
			if p == nil || p.state != stMinted { // one claimant at a time
				return nil, errors.New("no pairing awaits that code")
			}
			p.state, pr = stHandshaken, p
			return p.psk, nil
		})
	if err != nil {
		slog.Debug("app tunnel: pairing handshake refused", "endpoint", st.Header.EndpointHex(), "err", err)
		s.unclaim(pr)
		return
	}
	defer c.Close()
	// the first message proves the psk (IKpsk2): until it decrypts, the
	// pairing goes back to waiting when this stream fails
	_ = c.SetReadDeadline(s.opt.Now().Add(helloTimeout))
	raw, err := c.ReadMessage()
	_ = c.SetReadDeadline(time.Time{})
	var h hello
	if err == nil {
		err = json.Unmarshal(raw, &h)
	}
	if err != nil || h.V != 1 {
		slog.Debug("app tunnel: pairing hello", "err", err)
		s.unclaim(pr)
		return
	}
	reject := func(reason, text string) {
		b, _ := json.Marshal(map[string]string{"type": "rejected", "reason": reason, "error": text})
		_ = c.WriteMessage(b)
		_ = c.CloseWrite()
	}
	_, pub, err := auth.ParseDeviceKey(h.PublicKey)
	if err != nil {
		s.mu.Lock()
		s.finishLocked(pr, stRejected, "refused", "the app sent an unusable key")
		s.updatePairingLocked()
		s.mu.Unlock()
		reject("refused", "the app sent an unusable key: "+err.Error())
		s.changed()
		return
	}
	peer := c.Session().PeerStatic()
	sum := noise.SASNumber(c.Session().HandshakeHash())
	s.mu.Lock()
	if !s.opt.Auth.ConsumeTunnelCode(pr.code, pr.userID) { // voided (sign-out-everywhere) or expired
		s.finishLocked(pr, stExpired, "expired", "the code expired or was withdrawn")
		s.updatePairingLocked()
		s.mu.Unlock()
		reject("expired", "the code expired or was withdrawn — show a new one")
		s.changed()
		return
	}
	pr.state, pr.expires = stConfirm, s.opt.Now().Add(s.window)
	pr.name, pr.platform, pr.publicKey = h.Name, h.Platform, pub
	pr.noiseKey, pr.endpoint = base64.RawURLEncoding.EncodeToString(peer[:]), st.Header.EndpointHex()
	pr.sas = fmt.Sprintf("%06d", sum)
	expires := pr.expires
	s.mu.Unlock()
	s.changed()
	aw, _ := json.Marshal(map[string]string{"type": "await", "expires": expires.UTC().Format(time.RFC3339)})
	// wait: the decision, the window's end, or the phone leaving
	gone := make(chan struct{})
	if err := c.WriteMessage(aw); err != nil {
		close(gone)
	} else {
		go func() {
			for {
				if _, err := c.ReadMessage(); err != nil {
					if err != io.EOF { // a half-close isn't leaving: it may still read
						close(gone)
					}
					return
				}
			}
		}()
	}
	timer := time.NewTimer(expires.Sub(s.opt.Now()))
	defer timer.Stop()
	for {
		select {
		case d := <-pr.decide:
			err := c.WriteMessage(d.msg)
			if err == nil {
				_ = c.CloseWrite() // the phone may hang up first: it has its answer
			}
			d.result <- err
			return
		case <-gone:
			if s.abandon(pr) {
				return
			}
			gone = nil // a confirmation is being decided: its write will fail
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.mu.Lock()
			if pr.deciding {
				s.mu.Unlock()
				continue
			}
			reason := "expired"
			if pr.sawLimit {
				reason = "limit"
			}
			s.finishLocked(pr, stExpired, reason, "nobody confirmed the code in time")
			s.updatePairingLocked()
			s.mu.Unlock()
			s.changed()
			reject(reason, "nobody confirmed the code in time — pair again")
			return
		}
	}
}

// unclaim puts a pairing whose handshake or first message failed back to
// waiting (another, genuine, phone may still use the code).
func (s *Service) unclaim(pr *pairing) {
	if pr == nil {
		return
	}
	s.mu.Lock()
	if pr.state == stHandshaken {
		pr.state = stMinted
	}
	s.mu.Unlock()
}

// abandon ends a pairing whose stream ended before the confirmation;
// false when a confirmation is under way (it finishes the pairing).
func (s *Service) abandon(pr *pairing) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pr.deciding || !pr.finished.IsZero() {
		return !pr.deciding
	}
	s.finishLocked(pr, stAbandoned, "", "the phone left before the code was confirmed")
	s.updatePairingLocked()
	go s.changed()
	return true
}

// pairingFor finds a pairing p minted: the same person and the same login.
func (s *Service) pairingFor(id string, p auth.Principal) *pairing {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	pr := s.pairings[id]
	if pr == nil || pr.userID != p.UserID || pr.gen != p.Gen || p.Component != "" {
		return nil
	}
	return pr
}

// PairingView is GET /devices/pairing/{id}.
type PairingView struct {
	State    string       `json:"state"`
	SAS      string       `json:"sas,omitempty"`
	Device   *DeviceHello `json:"device,omitempty"`
	Expires  int64        `json:"expires"`
	Reason   string       `json:"reason,omitempty"`
	Error    string       `json:"error,omitempty"`
	DeviceID string       `json:"deviceId,omitempty"`
	Replaced string       `json:"replaced,omitempty"`
	// While in confirm: whether "Codes match" would need a device to
	// replace (atLimit), and the person's limit and device count — so the
	// replace choice can show before anyone clicks.
	AtLimit *bool `json:"atLimit,omitempty"`
	Limit   *int  `json:"limit,omitempty"`
	Count   *int  `json:"count,omitempty"`
}

// DeviceHello is who the phone says it is.
type DeviceHello struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

func (s *Service) viewLocked(pr *pairing) PairingView {
	v := PairingView{State: pr.state, Expires: pr.expires.Unix(), Reason: pr.reason, Error: pr.errText,
		DeviceID: pr.deviceID, Replaced: pr.replaced}
	if pr.state == stConfirm {
		v.SAS = pr.sas
		if s.opt.Users != nil {
			lim, n := s.opt.Users.UserDeviceLimit(pr.userID), len(s.opt.Users.Devices(pr.userID))
			at := n >= lim
			v.AtLimit, v.Limit, v.Count = &at, &lim, &n
		}
	}
	if pr.name != "" || pr.platform != "" {
		v.Device = &DeviceHello{Name: pr.name, Platform: pr.platform}
	}
	return v
}

// ErrPairingGone is a confirmation of a pairing that is no longer waiting.
var ErrPairingGone = errors.New("this pairing is no longer waiting for a confirmation")

// Confirm decides a pairing in confirm: match false burns it ("They
// don't"); match true makes the device — replacing one of the person's
// when replace names it — tells the sidecar, then the phone. A
// *users.DeviceLimitError leaves it waiting (the person picks a device to
// replace).
func (s *Service) Confirm(ctx context.Context, pr *pairing, match bool, replace string) (PairingView, error) {
	s.mu.Lock()
	if pr.state != stConfirm || pr.deciding {
		v := s.viewLocked(pr)
		s.mu.Unlock()
		return v, ErrPairingGone
	}
	if !s.opt.Auth.CredentialLive(pr.gen, pr.userID) {
		s.mu.Unlock()
		return PairingView{}, ErrPairingGone
	}
	pr.deciding = true
	s.mu.Unlock()

	finish := func(state, reason, text string, msg map[string]any) (PairingView, error) {
		b, _ := json.Marshal(msg)
		res := make(chan error, 1)
		pr.decide <- decision{msg: b, result: res}
		var werr error
		select {
		case werr = <-res:
		case <-ctx.Done():
			werr = ctx.Err()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		pr.deciding = false
		if werr != nil && state == stEnrolled {
			s.finishLocked(pr, stAbandoned, "", "the phone left before it heard back")
			s.updatePairingLocked()
			go s.changed()
			return s.viewLocked(pr), ErrPairingGone
		}
		s.finishLocked(pr, state, reason, text)
		s.updatePairingLocked()
		go s.changed()
		return s.viewLocked(pr), nil
	}
	if !match {
		return finish(stRejected, "mismatch", "the codes didn't match", map[string]any{"type": "rejected", "reason": "mismatch",
			"error": "the codes didn't match on your computer — nothing was paired"})
	}
	d, removed, err := s.opt.Users.AddDeviceReplacing(pr.userID, users.Device{Name: pr.name, Platform: pr.platform,
		PublicKey: pr.publicKey, Origin: pr.origin, TunnelKey: pr.noiseKey, Endpoint: pr.endpoint}, replace)
	var le *users.DeviceLimitError
	if errors.As(err, &le) {
		s.mu.Lock()
		pr.deciding, pr.sawLimit = false, true
		s.mu.Unlock()
		return PairingView{}, err
	}
	if err != nil {
		return finish(stRejected, "refused", err.Error(), map[string]any{"type": "rejected", "reason": "refused", "error": err.Error()})
	}
	if removed != nil {
		s.opt.Auth.DropDeviceSessions(removed.ID)
		if s.opt.OnDeviceRemoved != nil {
			s.opt.OnDeviceRemoved(pr.userID, removed.ID)
		}
	}
	// the sidecar admits the device before the phone hears it may connect
	actx, cancel := context.WithTimeout(ctx, ackTimeout)
	err = s.Resync(actx)
	cancel()
	if err != nil {
		_, _, _ = s.opt.Users.RemoveDevice(d.ID)
		slog.Warn("app tunnel: the sidecar didn't admit a new device", "err", err)
		return finish(stRejected, "refused", "the tunnel didn't admit the device", map[string]any{"type": "rejected", "reason": "refused",
			"error": "the workspace's tunnel didn't admit the device — try again"})
	}
	u, _ := s.opt.Users.Get(pr.userID)
	name := pr.userID
	if u != nil && u.Name != "" {
		name = u.Name
	}
	msg := map[string]any{"type": "enrolled", "deviceId": d.ID, "user": map[string]string{"id": pr.userID, "name": name},
		"origin": pr.origin, "name": d.Name, "tunnel": s.tunnelInfo()}
	s.mu.Lock()
	pr.deviceID = d.ID
	if removed != nil {
		pr.replaced = removed.ID
	}
	s.mu.Unlock()
	v, err := finish(stEnrolled, "", "", msg)
	if err != nil {
		_, _, _ = s.opt.Users.RemoveDevice(d.ID) // the phone never heard: no orphan record
		s.mu.Lock()
		pr.deviceID = ""
		s.mu.Unlock()
		go s.UserChanged("", false)
	}
	s.usersChanged()
	return v, err
}

// tunnelInfo is the "tunnel" part of enrolled: how the device reaches it.
func (s *Service) tunnelInfo() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.relayLocked()
	relay := map[string]string{}
	if r.Mode == RelayCustom {
		relay["url"] = r.URL
		if r.Token != "" {
			relay["token"] = r.Token
		}
	}
	return map[string]any{"ticket": s.runner.Status().Ticket, "relay": relay, "required": s.cfg.TunnelOnly}
}

// usersChanged publishes the users event (device lists refresh).
func (s *Service) usersChanged() {
	if s.opt.Hub != nil {
		s.opt.Hub.Publish(events.Event{Type: "users"})
	}
}
