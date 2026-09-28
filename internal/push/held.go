package push

import (
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Notifications from a tile deployment that isn't its tile's primary are
// never pushed (D127h) (09-fabric §6; 11-contract §3.3, §8). POST /notify
// answers such a call as it answers any other, 202, plus suppressed:true,
// after the same validation, and then:
//   - sends nothing and spends none of the tile's or the person's budget, so
//     a non-primary backend can't use up the primary's;
//   - asks no one whether the person reads the tile: without a budget that
//     would be an unbounded probe of who reads it, and nothing reaches them;
//   - keeps "would notify <user>: <title>" in a bounded list per (tile,
//     deployment), the last wouldNotifyMax, for the deployments panel
//     (Deployment.wouldNotify, 11-contract §1.1);
//   - announces it in the deployments event, op notify, which the server
//     delivers to the tile's write audience and that deployment's own
//     principals, never as a push or an old event type.
//
// A Holder carries the deployments plane's answers into POST /notify: boot
// mounts NotifyHandler(holder) where it mounted APINotify. Without one (or
// with its answers unset) every tile's one deployment is main, its primary,
// and nothing is held: APINotify is today's route.

// wouldNotifyMax is how many held notifications a deployment keeps.
const wouldNotifyMax = 20

// WouldNotify is one held notification: Deployment.wouldNotify's row.
type WouldNotify struct {
	At    string `json:"at"`    // RFC 3339, UTC
	To    string `json:"to"`    // "user:<id>", or "owner"
	Title string `json:"title"` // at most 280 bytes
}

// Holder holds the notifications of non-primary deployments.
type Holder struct {
	// Primary names a tile's primary deployment; Addressed answers which
	// deployment of a tile a request by p reaches (11-contract §0.4 DR1:
	// the tile's own principals their bound one), util.ErrNoDeployment for
	// one that no longer exists and any other error a refusal.
	Primary   func(tile string) string
	Addressed func(p auth.Principal, tile string) (string, error)
	Hub       *events.Hub      // the deployments event
	Now       func() time.Time // nil: time.Now

	mu    sync.Mutex
	notes map[string][]WouldNotify // tile\x00deployment → oldest first
}

// held is the deployment of tile a notification by p comes from when it
// isn't tile's primary; "" when it is, and the notification goes out as
// today.
func (h *Holder) held(p auth.Principal, tile string) (string, error) {
	if h == nil || h.Primary == nil || h.Addressed == nil {
		return "", nil
	}
	dep, err := h.Addressed(p, tile)
	if err != nil || dep == h.Primary(tile) {
		return "", err
	}
	return dep, nil
}

// hold keeps deployment dep of tile's notification to user and announces it.
func (h *Holder) hold(tile, dep, user, title string) {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	to := "user:" + user
	if user == OwnerUser {
		to = OwnerUser
	}
	n := WouldNotify{At: now().UTC().Format(time.RFC3339), To: to, Title: clip(title, 280)}
	key := tile + "\x00" + dep
	h.mu.Lock()
	if h.notes == nil {
		h.notes = map[string][]WouldNotify{}
	}
	list := append(h.notes[key], n)
	if len(list) > wouldNotifyMax {
		list = slices.Clone(list[len(list)-wouldNotifyMax:])
	}
	h.notes[key] = list
	h.mu.Unlock()
	if h.Hub != nil {
		h.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: map[string]any{
			"op": "notify", "deployment": dep, "to": n.To, "title": n.Title, "at": n.At}})
	}
}

// WouldNotify lists deployment dep of tile's held notifications, newest
// first: Deployment.wouldNotify.
func (h *Holder) WouldNotify(tile, dep string) []WouldNotify {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := slices.Clone(h.notes[tile+"\x00"+dep])
	slices.Reverse(out)
	return out
}

// Drop forgets deployment dep of tile's held notifications, when the
// deployment is removed or a tile is created at the path.
func (h *Holder) Drop(tile, dep string) {
	h.mu.Lock()
	delete(h.notes, tile+"\x00"+dep)
	h.mu.Unlock()
}

// NotifyHandler is POST /notify with non-primary deployments' notifications
// held by h (APINotify's, otherwise unchanged).
func (s *Service) NotifyHandler(h *Holder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.notify(w, r, h) }
}

// clip cuts s to at most n bytes, on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// writeHeldErr answers a notifier whose credential's deployment can't be
// told: 404 when it no longer exists, 403 when refused.
func writeHeldErr(w http.ResponseWriter, err error) {
	if errors.Is(err, util.ErrNoDeployment) {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
}
