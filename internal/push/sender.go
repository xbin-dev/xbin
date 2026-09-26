package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// job is either a note to fan out to a user's devices, or one sealed
// delivery to one device (a retry carries the same envelope).
type job struct {
	note *note

	user, deviceID, handle string
	env                    Envelope
	collapse               string
	priority               int
	attempt                int // deliveries tried so far
}

const maxAttempts = 5

// Stats are the sender's counters since boot (GET /push/config).
type Stats struct {
	Queued      int    `json:"queued"`
	Sent        int64  `json:"sent"`
	Retried     int64  `json:"retried"`
	Failed      int64  `json:"failed"`
	Dropped     int64  `json:"dropped"` // the queue was full
	LastError   string `json:"lastError,omitempty"`
	LastErrorAt int64  `json:"lastErrorAt,omitempty"`
}

// sender posts sealed envelopes to the relay from a bounded queue: callers
// never wait (a full queue drops), failures retry with backoff, and a relay
// that calls a handle dead removes the registration.
type sender struct {
	s    *Service
	q    chan job
	done chan struct{}
	wg   sync.WaitGroup

	sent, retried, failed, dropped atomic.Int64

	mu      sync.Mutex
	lastErr string
	lastAt  int64
	closed  bool
}

func newSender(s *Service) *sender {
	size, workers := s.o.QueueSize, s.o.Workers
	if size <= 0 {
		size = 256
	}
	if workers <= 0 {
		workers = 2
	}
	d := &sender{s: s, q: make(chan job, size), done: make(chan struct{})}
	for range workers {
		d.wg.Add(1)
		go d.run()
	}
	return d
}

func (d *sender) close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.mu.Unlock()
	close(d.done)
	d.wg.Wait()
}

func (d *sender) enqueue(j job) bool {
	select {
	case <-d.done:
		return false
	default:
	}
	select {
	case d.q <- j:
		return true
	default:
		d.dropped.Add(1)
		return false
	}
}

func (d *sender) stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return Stats{Queued: len(d.q), Sent: d.sent.Load(), Retried: d.retried.Load(), Failed: d.failed.Load(),
		Dropped: d.dropped.Load(), LastError: d.lastErr, LastErrorAt: d.lastAt}
}

func (d *sender) noteErr(msg string) {
	d.mu.Lock()
	d.lastErr, d.lastAt = msg, d.s.o.Now().Unix()
	d.mu.Unlock()
}

func (d *sender) run() {
	defer d.wg.Done()
	for {
		select {
		case <-d.done:
			return
		case j := <-d.q:
			if j.note != nil {
				d.fanOut(*j.note)
			} else {
				d.deliver(j)
			}
		}
	}
}

// fanOut seals a note to each of the user's devices that wants its kind.
func (d *sender) fanOut(n note) {
	s := d.s
	if !s.Enabled() {
		return
	}
	if n.tile != "" && s.st.muted(n.user, n.tile) {
		return
	}
	ws := s.Workspace()
	for _, dev := range s.st.devices(n.user) {
		if !kindAllowed(dev.Kinds, n.kind) {
			continue
		}
		pub, err := ParsePublicKey(dev.PublicKey)
		if err != nil {
			continue
		}
		pt, err := fit(Payload{V: 1, WS: ws, Kind: n.kind, Title: n.title, Body: n.body, Link: n.link, CollapseID: n.collapse})
		if err != nil {
			continue
		}
		env, err := Seal(pub, pt)
		if err != nil {
			d.failed.Add(1)
			d.noteErr("seal: " + err.Error())
			continue
		}
		d.deliver(job{user: dev.User, deviceID: dev.DeviceID, handle: dev.Handle, env: env,
			collapse: s.relayCollapse(n.collapse), priority: n.priority})
	}
}

// outcome of one relay post.
type outcome int

const (
	delivered outcome = iota
	retryLater
	deadHandle // drop the registration
	refused    // drop the notification
)

func (d *sender) deliver(j job) {
	s := d.s
	cfg, _ := s.relayConfig()
	if cfg == nil {
		return
	}
	out, wait, msg := d.post(cfg, j)
	j.attempt++
	switch out {
	case delivered:
		d.sent.Add(1)
		s.st.markSent(j.user, j.deviceID, s.o.Now().Unix())
	case deadHandle:
		d.failed.Add(1)
		d.noteErr(msg)
		if s.st.removeHandle(j.user, j.deviceID, j.handle) {
			s.o.Log.Info("push: device registration dropped", "user", j.user, "device", j.deviceID, "why", msg)
		}
	case refused:
		d.failed.Add(1)
		d.noteErr(msg)
		s.o.Log.Warn("push: relay refused a notification", "why", msg)
	case retryLater:
		d.noteErr(msg)
		if j.attempt >= maxAttempts {
			d.failed.Add(1)
			s.o.Log.Warn("push: giving up after retries", "user", j.user, "device", j.deviceID, "why", msg)
			return
		}
		d.retried.Add(1)
		time.AfterFunc(max(wait, d.backoff(j.attempt)), func() { d.enqueue(j) })
	}
}

func (d *sender) backoff(attempt int) time.Duration {
	if d.s.o.Backoff != nil {
		return d.s.o.Backoff(attempt)
	}
	w := 2 * time.Second
	for i := 1; i < attempt && w < 5*time.Minute; i++ {
		w *= 4
	}
	return min(w, 5*time.Minute)
}

// post sends one envelope to the relay (relay/README.md: POST /v1/push).
func (d *sender) post(cfg *RelayConfig, j job) (outcome, time.Duration, string) {
	body, _ := json.Marshal(map[string]any{"handle": j.handle, "envelope": j.env, "collapseId": j.collapse, "priority": j.priority})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.URL, "/")+"/v1/push", bytes.NewReader(body))
	if err != nil {
		return refused, 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Key)
	resp, err := d.s.o.HTTP.Do(req)
	if err != nil {
		return retryLater, 0, "relay: " + err.Error()
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	why := fmt.Sprintf("relay %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	var wait time.Duration
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		wait = time.Duration(min(secs, 3600)) * time.Second
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return delivered, 0, ""
	case resp.StatusCode == http.StatusGone, resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusForbidden:
		return deadHandle, 0, why
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return retryLater, wait, why
	default: // 400, 401: retrying cannot help
		return refused, 0, why
	}
}
