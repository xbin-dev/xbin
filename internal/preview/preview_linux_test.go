//go:build linux && integration

// Run with: go test -tags=integration ./internal/preview/
// Needs user namespaces and the base rootfs with Node, Playwright and
// Chromium (XBIN_TEST_ROOTFS, or the repo's .rootfs); skips otherwise.
package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
	os.Exit(m.Run())
}

func testRootfs(t *testing.T) string {
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(fs, browsers)); err != nil || !sandbox.Available() {
		t.Skip("no rootfs with Chromium, or no user namespaces")
	}
	confine.Configure(fs)
	t.Cleanup(func() { confine.Configure("") })
	return fs
}

type fixedHost struct{ h http.Handler }

func (f fixedHost) OpenPreview(string, time.Duration, any) (http.Handler, func(), error) {
	return f.h, func() {}, nil
}

// The sandbox has one way out: the socket. A listener on the host's loopback
// and one on a host interface are unreachable, and there is no interface but
// its own lo.
func TestSandboxReachesOnlyTheSocket(t *testing.T) {
	testRootfs(t)
	hostLn, err := net.Listen("tcp", "127.0.0.1:19041")
	if err != nil {
		t.Fatal(err)
	}
	defer hostLn.Close()
	probes := []string{"127.0.0.1:19041"}
	if addrs, _ := net.InterfaceAddrs(); true {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				ln, err := net.Listen("tcp", net.JoinHostPort(ipn.IP.String(), "19042"))
				if err == nil {
					defer ln.Close()
					probes = append(probes, net.JoinHostPort(ipn.IP.String(), "19042"))
				}
				break
			}
		}
	}
	dir, _, _, stop, err := Serve(http.NotFoundHandler(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	res, err := run(context.Background(), dir, 20*time.Second, nil, map[string]any{"probe": probes})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("probe: %s", res.Detail)
	var got struct {
		Interfaces []string
		Dials      map[string]string
	}
	if err := json.Unmarshal([]byte(res.Detail), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Interfaces) != 1 || got.Interfaces[0] != "lo" {
		t.Errorf("the sandbox has interfaces besides its own loopback: %v", got.Interfaces)
	}
	for target, how := range got.Dials {
		if how == "connected" || how == "timeout" {
			t.Errorf("the sandbox reached (or could route to) %s: %s", target, how)
		}
	}
	if len(got.Dials) != len(probes) {
		t.Errorf("probed %d of %d targets", len(got.Dials), len(probes))
	}
}

// One static page through the whole path: forwarder, socket, Chromium,
// picture, snapshot — and the page's own attempts to leave fail.
func TestRenderStaticPage(t *testing.T) {
	fs := testRootfs(t)
	hostLn, err := net.Listen("tcp", "127.0.0.1:19043")
	if err != nil {
		t.Fatal(err)
	}
	defer hostLn.Close()
	go func() {
		for {
			c, err := hostLn.Accept()
			if err != nil {
				return
			}
			t.Error("the sandbox reached a host listener")
			c.Close()
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>probe</title><h1>Hello preview</h1><button>Press</button><p id=o>…</p>
<script type=module>
const t = async (u) => { try { await fetch(u, {mode:'no-cors'}); return 'reached'; } catch { return 'blocked'; } };
o.textContent = 'host-loopback:' + await t('http://127.0.0.1:19043/') + ' secure:' + isSecureContext;
</script>`)
	})
	svc := &Service{Host: fixedHost{mux}, Rootfs: fs}
	for _, vp := range [][2]int{{1280, 800}, {390, 844}} {
		start := time.Now()
		res, err := svc.Render(context.Background(), Request{UserID: "u", URL: "http://127.0.0.1:19044/", Width: vp[0], Height: vp[1]})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%dx%d: wall %s timing %v memory %d kB png %d bytes (%dx%d) %s, its own sandbox: %s, ready: %s; snapshot:\n%s", vp[0], vp[1], time.Since(start).Round(time.Millisecond),
			res.Timing, res.MemoryKB, len(res.PNG), res.Width, res.Height, res.Browser, res.Sandbox, res.Ready, res.Snapshot)
		if res.Notice != Notice || !strings.HasPrefix(res.Browser, "chromium ") || res.Sandbox == "" {
			t.Errorf("the answer lacks its notice, browser or sandbox: %q %q %q", res.Notice, res.Browser, res.Sandbox)
		}
		if !res.OK || len(res.PNG) == 0 || res.Width != vp[0] {
			t.Fatalf("no picture: %+v", res)
		}
		if !strings.Contains(res.Snapshot, "Hello preview") || !strings.Contains(res.Snapshot, "host-loopback:blocked") {
			t.Fatalf("snapshot: %s", res.Snapshot)
		}
	}
}
