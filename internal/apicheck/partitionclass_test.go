package apicheck

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-29 S12 C10 — TestPartitionRouteClasses, the guard of
// plans/partitions/02 §8: every /api/xbin route xbind mounts has a class in
// internal/server/partitionclass.go, every DeploymentScoped route of the
// deployment table among them, and every row names a route that exists;
// every route web/xbin-client.js or the Go SDK (sdk/) calls has a row; and
// every route still waiting for its handler's conversion is a
// PartitionScoped one. Its "refusals" subtest drives the table through
// handleAPI with real credentials.
func TestPartitionRouteClasses(t *testing.T) {
	inv := inventory(t)
	table := server.PartitionClasses()
	var problems []string
	for p, where := range inv {
		if table[p] == server.PartitionUnclassified {
			problems = append(problems, where+" registers "+p+", which has no class in internal/server/partitionclass.go: "+
				"add a row, or a person's partition gets 403 on it")
		}
	}
	for p, c := range server.RouteClasses() {
		if c == server.DeploymentScoped && table[p] == server.PartitionUnclassified {
			problems = append(problems, "the deployment-scoped route "+p+" has no partition class")
		}
	}
	for p, c := range table {
		if _, ok := inv[p]; !ok {
			problems = append(problems, "internal/server/partitionclass.go classes "+p+" ("+c.String()+"), which no code registers: drop the row")
		}
	}
	for p := range server.PartitionUnconverted() {
		if table[p] != server.PartitionScoped {
			problems = append(problems, "partitionUnconverted lists "+p+", which isn't partition-scoped")
		}
	}
	for path, where := range clientRoutes(t) {
		if !rowMatches(table, path) {
			problems = append(problems, where+" calls /api/xbin"+path+", which matches no row of internal/server/partitionclass.go")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	t.Run("refusals", func(t *testing.T) { partitionRefusals(t, inv) })
}

var clientLit = regexp.MustCompile("/api/xbin(/[A-Za-z0-9_./{}%:-]*)")

// clientRoutes are the /api/xbin paths web/xbin-client.js and the Go SDK's
// non-test sources name, path → where. A path ending in "/" (a prefix a key
// or name is appended to) gets a segment.
func clientRoutes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	scan := func(p string) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range clientLit.FindAllStringSubmatch(string(b), -1) {
			path := m[1]
			if strings.HasSuffix(path, "/") {
				path += "x"
			}
			out[path] = filepath.ToSlash(p)
		}
	}
	scan(filepath.Join("..", "..", "web", "xbin-client.js"))
	err := filepath.WalkDir(filepath.Join("..", "..", "sdk"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		scan(p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 5 {
		t.Fatalf("found %d client routes — is the scan intact?", len(out))
	}
	return out
}

// rowMatches reports whether path matches some row's pattern, for any
// method.
func rowMatches(table map[string]server.PartitionClass, path string) bool {
	mux := http.NewServeMux()
	for p := range table {
		mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {})
	}
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if _, pat := mux.Handler(httptest.NewRequest(m, path, nil)); pat != "" {
			return true
		}
	}
	return false
}

// partitionPolicy partitions apps/pt: its instances act in their
// registered partition, its frames and terminals in their person's (global
// for the owner's); every other tile is unpartitioned.
type partitionPolicy struct{ server.NoopPolicy }

func (partitionPolicy) AddressedPartition(p auth.Principal, tile string) (util.Partition, error) {
	switch {
	case tile != "apps/pt":
		return "", nil
	case p.Via == "instance":
		return util.Partition(string(p.Partition) + map[bool]string{true: "global"}[p.Partition == ""]), nil
	case p.UserID != "":
		return util.UserPartition(p.UserID), nil
	}
	return util.PartitionGlobal, nil
}

// partitionRefusals mounts every inventory route on a server of its own,
// each answering which pattern ran and the partition its principal
// carries, plus a route without a class, and calls each with a user
// partition's instance, frame and terminal credentials of apps/pt, the
// owner's frame (global), the global instance's token, an unpartitioned
// tile's instance and frame, and the owner:
//   - a user partition is refused on every unconverted partition-scoped,
//     dormant, global-only and unclassified route before any handler runs,
//     and reaches the others with its partition on its principal;
//   - global, an unpartitioned tile and the owner reach every route, their
//     principal carrying no partition.
func partitionRefusals(t *testing.T, inv map[string]string) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	root := t.TempDir()
	for rel, body := range map[string]string{
		"xbin.json":            `{"schema":1}`,
		"apps/pt/xbin.json":    `{}`,
		"apps/pt/index.html":   "<!doctype html><p>pt</p>\n",
		"apps/other/xbin.json": `{}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	a.SetPartitionCoverage(func(string, util.Partition, string) bool { return true })
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelTerminal}}, "password1"); err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	srv := &server.Server{Reg: reg, Auth: a, Hub: events.NewHub()}
	srv.InstallPolicy(partitionPolicy{})
	h := srv.Handler()
	core := map[string]bool{}
	for _, p := range srv.APIRoutes() {
		core[p] = true
	}
	ran := func(pattern string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			server.WriteJSON(w, http.StatusOK, map[string]string{"ran": pattern, "partition": string(auth.PrincipalOf(r).Partition)})
		}
	}
	var stubs []string
	for p := range inv {
		if !core[p] {
			srv.RegisterAPI(p, ran(p))
			stubs = append(stubs, p)
		}
	}
	probe := "GET " + probePattern
	srv.RegisterAPI(probe, ran(probe))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	a.RegisterInstancePartition("inst-alice", "apps/pt", "", "user:alice", "uid-a")
	a.RegisterInstance("inst-global", "apps/pt")
	a.RegisterInstance("inst-other", "apps/other")
	type cred struct {
		name   string
		header http.Header
		user   bool // acts in a user partition
	}
	bearer := func(tok string) http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }
	frame := func(tok string) http.Header { return http.Header{auth.FrameTokenHeader: {tok}} }
	creds := []cred{
		{"alice's partition's instance", bearer("inst-alice"), true},
		{"alice's frame of apps/pt", frame(a.MintFrameToken("apps/pt", "alice", time.Hour)), true},
		{"alice's terminal on apps/pt", bearer(a.MintTerminal("apps/pt", "alice")), true},
		{"the owner's frame of apps/pt, global", frame(a.MintFrameToken("apps/pt", "", time.Hour)), false},
		{"the global instance", bearer("inst-global"), false},
		{"an unpartitioned tile's instance", bearer("inst-other"), false},
		{"alice's frame of an unpartitioned tile", frame(a.MintFrameToken("apps/other", "alice", time.Hour)), false},
		{"the owner", bearer(a.OwnerTokenValue()), false},
	}
	call := func(c cred, pattern string) (int, string) {
		t.Helper()
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			method, path = "GET", pattern
		}
		var segs []string
		for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
			switch {
			case strings.HasSuffix(s, "...}"):
				segs = append(segs, "x", "y")
			case strings.HasPrefix(s, "{"):
				segs = append(segs, "x")
			default:
				segs = append(segs, s)
			}
		}
		req, err := http.NewRequest(method, ts.URL+"/api/xbin/"+strings.Join(segs, "/"), strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range c.header {
			req.Header[k] = v
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.name, pattern, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	unconverted := server.PartitionUnconverted()
	for _, c := range creds {
		all := append([]string{probe}, stubs...)
		for p := range core { // the core API's own handlers need a daemon: only the refusals run
			if class := server.PartitionClassOf(p); c.user && (class == server.GlobalOnlyRefused || class == server.GlobalOnlyDormant ||
				class == server.PartitionScoped && unconverted[p] != "") {
				all = append(all, p)
			}
		}
		for _, pattern := range all {
			class := server.PartitionClassOf(pattern)
			code, body := call(c, pattern)
			want, wantCode := `"ran":"`+pattern+`"`, http.StatusOK
			switch {
			case !c.user:
			case class == server.PartitionScoped && unconverted[pattern] != "":
				want, wantCode = "this route isn't available to a partition's credentials yet ("+unconverted[pattern]+")", http.StatusForbidden
			case class == server.PartitionScoped, class == server.PartitionNeutral:
			case class == server.GlobalOnlyDormant:
				want, wantCode = "this route isn't available to a partition's credentials yet (dormant registrations)", http.StatusForbidden
			case class == server.GlobalOnlyRefused:
				want, wantCode = "this route is the global instance's alone: a person's partition (user:alice) can't use it", http.StatusForbidden
			default:
				want, wantCode = "this route isn't available to a partition's credentials yet", http.StatusForbidden
			}
			if wantCode == http.StatusOK {
				wantPart := ""
				if c.user {
					wantPart = "user:alice"
				}
				want = `"partition":"` + wantPart + `","ran":"` + pattern + `"`
			}
			if code != wantCode || !strings.Contains(body, want) {
				t.Errorf("%s → %s (%s): %d %.200s\n  want %d with %s", c.name, pattern, class, code, body, wantCode, want)
			}
		}
	}
}
