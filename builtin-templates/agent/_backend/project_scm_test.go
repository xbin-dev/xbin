package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

// The seeded token through a project's task, end to end: the project's
// workspace made with the fake provider's credential in the sandbox (the
// gate, the files, the git helper), a repo setup that prints it, and the
// task's own bash printing it — the cred file, git's credential helper, the
// gh config, an error. The token is in no row of any table, no log line, no
// stream event, no job's output, no answer of the task's routes and nothing
// sent to the model; what the commands printed is there, masked.
func TestSeededTokenNeverStoredTask(t *testing.T) {
	var logs bytes.Buffer
	var lmu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) { lmu.Lock(); defer lmu.Unlock(); return logs.Write(p) }))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	fx := newProjFix(t)
	fx.scm.prov.LongTokens = true // a 520-character installation token: no shape to go by
	hub := fx.ag.eng.hub
	list, _, _ := hub.subscribe(0, hub.now(), who{kind: whoUser, user: "alice", level: "read"})
	defer hub.unsubscribe(list)

	creds := fx.box.Home + "/.config/xbin-scm/*/github.com.cred"
	setup := fmt.Sprintf(`cat %s; echo "setup error: $(sed -n 2p %s)" >&2`, creds, creds)
	p := fx.newProject(t, asAlice, map[string]any{"repos": []map[string]any{{"repo": "acme/web", "setup": setup}}})
	cred := fx.box.Home + "/.config/xbin-scm/" + p.UID + "/github.com.cred"
	f := fakeOf(fx.ag)
	f.on(lastUser("leak it"), callTools(
		tc("c1", "bash", mustJSON(map[string]string{"command": "cat " + cred})),
		tc("c2", "bash", mustJSON(map[string]string{"command": `printf 'protocol=https\nhost=github.com\n\n' | git credential fill`})),
		tc("c3", "bash", mustJSON(map[string]string{"command": `cat "$GH_CONFIG_DIR/hosts.yml"; echo "error: bad $(sed -n 2p ` + cred + `)" >&2; exit 2`})),
	)).once()
	f.on(lastIs("tool", ""), say("done"))
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "leak it"})
	fx.waitWS(t, p.ID, 1, wsReady)
	waitFor(t, "the task to answer", func() bool { return strings.Contains(transcript(fx.ag.db, runID), "A:done") })
	waitJobsDone(t, fx, p.ID)
	waitQuiet(t, fx.ag)

	toks := fx.scm.prov.Tokens()
	if len(toks) == 0 {
		t.Fatal("the fake provider handed out no token")
	}
	// each token, and pieces of it (a masked prefix shouldn't hide the rest)
	var needles []string
	for _, tk := range toks {
		v := tk.Value
		needles = append(needles, v[:40], v[len(v)/2-20:len(v)/2+20], v[len(v)-40:])
	}

	// what the commands printed is there: the scan isn't vacuous
	all := fullText(fx.ag.db, runID)
	for _, want := range []string{"password=", "username=", "oauth_token", "error: bad"} {
		if !strings.Contains(all, want) {
			t.Fatalf("the commands didn't print %q: %s", want, clip(all, 3000))
		}
	}
	var setupOut string
	for _, j := range fx.ag.db.jobsWhere(`WHERE project_id=? AND kind=?`, p.ID, pjSetup) {
		setupOut += j.Out + j.Error
	}
	k, _ := fx.ag.db.taskByN(p.ID, 1)
	if !strings.Contains(setupOut+k.SetupTail, "setup error: password=") {
		t.Fatalf("the setup didn't print the credential: %q / %q", setupOut, k.SetupTail)
	}

	check := func(where, s string) {
		t.Helper()
		for _, n := range needles {
			if strings.Contains(s, n) {
				t.Errorf("the token is in %s", where)
				return
			}
		}
	}
	for _, n := range needles {
		if got := tablesHolding(t, fx.ag.db, n); len(got) != 0 {
			t.Errorf("the token is in table(s) %v", got)
			break
		}
	}
	// the stream: the task's tree (replayed from its ring) and the list's
	taskSub, evs, ok := hub.subscribe(runID, 0, who{kind: whoUser, user: "alice", level: "read"})
	defer hub.unsubscribe(taskSub)
	if !ok || len(evs) == 0 {
		t.Fatalf("the task's events aren't held to look at (%d, %v)", len(evs), ok)
	}
	list.mu.Lock()
	evs = append(evs, list.q...)
	list.mu.Unlock()
	b, _ := json.Marshal(evs)
	check("a stream event", string(b))
	// the task's answers, and what the model was sent
	for _, path := range []string{fmt.Sprintf("/runs/%d/view", runID), fmt.Sprintf("/runs/%d/task", runID),
		fmt.Sprintf("/projects/%d/tasks/1", p.ID), fmt.Sprintf("/projects/%d", p.ID)} {
		w := callAs(t, fx.mux, asAlice, "GET", path, nil)
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		check("GET "+path, w.Body.String())
	}
	sent, _ := json.Marshal(f.callsFor(runID))
	check("a model call", string(sent))
	lmu.Lock()
	defer lmu.Unlock()
	check("the log", logs.String())
	// and masked where it was printed
	if !strings.Contains(all, "password="+redactMark) || !strings.Contains(setupOut+k.SetupTail, "password="+redactMark) {
		t.Errorf("the credential isn't masked where it was printed: %s / %s", clip(all, 2000), clip(setupOut+k.SetupTail, 600))
	}
}
