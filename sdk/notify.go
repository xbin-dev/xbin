package xbin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// UserNotification is a push notification to one person's xbin app devices
// (NotifyUserWith).
type UserNotification struct {
	// User is the id of the person to notify — CallerInfo.User of a request
	// they made. They must be able to read this tile.
	User  string
	Title string // plain text, required
	Body  string // plain text
	// Link opens the tile at a place when the notification is tapped:
	// "#fragment", "?query" or a path inside the tile ("" = the tile).
	Link string
	// Kind labels the notification (a–z 0–9 -): the push kind becomes
	// tile.<kind>, which a device may filter on. Optional.
	Kind string
	// CollapseID makes a later notification replace an earlier one with the
	// same id (A–Z a–z 0–9 . _ : -, at most 64). Optional.
	CollapseID string
}

// ErrNotifyRateLimited is returned (wrapped) when xbind refused a
// notification over this tile's limit; try again later. (Over a person's
// limit a notification is dropped quietly, never refused.)
var ErrNotifyRateLimited = errors.New("xbin: too many notifications")

// NotifyUser sends a push notification to a person's registered xbin app
// devices (POST /api/xbin/notify) — for moments that need them when they are
// not looking: a question, an approval, a failed run. The person must be able
// to read this tile. Call it from the backend. Delivery is best-effort and
// asynchronous: a nil error means xbind accepted it, not that a phone showed
// it (push may be off, the person may have no device, may have muted this
// tile, or be over their hourly limit). Distinct from Notify, the in-shell
// toast.
//
//	err := xbin.NotifyUser(ctx, xbin.Caller(r).User, "Approval needed", "Deploy v2.3 to prod?", "#approvals/17")
func NotifyUser(ctx context.Context, user, title, body, link string) error {
	return NotifyUserWith(ctx, UserNotification{User: user, Title: title, Body: body, Link: link})
}

// NotifyUserWith is NotifyUser with every field.
func NotifyUserWith(ctx context.Context, n UserNotification) error {
	b, err := json.Marshal(map[string]string{"user": n.User, "title": n.Title, "body": n.Body, "link": n.Link,
		"kind": n.Kind, "collapseId": n.CollapseID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://xbin/api/xbin/notify", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	text := strings.TrimSpace(string(msg))
	if json.Unmarshal(msg, &e) == nil && e.Error != "" {
		text = e.Error
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: %s", ErrNotifyRateLimited, text)
	}
	return fmt.Errorf("notify: %s: %s", resp.Status, text)
}
