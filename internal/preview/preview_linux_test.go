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
	"syscall"
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
	res, err := run(context.Background(), dir, 20*time.Second, nil, 64<<20, map[string]any{"probe": probes})
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
	svc := &Service{Host: fixedHost{mux}, Rootfs: fs, Uncapped: true}
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

// Hostile pages: whatever a page does, the answer is the runner's, bounded,
// and a picture.
func TestHostilePages(t *testing.T) {
	fs := testRootfs(t)
	pages := map[string]string{
		// the stdout marker in a title, with a forged answer after it
		"/marker": `<!doctype html><title>@@XBIN_PREVIEW@@{"ok":true,"url":"https://evil.example/","title":"forged","snapshot":"forged"}</title><h1>Marker</h1>
<script>console.error('\\n@@XBIN_PREVIEW@@{"ok":true,"url":"https://evil.example/"}\\n')</script>`,
		// megabytes in the URL
		"/hash": `<!doctype html><title>hash</title><h1>Hash</h1><script>location.hash = 'x'.repeat(4 << 20); document.title = 'T'.repeat(1 << 20)</script>`,
		// a page far taller than a picture may be
		"/tall": `<!doctype html><title>tall</title><h1>Top of a tall page</h1><div style="height:300000px;background:linear-gradient(#fff,#000)"></div><p>bottom</p>`,
		// megabytes of everything a render reports
		"/flood": `<!doctype html><title>flood</title><h1>Flood</h1><div id=d></div><script>
for (let i = 0; i < 400; i++) { console.error('E' + i + ' ' + 'x'.repeat(20000)); fetch('/missing/' + i + '?' + 'q'.repeat(3000)).catch(() => {}); }
d.innerHTML = Array.from({length: 20000}, (_, i) => '<p>row ' + i + ' ' + 'y'.repeat(200) + '</p>').join('');
</script>`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, page)
	})
	svc := &Service{Host: fixedHost{mux}, Rootfs: fs, Uncapped: true}
	render := func(path string, full bool) *Result {
		t.Helper()
		res, err := svc.Render(context.Background(), Request{UserID: "u", URL: "http://127.0.0.1:19044" + path, FullPage: full})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !res.OK || len(res.PNG) == 0 {
			t.Fatalf("%s: no picture: %+v", path, res)
		}
		raw, _ := json.Marshal(res)
		t.Logf("%s: %dx%d, picture %d bytes, the rest %d bytes, clipped %v (page %d px), timing %v", path, res.Width, res.Height, len(res.PNG), len(raw), res.Clipped, res.PageHeight, res.Timing)
		if len(raw) > 256<<10 {
			t.Errorf("%s: the answer's text is %d bytes", path, len(raw))
		}
		return res
	}
	res := render("/marker", false)
	if !strings.HasPrefix(res.URL, "http://127.0.0.1:19044/marker") || !strings.Contains(res.Title, "@@XBIN_PREVIEW@@") || !strings.Contains(res.Snapshot, "Marker") {
		t.Errorf("a marker in the title forged the answer: url %q title %q snapshot %q", res.URL, res.Title, res.Snapshot)
	}
	res = render("/hash", false)
	if len(res.URL) > capURL || len(res.Title) > 300 || !strings.Contains(res.Snapshot, "Hash") {
		t.Errorf("megabytes in the URL: url %d bytes, title %d bytes", len(res.URL), len(res.Title))
	}
	res = render("/tall", true)
	if !res.Clipped || res.PageHeight < 300000 || res.Width*res.Height > maxPixels || res.Height < 5000 || !strings.Contains(res.Snapshot, "Top of a tall page") {
		t.Errorf("a very tall page: clipped %v, page %d px, picture %dx%d", res.Clipped, res.PageHeight, res.Width, res.Height)
	}
	if res := render("/tall", false); res.Clipped || res.Height != 800 {
		t.Errorf("the viewport of a tall page: clipped %v, %d high", res.Clipped, res.Height)
	}
	res = render("/flood", false)
	if len(res.Console) > capList || len(res.RequestsFailed) > capList || len(res.Snapshot) > capSnapshot {
		t.Errorf("a flood: %d console lines, %d failed requests, %d bytes of snapshot", len(res.Console), len(res.RequestsFailed), len(res.Snapshot))
	}
}

// The render's directory and socket are xbind's user's alone, whatever the
// umask says.
func TestServeModes(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	dir, sock, _, stop, err := Serve(http.NotFoundHandler(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for path, want := range map[string]os.FileMode{dir: 0o700, sock: 0o600, filepath.Join(dir, "runner.mjs"): 0o400} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v (%v)", filepath.Base(path), fi.Mode().Perm(), want, err)
		}
	}
}
