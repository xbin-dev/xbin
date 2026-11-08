package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// slowCreates wraps a manager whose creates outlast the agent's wait: lose
// creates are made but answered 504 unavailable (the sandbox exists, the
// answer is lost); while refuse is set a create is answered 504 and makes
// nothing, and a list is answered 504 too (the job can't look yet). Only the
// project's own creates are touched — its sandbox job's and its forks'
// (their clientIds), or those whose body holds match when it is set; made,
// when set, is called with each touched create's sandbox once the manager
// made it, before the answer (a test puts it in a state of its choosing).
type slowCreates struct {
	mu      sync.Mutex
	lose    int
	refuse  bool
	creates int // that reached the manager
	match   string
	made    func(id string)
}

func (s *slowCreates) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sbx/sandboxes") {
			s.mu.Lock()
			refuse := s.refuse
			s.mu.Unlock()
			if refuse { // the job may not look before the test has laid out what earlier tries left
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGatewayTimeout)
				fmt.Fprint(w, `{"error":"not yet","refusal":"unavailable"}`)
				return
			}
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/sbx/sandboxes") {
			h.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		match := s.match
		if match == "" {
			match = `"clientId":"agent:proj:`
		}
		if !strings.Contains(string(body), match) { // only the project's own
			h.ServeHTTP(w, r)
			return
		}
		s.mu.Lock()
		refuse, lose, made := s.refuse, s.lose > 0, s.made
		if lose {
			s.lose--
		}
		if !refuse {
			s.creates++
		}
		s.mu.Unlock()
		if !refuse {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			var box struct {
				ID string `json:"id"`
			}
			if made != nil && rec.Code < 300 && json.Unmarshal(rec.Body.Bytes(), &box) == nil && box.ID != "" {
				made(box.ID)
				// the answer as the sandbox is now
				g := r.Clone(r.Context())
				g.Method, g.Body, g.ContentLength = http.MethodGet, http.NoBody, 0
				g.URL.Path, g.URL.RawPath = r.URL.Path+"/"+box.ID, ""
				now := httptest.NewRecorder()
				h.ServeHTTP(now, g)
				if now.Code == http.StatusOK {
					rec.Body = now.Body
				}
			}
			if !lose {
				for k, v := range rec.Header() {
					w.Header()[k] = v
				}
				w.WriteHeader(rec.Code)
				_, _ = w.Write(rec.Body.Bytes())
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGatewayTimeout)
		fmt.Fprint(w, `{"error":"the create took too long","refusal":"unavailable"}`)
	})
}

// projectBoxes is the manager's sandboxes labelled with the project uid.
func (fx *projFix) projectBoxes(uid string) []*fsbBox {
	fx.m.mu.Lock()
	defer fx.m.mu.Unlock()
	var out []*fsbBox
	for _, b := range fx.m.boxes {
		if b.Labels[projectLabel] == uid {
			out = append(out, b)
		}
	}
	return out
}

func fastBackoff(t *testing.T) {
	old := jobBackoffBase
	jobBackoffBase = 50 * time.Millisecond
	t.Cleanup(func() { jobBackoffBase = old })
}

// A create whose answer is lost (a VM that takes longer than the agent
// waits) is not made again: the next try takes up the sandbox it made.
func TestProjectSandboxLostCreateTakenUp(t *testing.T) {
	fastBackoff(t)
	sc := &slowCreates{lose: 1}
	fx := newProjFixWith(t, sc.wrap)
	p := fx.newProject(t, asAlice, map[string]any{"sandbox": map[string]any{"new": map[string]any{"provider": "apps/cs"}}})
	fx.waitRepoReady(t, p.ID)
	q, _ := fx.ag.db.getProject(p.ID)
	boxes := fx.projectBoxes(p.UID)
	if len(boxes) != 1 || q.SandboxRef != sandboxRef("apps/cs", boxes[0].ID) || !q.SandboxMade {
		t.Fatalf("the project's sandboxes: %d, its ref %s (made %v), %d creates (jobs %s)", len(boxes), q.SandboxRef, q.SandboxMade, sc.creates, jobsDump(fx.ag.db, p.ID))
	}
	if sc.creates != 1 {
		t.Fatalf("%d creates for one sandbox", sc.creates)
	}
	if got := boxes[0].Labels[projectMadeLabel]; got != p.UID+".0" {
		t.Fatalf("its made label: %q", got)
	}
}

// What earlier builds left — a sandbox per try, labelled with the project
// alone — is taken up once (the newest running one), the rest deleted.
func TestProjectSandboxTwinsDeleted(t *testing.T) {
	fastBackoff(t)
	sc := &slowCreates{refuse: true}
	fx := newProjFixWith(t, sc.wrap)
	p := fx.newProject(t, asAlice, map[string]any{"sandbox": map[string]any{"new": map[string]any{"provider": "apps/cs"}}})
	var made []*sbxSandbox
	for i := 0; i < 3; i++ {
		b := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "web", Labels: map[string]string{projectLabel: p.UID}})
		made = append(made, b)
		time.Sleep(5 * time.Millisecond)
	}
	// not this project's: left alone
	other := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "other", Labels: map[string]string{projectLabel: "zzzzzz"}})
	sc.mu.Lock()
	sc.refuse = false
	sc.creates = 0
	sc.mu.Unlock()
	fx.waitRepoReady(t, p.ID)
	defer func() {
		if t.Failed() {
			t.Logf("jobs: %s\ntwins: %s", jobsDump(fx.ag.db, p.ID), fx.ag.db.getSetting(twinsKey(p.ID)))
		}
	}()
	hwait(t, "the twins deleted", func() bool { return len(fx.projectBoxes(p.UID)) == 1 })
	q, _ := fx.ag.db.getProject(p.ID)
	keep := fx.projectBoxes(p.UID)[0]
	if q.SandboxRef != sandboxRef("apps/cs", keep.ID) || !q.SandboxMade {
		t.Fatalf("the project's sandbox %s, the one kept %s", q.SandboxRef, keep.ID)
	}
	found := false
	for _, b := range made {
		found = found || b.ID == keep.ID
	}
	if !found || sc.creates != 0 {
		t.Fatalf("it should take one of those made (kept %s, %d creates)", keep.ID, sc.creates)
	}
	fx.m.mu.Lock()
	_, otherThere := fx.m.boxes[other.ID]
	fx.m.mu.Unlock()
	if !otherThere {
		t.Fatal("another project's sandbox was deleted")
	}
	hwait(t, "the sandbox job done", func() bool {
		js := fx.ag.db.jobsWhere(`WHERE project_id=? AND kind=? ORDER BY id DESC LIMIT 1`, p.ID, pjSandbox)
		return len(js) == 1 && js[0].State == "done"
	})
	if v := fx.ag.db.getSetting(twinsKey(p.ID)); v != "" {
		t.Fatalf("twins left: %s", v)
	}
}
