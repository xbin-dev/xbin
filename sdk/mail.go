package xbin

// Partition mail (docs/partitions.md §Partition mail; design:
// plans/partitions/04 §3). A partitioned tile's global instance hands
// something to ONE person's partition — and a person's partition hands
// something to global — through an inbox xbind keeps for each addressee:
// durable, sealed, readable only by its addressee, with the sender stamped
// by xbind. Delivery is at-least-once until acknowledged or expired, so a
// handler dedupes by item ID.
//
// When the tile declares "partitionMail": "/mailbox" beside "global", xbind
// rings that path on the addressee's instance whenever its inbox holds
// items (a MailBell body, From: xbin/mail); the handler reads with InboxPage
// and acknowledges with Ack. Without it, the code polls Inbox.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MailItem is one item of this partition's inbox. From is stamped by xbind —
// "global" for the tile's global instance, "user:<id>" for that person's
// partition — and can be trusted; never trust a person named inside Data.
type MailItem struct {
	ID      string          `json:"id"`
	From    string          `json:"from"`
	Topic   string          `json:"topic"`
	Data    json.RawMessage `json:"data"`
	At      time.Time       `json:"at"`
	Expires time.Time       `json:"expires"`
}

// MailBell is the doorbell xbind POSTs to the tile's partitionMail path
// (From: xbin/mail) while the inbox of Partition holds Pending items.
type MailBell struct {
	Partition string `json:"partition"`
	Pending   int    `json:"pending"`
}

// MailOptions are MailWith's optional fields.
type MailOptions struct {
	// TTL is how long the item waits unacknowledged (whole seconds, at
	// most 30 days); 0 means xbind's default, 7 days.
	TTL time.Duration
	// Source names where a private trigger's event came from, when the
	// global instance hands one to a person: xbind counts it in that
	// person's egress ledger (never the content). Ignored otherwise.
	Source string
}

// errNoMail is what the mail helpers answer on an xbind without partition
// mail (a 404 on its routes).
func errNoMail(status string) error {
	return fmt.Errorf("partition mail: this xbind has no partition mail (partition-mail/1): %s (/docs/partitions.md)", status)
}

// Mail sends partition mail: from the tile's global instance to
// "user:<id>" (a person who can read the tile) or "global"; from a person's
// partition to "global" only. It answers the item's id.
func Mail(to, topic string, data any) (string, error) {
	return MailWith(to, topic, data, MailOptions{})
}

// MailWith is Mail with options.
func MailWith(to, topic string, data any, o MailOptions) (string, error) {
	body := map[string]any{"to": to, "topic": topic, "data": data}
	if o.TTL > 0 {
		body["ttl"] = int64((o.TTL + time.Second - 1) / time.Second)
	}
	if o.Source != "" {
		body["source"] = o.Source
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	resp, err := Client().Post("http://xbin/api/xbin/partitions/mail", "application/json", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound && !strings.Contains(string(b), "no such person") {
			return "", errNoMail(resp.Status)
		}
		return "", fmt.Errorf("partition mail: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("partition mail: an answer without an id: %s", strings.TrimSpace(string(b)))
	}
	return out.ID, nil
}

// MailPage is one page of this partition's inbox. More says items wait
// past it: a page stops at its limit or at about 8 MiB of data, so a page
// shorter than the limit isn't the end — only More false is.
type MailPage struct {
	Items []MailItem `json:"items"`
	More  bool       `json:"more"`
}

// InboxPage lists this partition's unacknowledged mail, oldest first: after
// is the last id already read ("" from the start), limit at most 1000 (0
// means 100). Read on with the last item's ID while More.
func InboxPage(after string, limit int) (MailPage, error) {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	u := "http://xbin/api/xbin/partitions/mail"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := Client().Get(u)
	if err != nil {
		return MailPage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusNotFound {
			return MailPage{}, errNoMail(resp.Status)
		}
		return MailPage{}, fmt.Errorf("partition mail: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out MailPage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return MailPage{}, fmt.Errorf("partition mail: %w", err)
	}
	return out, nil
}

// Inbox is InboxPage's items alone. A page shorter than limit may not be
// the end (it stops at about 8 MiB of data): an empty page is, or use
// InboxPage and its More.
func Inbox(after string, limit int) ([]MailItem, error) {
	pg, err := InboxPage(after, limit)
	return pg.Items, err
}

// Ack removes items from this partition's inbox; ids already acknowledged
// or expired are nothing to do.
func Ack(ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"ids": ids})
	if err != nil {
		return err
	}
	resp, err := Client().Post("http://xbin/api/xbin/partitions/mail/ack", "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusNotFound {
			return errNoMail(resp.Status)
		}
		return errors.New("partition mail: " + resp.Status + ": " + strings.TrimSpace(string(b)))
	}
	return nil
}
