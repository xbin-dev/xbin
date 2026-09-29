package sandboxcontract

// ports.go — the ports capability (D135): ANY
// /sbx/sandboxes/{id}/ports/{port}/{path…}, a proxy to a server on the
// sandbox's own loopback. Its checks need a server in the sandbox: python3
// (http.server) when the image has it, else the live part is skipped.

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

var portChecks = []check{
	{"proxy", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "web"})
		// nothing listens: 502 not-listening (a port nothing in a fresh sandbox holds)
		a.Refused("GET", "/sandboxes/"+sb.ID+"/ports/1/", nil, http.StatusBadGateway, "not-listening")
		for _, p := range []string{"0", "65536", "x"} {
			a.Refused("GET", "/sandboxes/"+sb.ID+"/ports/"+p+"/", nil, http.StatusBadRequest, "invalid")
		}
		if strings.TrimSpace(a.Sh(sb.ID, "command -v python3 >/dev/null && echo yes || echo no")) != "yes" {
			t.Skip("the sandbox has no python3 to serve a page (the refusals above were checked)")
		}
		a.Put(sb.ID, sb.Workdir+"/site/index.html", "<p>from the sandbox</p>", "&mkdirs=1")
		a.Put(sb.ID, sb.Workdir+"/site/sub dir/a.txt", "deep", "&mkdirs=1")
		x := a.Exec(sb.ID, map[string]any{"cmd": "exec python3 -u -m http.server 0 --bind 127.0.0.1", "cwd": sb.Workdir + "/site"})
		port := ""
		re := regexp.MustCompile(`port (\d+)`)
		eventually(t, 20*time.Second, "python3 -m http.server says its port", func() bool {
			ch := a.Chunk(sb.ID, x.ID, "since=0&waitMs=500")
			if m := re.FindStringSubmatch(ch.Data); m != nil {
				port = m[1]
				return true
			}
			if ch.State != "running" {
				t.Fatalf("the server ended: %q", ch.Data)
			}
			return false
		})
		base := "/sandboxes/" + sb.ID + "/ports/" + port
		var body []byte
		eventually(t, 10*time.Second, "the server answers through the proxy", func() bool {
			resp, b, err := a.Do(context.Background(), "GET", base+"/", nil)
			if err == nil && resp.StatusCode == 200 {
				body = b
				return true
			}
			return false
		})
		if string(body) != "<p>from the sandbox</p>" {
			t.Fatalf("index: %q", body)
		}
		var got []byte
		h := a.Call("GET", base+"/sub%20dir/a.txt?x=1", nil, 200, &got)
		if string(got) != "deep" || h.Get("Content-Type") == "" {
			t.Fatalf("an escaped path with a query: %q %v", got, h)
		}
		a.Call("GET", base+"/nothing-here", nil, 404, nil) // the server's own 404 comes back as it is
		// another consumer: the sandbox doesn't exist
		e.as("b").Refused("GET", base+"/", nil, 404, "not-found")
		a.Call("DELETE", "/sandboxes/"+sb.ID+"/execs/"+x.ID, nil, 204, nil)
	}},
	{"people", func(t *testing.T, e *env) {
		a := e.as("a")
		alice, bob := a.Verified("alice"), a.Verified("bob")
		sb := alice.Create(map[string]any{"name": "private"})
		// the person rules of exec: bob may not use alice's private sandbox
		bob.Refused("GET", "/sandboxes/"+sb.ID+"/ports/1/", nil, 403, "not-allowed")
		alice.Refused("GET", "/sandboxes/"+sb.ID+"/ports/1/", nil, http.StatusBadGateway, "not-listening")
	}},
}
