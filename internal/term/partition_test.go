package term

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// partHooks is the broker's side for a manager whose apps/p is
// partitioned: a person acts in user:<id> with the partition id "k-<id>",
// the owner (no person) in global while global is on and without the tile
// API otherwise; refuse names people who reach no partition; switching
// refuses every session on apps/p. Every other tile isn't partitioned. A
// sub-path answers as its tile (apps/p or apps/u), as the registry resolves.
type partHooks struct {
	mu        sync.Mutex
	global    bool
	switching bool
	refuse    map[string]bool
	on        map[string]bool // tiles partitioned now (both hooks); apps/p unless set
}

// tileOf is the tile holding path, "" for none.
func tileOf(path string) string {
	for _, t := range []string{"apps/p", "apps/u"} {
		if path == t || strings.HasPrefix(path, t+"/") {
			return t
		}
	}
	return ""
}

// isOn: under h.mu.
func (h *partHooks) isOn(tile string) bool {
	if h.on != nil {
		return h.on[tile]
	}
	return tile == "apps/p"
}

func (h *partHooks) session(p auth.Principal, path, dep string) (Partition, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tile := tileOf(path)
	if tile == "apps/p" && h.switching {
		return Partition{}, fmt.Errorf("%w: apps/p is paused", ErrPartitionSwitching)
	}
	if !h.isOn(tile) {
		return Partition{Tile: tile}, nil
	}
	out := Partition{Partitioned: true, Tile: tile}
	switch {
	case p.UserID != "" && h.refuse[p.UserID]:
		return Partition{}, errors.New("apps/p: " + p.UserID + " can't read it")
	case p.UserID != "":
		out.Part, out.Key = "user:"+p.UserID, "k-"+p.UserID
	case h.global:
		out.Part = "global"
	default:
		out.NoAPI = "sign in as a person: " + tile + " keeps each person's data apart"
	}
	return out, nil
}

func (h *partHooks) partitioned(path string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tile := tileOf(path)
	return tile, h.isOn(tile)
}

func (h *partHooks) install(m *Manager) {
	m.SessionPartition = h.session
	m.TilePartitioned = h.partitioned
	m.PersonPartitionKey = func(id string) string { return "k-" + id }
}

func termPerson(id string, tiles ...string) auth.Principal {
	lv := map[string]string{}
	for _, t := range tiles {
		lv[t] = users.LevelTerminal
	}
	return auth.Principal{UserID: id, Via: "session", User: &users.User{ID: id, Role: users.RoleUser, Tiles: lv, TermAPI: true}}
}

func termAdmin(id string) auth.Principal {
	return auth.Principal{UserID: id, Via: "session", User: &users.User{ID: id, Role: users.RoleAdmin}}
}

// partManager is a host-shell manager (isolation off) over a workspace
// with apps/p (partitioned by h) and apps/u, and a recording token minter.
func partManager(t *testing.T, h *partHooks) (*Manager, *zsTokens) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"apps/p", "apps/u"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	toks := &zsTokens{}
	m := NewManager(root, func() []string { return []string{"XBIN_URL=http://127.0.0.1:8642"} })
	m.Tokens = toks
	h.install(m)
	t.Cleanup(func() {
		for _, s := range m.sorted() {
			s.kill()
		}
		for deadline := time.Now().Add(10 * time.Second); len(m.sorted()) > 0; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Errorf("sessions left: %v", m.List())
				return
			}
		}
	})
	return m, toks
}

// openShell opens a shell on tile for p through ServeWS (the upgrade then
// fails, which leaves the session running with no client) → its id, or
// the refusal.
func openShell(t *testing.T, m *Manager, p auth.Principal, tile, q string) (string, int, string) {
	t.Helper()
	before := map[string]bool{}
	for _, s := range m.sorted() {
		before[s.ID] = true
	}
	r := httptest.NewRequest("GET", "/ws/term?cwd="+tile+q, nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	m.ServeWS(w, r)
	for _, s := range m.sorted() {
		if !before[s.ID] {
			return s.ID, w.Code, w.Body.String()
		}
	}
	return "", w.Code, w.Body.String()
}

func sessionOf(m *Manager, id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// procEnv and procCwd read a live session's process, once it has exec'd
// (before, /proc shows the test binary's env and cwd).
func procEnv(t *testing.T, s *Session) []string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(s.cmd.Process.Pid) + "/environ")
		if err != nil {
			t.Skipf("no /proc here: %v", err)
		}
		env := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "XBIN_COMPONENT=") }) || time.Now().After(deadline) {
			return env
		}
	}
}

func procCwd(t *testing.T, s *Session) string {
	t.Helper()
	procEnv(t, s) // exec'd
	d, err := os.Readlink("/proc/" + strconv.Itoa(s.cmd.Process.Pid) + "/cwd")
	if err != nil {
		t.Skipf("no /proc here: %v", err)
	}
	return d
}

func envAfter(env []string, key string) (string, string) {
	for i, e := range env {
		if strings.HasPrefix(e, key+"=") {
			next := ""
			if i+1 < len(env) {
				next = env[i+1]
			}
			return e, next
		}
	}
	return "", ""
}

// covers PD-22 S16 — the env golden, partitioned vs not (06 §Tests term):
// a partitioned tile's session gets XBIN_PARTITION right after
// XBIN_COMPONENT (after XBIN_DEPLOYMENT when that is set), in the sandboxed
// env and the host shell's alike; a session without a partition, and every
// session of a tile that isn't partitioned, keeps today's env byte for byte.
func TestPartitionSessionEnvGolden(t *testing.T) {
	root := t.TempDir()
	m := &Manager{Root: root, Listen: "127.0.0.1:8642", Env: func() []string { return []string{"XBIN_URL=http://127.0.0.1:8642"} },
		sessions: map[string]*Session{}, envHeld: map[string]bool{}}
	today := zsEnvLines(m.sandboxEnv("apps/p", false, "/w/homes/ana", "T"))
	if got := zsEnvLines(m.sessionEnv("apps/p", false, "/w/homes/ana", "T", openOpts{})); got != today {
		t.Errorf("an unpartitioned session's env changed:\n%s\nwant\n%s", got, today)
	}
	if got := zsEnvLines(m.sessionEnv("apps/p", false, "/w/homes/ana", "T", openOpts{part: sessionPart{on: true, apiOff: true}})); got != today {
		t.Errorf("a partitioned session without a partition gains env:\n%s", got)
	}
	o := openOpts{part: sessionPart{on: true, part: "user:ana", key: "k-ana", tile: "apps/p"}}
	want := strings.Replace(today, "XBIN_COMPONENT=apps/p\n", "XBIN_COMPONENT=apps/p\nXBIN_PARTITION=user:ana\n", 1)
	if got := zsEnvLines(m.sessionEnv("apps/p", false, "/w/homes/ana", "T", o)); got != want {
		t.Errorf("a person's session:\n%s\nwant\n%s", got, want)
	}
	o.target = sessionTarget{Target: Target{Deployment: "dev"}, env: "dev"}
	o.part.part = "global"
	want = strings.Replace(today, "XBIN_COMPONENT=apps/p\n", "XBIN_COMPONENT=apps/p\nXBIN_DEPLOYMENT=dev\nXBIN_PARTITION=global\n", 1)
	if got := zsEnvLines(m.sessionEnv("apps/p", false, "/w/homes/ana", "T", o)); got != want {
		t.Errorf("a non-primary target's session:\n%s\nwant\n%s", got, want)
	}

	// the host shell (isolation off), through a real session
	h := &partHooks{}
	pm, _ := partManager(t, h)
	ana := termPerson("ana", "apps/p", "apps/u")
	id, _, body := openShell(t, pm, ana, "apps/p", "")
	if id == "" {
		t.Fatalf("ana's shell on apps/p: %s", body)
	}
	if e, next := envAfter(procEnv(t, sessionOf(pm, id)), "XBIN_COMPONENT"); e != "XBIN_COMPONENT=apps/p" || next != "XBIN_PARTITION=user:ana" {
		t.Errorf("host shell on apps/p: %q then %q", e, next)
	}
	id, _, body = openShell(t, pm, ana, "apps/u", "")
	if id == "" {
		t.Fatalf("ana's shell on apps/u: %s", body)
	}
	for _, e := range procEnv(t, sessionOf(pm, id)) {
		if strings.HasPrefix(e, "XBIN_PARTITION=") {
			t.Errorf("a shell on an unpartitioned tile has %s", e)
		}
	}
}

// covers PD-10 PD-22 S16 — opening sessions on a partitioned tile (06
// §Tests term): a person's shell starts in $HOME (an unpartitioned tile's
// in the tile), keeps its partition (the frame's partition and note, the
// row's partition), and its terminal token is minted as today; a session
// with no person on a tile without global opens with the API off (no
// token, api:false, the note says why), with global it reaches global; a
// person the broker refuses gets 403, a running switch 409.
func TestPartitionSessionOpen(t *testing.T) {
	h := &partHooks{}
	m, toks := partManager(t, h)
	ana := termPerson("ana", "apps/p", "apps/u")
	id, _, body := openShell(t, m, ana, "apps/p", "")
	if id == "" {
		t.Fatalf("ana's shell: %s", body)
	}
	s := sessionOf(m, id)
	if cwd, home := procCwd(t, s), HomeDir(m.Root, "ana"); cwd != home {
		t.Errorf("ana's shell on apps/p starts in %s, want her $HOME %s", cwd, home)
	}
	hello := s.helloFields("")
	// a host shell (isolation off) reads every partition: its note says so
	if hello["partition"] != "user:ana" || !strings.Contains(hello["partitionNote"].(string), "no isolation") || hello["api"] != nil {
		t.Errorf("the session frame: %v", hello)
	}
	// … a sandboxed one's promises its own partition (the note alone: no
	// sandbox is started here)
	iso := &Manager{Root: m.Root, Isolate: true}
	h.install(iso)
	io := openOpts{api: true}
	if code, err := iso.pickPartition(ana, &io, "apps/p"); err != nil || !strings.Contains(io.part.note, "partition: yours (user:ana)") {
		t.Errorf("the isolated note: %d %v %q", code, err, io.part.note)
	}
	iso.partOpened(&io)
	if row, _ := m.Info(id); row.Partition != "user:ana" {
		t.Errorf("the row's partition: %q", row.Partition)
	}
	if got := strings.Join(toks.minted, ","); got != "apps/p|ana" {
		t.Errorf("tokens minted: %s", got)
	}
	uid, _, _ := openShell(t, m, ana, "apps/u", "")
	if cwd := procCwd(t, sessionOf(m, uid)); cwd != filepath.Join(m.Root, "apps", "u") {
		t.Errorf("a shell on an unpartitioned tile starts in %s", cwd)
	}
	if hello := sessionOf(m, uid).helloFields(""); hello["partition"] != nil || hello["partitionNote"] != nil || hello["api"] != nil {
		t.Errorf("an unpartitioned tile's frame gained fields: %v", hello)
	}

	// the owner (no person), no global: the API is off, nothing minted
	toks.minted = nil
	owner := auth.Principal{Owner: true}
	id, _, body = openShell(t, m, owner, "apps/p", "")
	if id == "" {
		t.Fatalf("the owner's shell: %s", body)
	}
	s = sessionOf(m, id)
	hello = s.helloFields("")
	if hello["api"] != false || !strings.Contains(hello["partitionNote"].(string), "tile API off: sign in as a person") || hello["partition"] != nil || s.api || len(toks.minted) != 0 {
		t.Errorf("the owner's session without global: frame %v, api %v, minted %v", hello, s.api, toks.minted)
	}
	for _, e := range procEnv(t, s) {
		if strings.HasPrefix(e, "XBIN_TOKEN=") || strings.HasPrefix(e, "XBIN_PARTITION=") {
			t.Errorf("the owner's API-less session has %s", e)
		}
	}
	// … with global: it reaches global, with a token
	h.mu.Lock()
	h.global = true
	h.mu.Unlock()
	id, _, _ = openShell(t, m, owner, "apps/p", "")
	if s = sessionOf(m, id); s.part.part != "global" || !s.api || len(toks.minted) != 1 {
		t.Errorf("the owner's session with global: %+v, api %v, minted %v", s.part, s.api, toks.minted)
	}
	if e, next := envAfter(procEnv(t, s), "XBIN_COMPONENT"); next != "XBIN_PARTITION=global" {
		t.Errorf("the owner's global session: %q then %q", e, next)
	}

	// refused person: 403; a running switch: 409 — no session either way
	h.mu.Lock()
	h.refuse = map[string]bool{"bob": true}
	h.mu.Unlock()
	if id, code, body := openShell(t, m, termPerson("bob", "apps/p"), "apps/p", ""); id != "" || code != 403 || !strings.Contains(body, "can't read it") {
		t.Errorf("a refused person: %q %d %s", id, code, body)
	}
	h.mu.Lock()
	h.switching = true
	h.mu.Unlock()
	if id, code, _ := openShell(t, m, ana, "apps/p", ""); id != "" || code != 409 {
		t.Errorf("during a switch: %q %d", id, code)
	}
	if buildErr == nil {
		m.BxPath = bxBin
		if _, code, err := m.OpenAgentWith(ana, AgentOpen{Cwd: "apps/p", Provider: "fake"}); code != 409 || !errors.Is(err, ErrPartitionSwitching) {
			t.Errorf("an agent session during a switch: %d %v", code, err)
		}
	}
}

// covers PD-22 — the layer is per person on a partitioned tile (06 §Tests
// term): each person's own key under .xbin/term-part/<TileKey>/, never the
// tile's (.xbin/term/<CompKey>), which a session with no person keeps.
func TestPartitionLayerPerPerson(t *testing.T) {
	m := &Manager{Root: "/w"}
	ana := openOpts{part: sessionPart{on: true, tile: "apps/p", part: "user:ana", key: "u-aa"}}
	bob := openOpts{part: sessionPart{on: true, tile: "apps/p", part: "user:bob", key: "u-bb"}}
	glob := openOpts{part: sessionPart{on: true, tile: "apps/p", part: "global"}}
	ka, kb, kg := ana.layerKey("apps/p"), bob.layerKey("apps/p"), glob.layerKey("apps/p")
	if ka == kb || ka == termKey("apps/p") {
		t.Fatalf("people share a layer: %s %s", ka, kb)
	}
	if kg != termKey("apps/p") || (openOpts{}).layerKey("apps/p") != termKey("apps/p") {
		t.Errorf("the tile's own layer: %s", kg)
	}
	if got, want := m.layerDir(ka), filepath.Join("/w", ".xbin", "term-part", util.TileKey("apps/p"), "u-aa"); got != want {
		t.Errorf("ana's layer at %s, want %s", got, want)
	}
	if got, want := m.layerDir(kg), filepath.Join("/w", ".xbin", "term", util.CompKey("apps/p")); got != want {
		t.Errorf("the tile's layer at %s, want %s", got, want)
	}
	// the reset and status routes act on the caller's own layer there
	h := &partHooks{}
	h.install(m)
	if got := m.callerLayer(termPerson("ana"), "apps/p"); got != partLayerKey("apps/p", "k-ana") {
		t.Errorf("ana's reset key on apps/p: %s", got)
	}
	if got := m.callerLayer(termPerson("ana"), "apps/u"); got != termKey("apps/u") {
		t.Errorf("ana's reset key on apps/u: %s", got)
	}
	if got := m.callerLayer(auth.Principal{Owner: true}, "apps/p"); got != termKey("apps/p") {
		t.Errorf("the owner's reset key on apps/p: %s", got)
	}
}

// covers PD-09 S2 — admins and other people's sessions (06 §3, §Tests
// term/server): on a partitioned tile an admin can't reattach to, drive or
// rename another person's session, but may end it; their own session, and
// every session of a tile that isn't partitioned, are as today. A tile
// partitioned after the session opened counts too.
func TestPartitionAdminGates(t *testing.T) {
	h := &partHooks{on: map[string]bool{"apps/p": true}}
	m := NewManager(t.TempDir(), nil)
	h.install(m)
	part := stub(m, "p1", "ana", "apps/p", time.Now())
	part.part = sessionPart{on: true, tile: "apps/p", part: "user:ana", key: "k-ana"}
	stub(m, "u1", "ana", "apps/u", time.Now())
	own := stub(m, "p2", "root", "apps/p", time.Now())
	own.part = sessionPart{on: true, tile: "apps/p", part: "user:root", key: "k-root"}
	admin := termAdmin("root")
	if why := part.mayReattach(admin, m.partitioned(part)); !strings.Contains(why, "keeps each person's data apart") {
		t.Errorf("an admin reattaching to ana's partition session: %q", why)
	}
	if err := m.MayDrive("p1", admin); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "keeps each person's data apart") {
		t.Errorf("an admin driving ana's partition session: %v", err)
	}
	if err := m.MayKill("p1", admin); err != nil {
		t.Errorf("an admin ending ana's partition session: %v", err)
	}
	if m.MayRename("p1", admin) {
		t.Error("an admin renames ana's partition session")
	}
	if err := m.MayDrive("p1", termPerson("ana", "apps/p")); err != nil {
		t.Errorf("ana driving her own: %v", err)
	}
	if err := m.MayKill("p1", termPerson("bob", "apps/p")); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob ending ana's: %v", err)
	}
	for _, id := range []string{"u1", "p2"} {
		s := sessionOf(m, id)
		if why := s.mayReattach(admin, m.partitioned(s)); why != "" {
			t.Errorf("%s: an admin's reattach refused: %s", id, why)
		}
		if err := m.MayDrive(id, admin); err != nil {
			t.Errorf("%s: an admin's drive refused: %v", id, err)
		}
		if !m.MayRename(id, admin) {
			t.Errorf("%s: an admin's rename refused", id)
		}
	}
	// a tile partitioned since the session opened: no admin pass either
	h.mu.Lock()
	h.on["apps/u"] = true
	h.mu.Unlock()
	if err := m.MayDrive("u1", admin); !errors.Is(err, ErrForbidden) {
		t.Errorf("an admin driving a session on a tile partitioned since: %v", err)
	}
	// GET /status's listing: a person's session there has no name — in
	// their partition, or in global targeting a non-primary deployment
	// (its layer and history are still theirs)
	dev := stub(m, "p3", "ana", "apps/p", time.Now())
	dev.part = sessionPart{on: true, tile: "apps/p", part: "global", key: "k-ana"}
	m.Rename("p1", "dump the payroll")
	m.Rename("p3", "payroll on dev")
	for _, row := range m.List() {
		switch row["id"] {
		case "p1", "p3":
			if row["name"] != "" || row["partition"] == nil {
				t.Errorf("the status row of ana's session on apps/p: %v", row)
			}
		}
	}
	if info, _ := m.Info("p3"); !info.Personal() || info.Partition != "global" {
		t.Errorf("ana's non-primary session: %+v personal %v", info, info.Personal())
	}
	if info, _ := m.Info("u1"); info.Personal() {
		t.Error("a session on an unpartitioned tile reads personal")
	}

	// view-as (an admin looking as ana, read-only) opens no session on a
	// partitioned tile — ana's own included (PD-08); elsewhere as today
	viewAna := termPerson("ana", "apps/p", "apps/u")
	viewAna.Impersonator = "root"
	if err := m.MayDrive("p1", viewAna); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "viewing as someone") {
		t.Errorf("view-as driving ana's partition session: %v", err)
	}
	if why := part.mayReattach(viewAna, m.partitioned(part)); !strings.Contains(why, "viewing as someone") {
		t.Errorf("view-as reattaching to ana's partition session: %q", why)
	}
	h.mu.Lock()
	h.on["apps/u"] = false
	h.mu.Unlock()
	if err := m.MayDrive("u1", viewAna); err != nil {
		t.Errorf("view-as on an unpartitioned tile's session (today's pass): %v", err)
	}
}

// covers PD-22 PD-43 — partition agent history (06 §2, §Tests term): an
// agent session on a partitioned tile starts in $HOME and saves its
// transcript under data/agent-history/.partitions/<pkey>/<TileKey>/, not
// the person's own history; the history routes merge it in (list, read,
// resume); another partition id (a recreated person) reads none of it.
func TestPartitionAgentHistory(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	if err := os.MkdirAll(filepath.Join(r.root, "apps", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &partHooks{}
	h.install(m)
	keys := map[string]string{"ana": "k-ana"}
	var mu sync.Mutex
	m.PersonPartitionKey = func(id string) string { mu.Lock(); defer mu.Unlock(); return keys[id] }
	ana := termPerson("ana", "apps/p")
	info, code, err := m.OpenAgent(ana, "apps/p", "", "fake", "", "mine", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	if info.Partition != "user:ana" {
		t.Errorf("the row's partition: %q", info.Partition)
	}
	<-r.change
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if cwd, home := procCwd(t, sessionOf(m, info.ID)), HomeDir(m.Root, "ana"); cwd != home {
		t.Errorf("the agent host starts in %s, want $HOME %s", cwd, home)
	}
	if _, err := m.AgentPrompt(context.Background(), info.ID, "secret plan"); err != nil {
		t.Fatal(err)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	m.Kill(info.ID)
	for op := ""; op != "close:"+info.ID; op = <-r.change {
	}
	want := filepath.Join(m.Root, "data", "agent-history", ".partitions", "k-ana", util.TileKey("apps/p"), info.ID+".json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the partition history file: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(m.Root, "data", "agent-history", "ana")); len(ents) != 0 {
		t.Errorf("the partition session wrote into ana's own history: %v", ents)
	}
	if hist := m.ListHistory(OwnHistory("ana"), "apps/p", nil); len(hist) != 1 || hist[0].ID != info.ID || hist[0].Preview != "secret plan" {
		t.Errorf("ana's history of apps/p: %+v", hist)
	}
	if hist := m.ListHistory(OwnHistory("ana"), "", nil); len(hist) != 1 {
		t.Errorf("ana's whole history: %+v", hist)
	}
	if _, evs, err := m.ReadHistory(OwnHistory("ana"), info.ID); err != nil || len(evs) == 0 {
		t.Errorf("read back: %v", err)
	}
	// a sub-path narrows to its own entries, as ana's own history does
	if hist := m.ListHistory(OwnHistory("ana"), "apps/p/src", nil); len(hist) != 0 {
		t.Errorf("apps/p/src lists apps/p's partition entries: %+v", hist)
	}
	// an admin viewing as ana reads none of it (PD-08, PD-09)
	viewAs := HistoryScope{Home: "ana", ViewAs: true}
	if hist := m.ListHistory(viewAs, "", nil); len(hist) != 0 {
		t.Errorf("view-as lists ana's partition history: %+v", hist)
	}
	if _, _, err := m.ReadHistory(viewAs, info.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("view-as reads ana's partition transcript: %v", err)
	}
	if err := m.DeleteHistory(viewAs, info.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("view-as deletes ana's partition transcript: %v", err)
	}
	// resume reopens it, in the partition again
	info2, code, err := m.OpenAgent(ana, "apps/p", "", "", "", "", info.ID, nil)
	if err != nil || code != 200 || info2.Name != "mine" {
		t.Fatalf("resume: %d %v %+v", code, err, info2)
	}
	<-r.change
	m.Kill(info2.ID)
	for op := ""; op != "close:"+info2.ID; op = <-r.change {
	}
	// a resume never crosses stores: the partition's entry doesn't continue
	// once apps/p stops keeping people apart (its continuation would land
	// in ana's own history, which outlives the partition), nor an entry of
	// ana's own history inside her partition
	h.mu.Lock()
	h.on = map[string]bool{"apps/p": false}
	h.mu.Unlock()
	if _, code, err := m.OpenAgent(ana, "apps/p", "", "", "", "", info.ID, nil); code != 409 || err == nil || !strings.Contains(err.Error(), "start a new one") {
		t.Errorf("resuming a partition entry outside the partition: %d %v", code, err)
	}
	h.mu.Lock()
	h.on = nil
	h.mu.Unlock()
	own := filepath.Join(m.Root, "data", "agent-history", "ana", util.CompKey("apps/p"))
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "0ld0.json"), []byte(`{"meta":{"id":"0ld0","cwd":"apps/p","provider":"fake","created":"x","ended":"x","turns":1,"acpSessionId":"s","loadable":true},"events":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code, err := m.OpenAgent(ana, "apps/p", "", "", "", "", "0ld0", nil); code != 409 || err == nil || !strings.Contains(err.Error(), "before the tile kept") {
		t.Errorf("resuming an own entry inside the partition: %d %v", code, err)
	}
	if n := m.inFlight(func(sessionPart) bool { return true }); n != 0 {
		t.Errorf("refused opens left %d in flight", n)
	}
	if err := os.RemoveAll(own); err != nil {
		t.Fatal(err)
	}
	// a recreated ana (a new uid: a new partition id) reads none of it
	mu.Lock()
	keys["ana"] = "k-ana2"
	mu.Unlock()
	if hist := m.ListHistory(OwnHistory("ana"), "", nil); len(hist) != 0 {
		t.Errorf("a new incarnation inherits the partition history: %+v", hist)
	}
	if _, err := m.HistoryMeta(OwnHistory("ana"), info.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("a new incarnation reads an old entry: %v", err)
	}
}

// covers PD-22 — a mode switch's side of terminals (01 §2.5-§2.6): the
// tile's sessions end and are waited for; the dry run counts the person
// layers and partition histories, the wipe removes them (layers confined)
// and names their partition ids; the tile's own layer, people's own
// history and another tile's are kept.
func TestPartitionWipeAndStop(t *testing.T) {
	h := &partHooks{}
	m, _ := partManager(t, h)
	var removed []string
	m.rmTree = func(dir string) error { removed = append(removed, dir); return os.RemoveAll(dir) }
	mk := func(parts ...string) string {
		d := filepath.Join(append([]string{m.Root}, parts...)...)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	tk, uk := util.TileKey("apps/p"), util.TileKey("apps/u")
	mk(".xbin", "term-part", tk, "k-ana", "upper")
	mk(".xbin", "term-part", tk, "k-bob", "upper")
	otherLayer := mk(".xbin", "term-part", uk, "k-ana", "upper")
	tileLayer := mk(".xbin", "term", util.CompKey("apps/p"), "upper")
	mk("data", "agent-history", ".partitions", "k-ana", tk)
	mk("data", "agent-history", ".partitions", "k-cid", tk)
	otherHist := mk("data", "agent-history", ".partitions", "k-ana", uk)
	ownHist := mk("data", "agent-history", "ana", util.CompKey("apps/p"))

	dry, err := m.WipePartitionTile("apps/p", true)
	if err != nil || dry.Layers != 2 || dry.Histories != 2 || !slices.Equal(dry.Keys, []string{"k-ana", "k-bob", "k-cid"}) {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if len(removed) != 0 {
		t.Fatal("the dry run removed something")
	}

	// sessions: apps/p's end (all three — one on a sub-path, opened before
	// the tile was partitioned), apps/u's stays
	ana := termPerson("ana", "apps/p", "apps/u", "apps/p/src")
	mk("apps", "p", "src")
	h.mu.Lock()
	h.on = map[string]bool{"apps/p": false}
	h.mu.Unlock()
	early, _, _ := openShell(t, m, ana, "apps/p/src", "")
	h.mu.Lock()
	h.on["apps/p"] = true
	h.mu.Unlock()
	p1, _, _ := openShell(t, m, ana, "apps/p", "")
	p2, _, _ := openShell(t, m, auth.Principal{Owner: true}, "apps/p", "")
	u1, _, _ := openShell(t, m, ana, "apps/u", "")
	if early == "" || p1 == "" || p2 == "" || u1 == "" {
		t.Fatal("sessions didn't open")
	}
	if s := sessionOf(m, early); s.part.on || s.part.tile != "apps/p" {
		t.Fatalf("the sub-path session's partition: %+v", s.part)
	}
	if err := m.StopTileSessions("apps/p"); err != nil {
		t.Fatal(err)
	}
	if sessionOf(m, early) != nil || sessionOf(m, p1) != nil || sessionOf(m, p2) != nil || sessionOf(m, u1) == nil {
		t.Fatalf("after the stop: sub-path %v p1 %v p2 %v u1 %v", sessionOf(m, early) != nil, sessionOf(m, p1) != nil, sessionOf(m, p2) != nil, sessionOf(m, u1) != nil)
	}

	got, err := m.WipePartitionTile("apps/p", false)
	if err != nil || got.Layers != 2 || got.Histories != 2 {
		t.Fatalf("wipe: %+v %v", got, err)
	}
	if len(removed) != 2 {
		t.Errorf("layers removed confined: %v", removed)
	}
	if _, err := os.Stat(filepath.Join(m.Root, ".xbin", "term-part", tk)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("apps/p's person layers survived: %v", err)
	}
	for _, d := range []string{filepath.Join(m.Root, "data", "agent-history", ".partitions", "k-ana", tk), filepath.Join(m.Root, "data", "agent-history", ".partitions", "k-cid", tk)} {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived: %v", d, err)
		}
	}
	for _, d := range []string{otherLayer, tileLayer, otherHist, ownHist} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s went: %v", d, err)
		}
	}
	if again, err := m.WipePartitionTile("apps/p", false); err != nil || again.Layers+again.Histories != 0 {
		t.Errorf("a second wipe: %+v %v", again, err)
	}
}

// covers S13 — a tile whose recorded mode gains or loses user partitions
// without a switch ends the sessions opened under the other mode; those
// opened under the new one stay.
func TestPartitionModeChangedEndsStaleSessions(t *testing.T) {
	h := &partHooks{}
	m, _ := partManager(t, h)
	if err := os.MkdirAll(filepath.Join(m.Root, "apps", "u", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	ana := termPerson("ana", "apps/p", "apps/u", "apps/u/src")
	inP, _, _ := openShell(t, m, ana, "apps/p", "")
	inU, _, _ := openShell(t, m, ana, "apps/u", "")
	inUSub, _, _ := openShell(t, m, ana, "apps/u/src", "") // a sub-path: its tile's session too
	if n := m.PartitionModeChanged("apps/p", true); n != 0 {
		t.Errorf("apps/p stays partitioned: %d ended", n)
	}
	if n := m.PartitionModeChanged("apps/u", true); n != 2 {
		t.Errorf("apps/u became partitioned: %d ended, want 2 (one on a sub-path)", n)
	}
	if n := m.PartitionModeChanged("apps/p", false); n != 1 {
		t.Errorf("apps/p stopped being partitioned: %d ended, want 1", n)
	}
	deadline := time.Now().Add(5 * time.Second)
	for sessionOf(m, inP) != nil || sessionOf(m, inU) != nil || sessionOf(m, inUSub) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the stale sessions didn't end")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// covers PD-22 PD-26 — one partition's end (F7b's reset, purge and a
// deleted person's sweep): its sessions on the tile stop — nobody else's —
// and its person layer and partition history go, of one tile or of every
// tile; other people's stay.
func TestPartitionKeyWipe(t *testing.T) {
	h := &partHooks{}
	m, _ := partManager(t, h)
	m.rmTree = os.RemoveAll
	mk := func(parts ...string) string {
		d := filepath.Join(append([]string{m.Root}, parts...)...)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	tp, tu := util.TileKey("apps/p"), util.TileKey("apps/u")
	anaP := mk(".xbin", "term-part", tp, "k-ana", "upper")
	anaU := mk(".xbin", "term-part", tu, "k-ana", "upper")
	bobP := mk(".xbin", "term-part", tp, "k-bob", "upper")
	anaHistP := mk("data", "agent-history", ".partitions", "k-ana", tp)
	anaHistU := mk("data", "agent-history", ".partitions", "k-ana", tu)
	bobHist := mk("data", "agent-history", ".partitions", "k-bob", tp)

	ana, bob := termPerson("ana", "apps/p"), termPerson("bob", "apps/p")
	a1, _, _ := openShell(t, m, ana, "apps/p", "")
	b1, _, _ := openShell(t, m, bob, "apps/p", "")
	if err := m.StopPartitionSessions("apps/p", "k-ana"); err != nil {
		t.Fatal(err)
	}
	if sessionOf(m, a1) != nil || sessionOf(m, b1) == nil {
		t.Fatalf("after ana's stop: ana's %v, bob's %v", sessionOf(m, a1) != nil, sessionOf(m, b1) != nil)
	}
	got, err := m.WipePartitionKey("apps/p", "k-ana", false)
	if err != nil || got.Layers != 1 || got.Histories != 1 {
		t.Fatalf("ana's apps/p: %+v %v", got, err)
	}
	for _, gone := range []string{anaP, anaHistP} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived", gone)
		}
	}
	for _, kept := range []string{anaU, bobP, anaHistU, bobHist} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s went: %v", kept, err)
		}
	}
	if got, err := m.WipePartitionKey("", "k-ana", true); err != nil || got.Layers != 1 || got.Histories != 1 {
		t.Errorf("ana's dry sweep: %+v %v", got, err)
	}
	if got, err := m.WipePartitionKey("", "k-ana", false); err != nil || got.Layers != 1 {
		t.Errorf("ana's sweep: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "data", "agent-history", ".partitions", "k-ana")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ana's partition history survived the sweep: %v", err)
	}
	if _, err := m.WipePartitionKey("", "../k", false); err == nil {
		t.Error("a path as a partition id")
	}
}

// covers PD-22 PD-26 — nothing opens between a partition's stop and its
// wipe: while HoldPartition holds it, its person's new sessions answer 409
// (on its tile, or on every tile for a "" hold) — nobody else's — and a
// stop waits for the opens already in flight to register or fail, so none
// slips past it and mounts the layer being removed.
func TestPartitionHold(t *testing.T) {
	h := &partHooks{on: map[string]bool{"apps/p": true, "apps/u": true}}
	m, _ := partManager(t, h)
	ana, bob := termPerson("ana", "apps/p", "apps/u"), termPerson("bob", "apps/p")
	release := m.HoldPartition("apps/p", "k-ana")
	if id, code, body := openShell(t, m, ana, "apps/p", ""); id != "" || code != 409 || !strings.Contains(body, "being reset or removed") {
		t.Errorf("ana's shell while her partition of apps/p ends: %q %d %s", id, code, body)
	}
	if buildErr == nil {
		m.BxPath = bxBin
		if _, code, err := m.OpenAgentWith(ana, AgentOpen{Cwd: "apps/p", Provider: "fake"}); code != 409 || !errors.Is(err, ErrPartitionEnding) {
			t.Errorf("ana's agent session while it ends: %d %v", code, err)
		}
	}
	if id, _, body := openShell(t, m, ana, "apps/u", ""); id == "" {
		t.Errorf("ana's shell on another tile: %s", body)
	}
	if id, _, body := openShell(t, m, bob, "apps/p", ""); id == "" {
		t.Errorf("bob's shell on apps/p: %s", body)
	}
	release()
	release() // once
	if id, _, body := openShell(t, m, ana, "apps/p", ""); id == "" {
		t.Errorf("ana's shell once released: %s", body)
	}
	every := m.HoldPartition("", "k-ana")
	if id, code, _ := openShell(t, m, ana, "apps/u", ""); id != "" || code != 409 {
		t.Errorf("ana's shell on apps/u under an every-tile hold: %q %d", id, code)
	}
	every()

	// an open in flight: the stop waits for it to register
	o := openOpts{part: sessionPart{on: true, tile: "apps/p", part: "user:ana", key: "k-ana"}}
	if !m.opening(&o) {
		t.Fatal("an open refused without a hold")
	}
	done := make(chan error, 1)
	go func() { done <- m.StopPartitionSessions("apps/p", "k-ana") }()
	select {
	case err := <-done:
		t.Fatalf("the stop returned (%v) with an open in flight", err)
	case <-time.After(300 * time.Millisecond):
	}
	m.partOpened(&o)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := m.inFlight(func(sessionPart) bool { return true }); n != 0 {
		t.Errorf("%d opens left in flight", n)
	}
}
