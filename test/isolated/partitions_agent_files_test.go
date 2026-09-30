//go:build linux && integration

package isolated

// partitions_agent_files_test.go — the big-file case of
// TestPartitionsAgentChannels (work pack AF, the owner's ruling 90 §I11;
// the template's API.md "Partitioned instances"): a file sent with a linked
// DM that is too large for the handoff's partition mail (640 KiB of files a
// mail) isn't named in the text any more — it waits in the global
// instance's storage, alice's partition fetches it over her own-global call
// before taking the DM, and it lands in her DM conversation whole.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

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
}
