//go:build linux && integration

package isolated

// partitions_agent_files_test.go — the big-file case of
// TestPartitionsAgentChannels (work pack AF, the owner's ruling 90 §I11;
// the template's API.md "Partitioned instances"): a file sent with a linked
// DM that is too large for the handoff's partition mail (640 KiB of files a
// mail) waits in the global instance's storage, alice's partition fetches
// it over her own-global call before taking the DM, it lands in her DM
// conversation whole — and once her partition acknowledged it, the global
// instance holds nothing more of hers (read through pcHeldProbe).

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// pcHeldProbe is a builder's topic compiled into the instance (as
// pcHandoffProbe): at the global instance, a person's e2e/held {nonce} is
// answered by mailing them how many DM files it still holds for them
// (e2e/held-n {nonce, held}; their partition leaves it in their inbox,
// where their frame reads it).
const pcHeldProbe = `package main

import (
	"context"
	"encoding/json"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	mailHandlers["e2e/held"] = func(_ context.Context, t *DB, it mailItem) error {
		if !globalMode() || !strings.HasPrefix(it.From, "user:") {
			return nil
		}
		var n int
		_ = t.q.QueryRow("SELECT count(*) FROM handoff_files h JOIN channel_files c ON c.id=h.file_id WHERE h.person=? AND h.dir='in'",
			strings.TrimPrefix(it.From, "user:")).Scan(&n)
		var nonce string
		_ = json.Unmarshal(it.Data, &nonce)
		t.AfterCommit(func() { _, _ = xbin.Mail(it.From, "e2e/held-n", map[string]any{"nonce": nonce, "held": n}) })
		return nil
	}
}
`

func pcBigFile(t *testing.T, e *psEnv, ok func(method, path string, body any, hdrs ...xbindtest.Header) xbindtest.Resp,
	ag func(person, method, path string, body any) xbindtest.Resp) {
	big := make([]byte, 800<<10)
	for i := range big {
		big[i] = byte(i*7 + i>>11)
	}
	ok("POST", "/api/"+pcBridge+"/console/send", map[string]any{"as": map[string]string{"id": "u1", "name": "Uma"},
		"conversation": map[string]string{"id": "D1", "type": "dm"}, "text": "hello, the big chart",
		"files": []map[string]string{{"name": "chart.bin", "mime": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(big)}}})
	var got string
	xbindtest.Eventually(t, 4*time.Minute, "the big file in alice's DM conversation", func() (bool, string) {
		var list struct {
			Items []struct {
				ID     int64
				Origin string
			}
		}
		_ = json.Unmarshal(ag("alice", "GET", "/conversations?scope=mine", nil).Body, &list)
		for _, it := range list.Items {
			if it.Origin != "channel" {
				continue
			}
			var files []struct {
				Path   string
				Bytes  int
				Binary bool
			}
			r := ag("alice", "GET", fmt.Sprintf("/runs/%d/files", it.ID), nil)
			_ = json.Unmarshal(r.Body, &files)
			got = fmt.Sprintf("run %d: %s", it.ID, cut(string(r.Body), 300))
			for _, f := range files {
				if f.Binary && f.Bytes == len(big) {
					return true, got
				}
			}
		}
		return false, got
	})
	// nothing says it wasn't passed on
	var list struct {
		Items []struct {
			ID     int64
			Origin string
		}
	}
	ag("alice", "GET", "/conversations?scope=mine", nil).Decode(t, &list)
	for _, it := range list.Items {
		if it.Origin == "channel" {
			if r := ag("alice", "GET", fmt.Sprintf("/runs/%d", it.ID), nil); strings.Contains(string(r.Body), "not passed on") {
				t.Errorf("BUG: the DM says a file wasn't passed on: %s", cut(string(r.Body), 400))
			}
		}
	}
	// her partition acknowledged the fetch: the global instance holds none of her files any more
	const mailAPI = "/api/xbin/partitions/mail"
	d, alice := e.d, e.fr(t, paAgent, "alice")
	asked, held := 0, -1
	xbindtest.Eventually(t, 2*time.Minute, "the global instance holding none of alice's DM files", func() (bool, string) {
		asked++
		nonce := fmt.Sprintf("big-%d", asked)
		d.Must(t, "POST", mailAPI, map[string]any{"to": "global", "topic": "e2e/held", "data": nonce}, 200, alice)
		for range 40 {
			var pg struct {
				Items []struct {
					Topic string
					Data  struct {
						Nonce string
						Held  int
					}
				}
			}
			d.Must(t, "GET", mailAPI, nil, 200, alice).Decode(t, &pg)
			for _, it := range pg.Items {
				if it.Topic == "e2e/held-n" && it.Data.Nonce == nonce {
					held = it.Data.Held
					return held == 0, fmt.Sprintf("held %d", held)
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
		return false, fmt.Sprintf("no answer to %s (last: held %d)", nonce, held)
	})
}
