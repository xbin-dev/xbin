package broker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/util"
)

// ---- helpers ----

// archiveMember is one tar entry of an archive under test.
type archiveMember struct {
	name string
	mode int64
	body []byte
}

func readArchive(t *testing.T, body []byte) []archiveMember {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(body))
	var out []archiveMember
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out = append(out, archiveMember{h.Name, h.Mode, b})
	}
}

func writeArchive(t *testing.T, ms []archiveMember) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range ms {
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: m.mode, Size: int64(len(m.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(m.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// manifestJSON is an archive's backup.json for a test to write.
func manifestJSON(t *testing.T, m backup.Manifest) archiveMember {
	t.Helper()
	m.Schema = backup.Schema
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return archiveMember{backup.ManifestName, 0o644, b}
}

// testRecord is a valid deployment record of tile: main is the primary,
// pinned to tree (live reload paused), or following the work tree when
// tree is "".
func testRecord(tile, owner, tree string) []byte {
	cp, live := "null", `"main"`
	if tree != "" {
		cp, live = `"`+tree+`"`, `""`
	}
	return []byte(fmt.Sprintf(`{
  "schema": 1,
  "tile": %q,
  "owner": %q,
  "created": "2026-09-27T10:12:03Z",
  "seq": 3,
  "liveReload": %s,
  "lastLiveReload": "main",
  "primary": "main",
  "protectedPrimary": false,
  "nextDeploy": 1,
  "deployments": {"main": {"checkpoint": %s, "created": "2026-09-27T10:12:03Z", "by": "user:ana"}},
  "future": {"kept": true}
}
`, tile, owner, live, cp))
}

func writeTestRecord(t *testing.T, root, tile string, data []byte) {
	t.Helper()
	dir := filepath.Join(root, "data", "deployments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, util.TileKey(tile)+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// needStoreGit skips a test that builds a real checkpoint store on a host
// without git or GNU find (the store's direct-mode runs use both).
func needStoreGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host")
	}
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !bytes.Contains(out, []byte("GNU")) {
		t.Skip("no GNU find on this host")
	}
	if confine.Isolated() {
		t.Fatal("confinement is on in a direct-mode test")
	}
}

// captureStore checkpoints tile's work tree into its store, creating it as
// a committed opt-in does, and returns the checkpoint's tree id.
func captureStore(t *testing.T, root, tile string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := checkpoint.New(root).Capture(ctx, checkpoint.CaptureRequest{
		Source: checkpoint.Source{Tile: tile, WorkTree: filepath.Join(root, filepath.FromSlash(tile)), Nested: []string{"widget"}},
		By:     "user:ana", Create: true})
	if err != nil {
		t.Fatalf("capture of %s: %v", tile, err)
	}
	return res.Hash
}

// putCall is what a recording restorer saw of the staged repository.
type putCall struct {
	tile   string
	record []byte
	refs   map[string]string
	files  []string // the stage's files, relative
	head   string
}

// recordingRestorer records each call; the stage is gone once it returns.
func recordingRestorer(t *testing.T, calls *[]putCall) deploymentRestorer {
	return func(ctx context.Context, tile string, record []byte, objects string, refs map[string]string) error {
		c := putCall{tile: tile, record: record, refs: refs}
		if objects != "" {
			_ = filepath.WalkDir(objects, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(objects, p)
					c.files = append(c.files, filepath.ToSlash(rel))
				}
				return nil
			})
			b, _ := os.ReadFile(filepath.Join(objects, "HEAD"))
			c.head = string(b)
		}
		*calls = append(*calls, c)
		return nil
	}
}

// gitOut runs the host's git for a fixture, with stdin as its input.
func gitOut(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

var createdField = regexp.MustCompile(`"created": "[^"]*"`)

// ---- tests ----

// covers P5 P29 T11 SC-AUDIT — (M1: record, store) a tile's backup carries
// its deployment state: with a deployment record, the main archive gains
// the record verbatim and the checkpoint store's git data (refs before
// objects, every object, never its config, hooks, info or index) right
// after backup.json, and the manifest gains its deployment section at
// schema 1; every other member is today's, in today's order. Without a
// record — a leftover store after an opt-out, a record made for another
// owner, one naming another tile — the archive is today's, unchanged. The
// restore reads the sections back and hands the plane the record and a
// staged repository of the objects and the refs, by their restore-only
// names.
func TestBackupCoversDeployments(t *testing.T) {
	needStoreGit(t)
	b := zeroDataBroker(t)
	b.Version = "test"
	root := b.Reg.Root
	arch := &zeroDataArchiver{}
	b.ProxyHandler = arch
	backupOf := func(comp string) []archiveMember {
		t.Helper()
		if v, err := b.doBackup(comp); err != nil || v != "v1" {
			t.Fatalf("backup %s: %q %v", comp, v, err)
		}
		arch.mu.Lock()
		body := append([]byte(nil), arch.body...)
		arch.mu.Unlock()
		ms := readArchive(t, body)
		if len(ms) == 0 || ms[0].name != backup.ManifestName {
			t.Fatalf("backup %s: backup.json isn't first", comp)
		}
		ms[0].body = createdField.ReplaceAll(ms[0].body, []byte(`"created": "<masked>"`))
		return ms
	}
	same := func(what string, got, want []archiveMember) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the archive isn't today's\n got: %s\nwant: %s", what, names(got), names(want))
		}
	}

	base, baseWidget := backupOf("apps/cal"), backupOf("apps/cal/widget")
	tree := captureStore(t, root, "apps/cal")
	same("a leftover store without a record", backupOf("apps/cal"), base)
	writeTestRecord(t, root, "apps/cal", testRecord("apps/cal", "user:someone-else", tree))
	same("a record made for another owner", backupOf("apps/cal"), base)
	writeTestRecord(t, root, "apps/cal", testRecord("apps/other", "", tree))
	same("a record naming another tile", backupOf("apps/cal"), base)

	rec := testRecord("apps/cal", b.ownerRef("apps/cal"), tree)
	writeTestRecord(t, root, "apps/cal", rec)
	got := backupOf("apps/cal")
	same("a nested tile without a record", backupOf("apps/cal/widget"), baseWidget)

	// the manifest: today's plus the section, schema 1
	var gm, bm map[string]any
	if err := json.Unmarshal(got[0].body, &gm); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(base[0].body, &bm); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"record": true, "checkpoints": true}; !reflect.DeepEqual(gm["deployments"], want) {
		t.Errorf("the manifest's deployment section: %v, want %v", gm["deployments"], want)
	}
	delete(gm, "deployments")
	if !reflect.DeepEqual(gm, bm) || gm["schema"] != float64(1) {
		t.Errorf("the manifest changed beyond its section:\n got: %s\nwant: %s", got[0].body, base[0].body)
	}
	if !bytes.HasSuffix(bytes.TrimSpace(got[0].body), []byte("\"deployments\": {\n    \"record\": true,\n    \"checkpoints\": true\n  }\n}")) {
		t.Errorf("the section isn't the manifest's last field:\n%s", got[0].body)
	}
	// the record, verbatim, right after the manifest
	if len(got) < 2 || got[1].name != backup.RecordName || got[1].mode != 0o600 || !bytes.Equal(got[1].body, rec) {
		t.Fatalf("member 2 isn't the record, verbatim: %s", names(got[1:2]))
	}
	// the store's git data, refs before objects
	store := checkpointStoreDir(root, "apps/cal")
	var storeMembers []string
	i := 2
	for ; i < len(got) && strings.HasPrefix(got[i].name, backup.CheckpointsPrefix); i++ {
		storeMembers = append(storeMembers, strings.TrimPrefix(got[i].name, backup.CheckpointsPrefix))
	}
	seenObject := false
	for _, rel := range storeMembers {
		isObject := strings.HasPrefix(rel, "objects/")
		switch {
		case isObject:
			seenObject = true
		case seenObject:
			t.Errorf("ref %s comes after an object", rel)
		}
		if !isObject && rel != "packed-refs" && !strings.HasPrefix(rel, "refs/") {
			t.Errorf("the archive carries the store's %s", rel)
		}
	}
	var wantObjects []string
	_ = filepath.WalkDir(filepath.Join(store, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.Contains(p, string(filepath.Separator)+"info"+string(filepath.Separator)) {
			rel, _ := filepath.Rel(store, p)
			wantObjects = append(wantObjects, filepath.ToSlash(rel))
		}
		return nil
	})
	var gotObjects []string
	for _, rel := range storeMembers {
		if strings.HasPrefix(rel, "objects/") {
			gotObjects = append(gotObjects, rel)
		}
	}
	sort.Strings(wantObjects)
	if len(wantObjects) == 0 || !reflect.DeepEqual(gotObjects, wantObjects) {
		t.Errorf("archived objects %v, the store has %v", gotObjects, wantObjects)
	}
	refs := map[string]string{}
	for j := 2; j < i; j++ {
		if rel := strings.TrimPrefix(got[j].name, backup.CheckpointsPrefix); strings.HasPrefix(rel, "refs/") {
			refs[rel] = strings.TrimSpace(string(got[j].body))
		}
	}
	cp, view := refs["refs/xbin/checkpoints/"+tree], refs["refs/xbin/views/"+tree]
	if !objectID(cp) || !objectID(view) {
		t.Fatalf("the archive lacks the checkpoint's refs: %v", refs)
	}
	// then today's members, in today's order
	same("the members after the deployment state", got[i:], base[1:])

	// the restore reads it back and hands it to the plane
	var calls []putCall
	body := writeArchive(t, got)
	if _, err := b.restore("apps/cal", bytes.NewReader(body), recordingRestorer(t, &calls)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("the plane was called %d times", len(calls))
	}
	c := calls[0]
	wantRefs := map[string]string{"refs/xbin/restored/checkpoints/" + tree: cp, "refs/xbin/restored/views/" + tree: view}
	if c.tile != "apps/cal" || !bytes.Equal(c.record, rec) || !reflect.DeepEqual(c.refs, wantRefs) {
		t.Errorf("the plane got tile %q, record %q, refs %v; want the record and %v", c.tile, c.record, c.refs, wantRefs)
	}
	staged := append([]string{"HEAD"}, wantObjects...)
	for ref := range wantRefs {
		staged = append(staged, ref)
	}
	sort.Strings(staged)
	sort.Strings(c.files)
	if !reflect.DeepEqual(c.files, staged) || c.head != "ref: refs/heads/restore\n" {
		t.Errorf("the staged repository holds %v (HEAD %q), want %v", c.files, c.head, staged)
	}
	if _, err := os.Lstat(filepath.Join(root, ".xbin", "restore")); err == nil {
		t.Error("the staged repository outlived the restore")
	}
	// an xbind whose plane can't take it restores the rest, as today
	if _, err := b.restore("apps/cal", bytes.NewReader(body), nil); err != nil {
		t.Fatalf("restore without a restorer: %v", err)
	}
}

// names lists members for a failure message.
func names(ms []archiveMember) string {
	var out []string
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s(%o,%d)", m.name, m.mode, len(m.body)))
	}
	return strings.Join(out, " ")
}

// covers T11 P29 — restore refuses an archive whose deployment state
// belongs to another tile before it writes anything: a manifest naming
// another tile, a record naming another tile, and two records each leave
// the tile's files, the other tile's files, the staging area and the plane
// untouched. An archive without a deployment section is placed as today,
// its deployments/ entries skipped as an older xbind skips them.
func TestRestoreRefusesForeignManifest(t *testing.T) {
	b := zeroDataBroker(t)
	root := b.Reg.Root
	section := &backup.Deployments{Record: true}
	archive := func(component string, dep *backup.Deployments, extra ...archiveMember) []byte {
		ms := []archiveMember{manifestJSON(t, backup.Manifest{Component: component, Scope: component, Includes: []string{"source"}, Deployments: dep})}
		ms = append(ms, extra...)
		ms = append(ms, archiveMember{"source/restored.txt", 0o644, []byte("restored\n")})
		return writeArchive(t, ms)
	}
	record := func(tile string) archiveMember {
		return archiveMember{backup.RecordName, 0o600, testRecord(tile, "", "")}
	}
	object := archiveMember{backup.CheckpointsPrefix + "objects/ab/" + strings.Repeat("c", 38), 0o444, []byte("x")}
	untouched := func(what string, calls []putCall) {
		t.Helper()
		for _, rel := range []string{"apps/cal/restored.txt", "apps/other/restored.txt", ".xbin/restore", "data/deployments", "data/checkpoints"} {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
				t.Errorf("%s: %s was written", what, rel)
			}
		}
		if len(calls) != 0 {
			t.Errorf("%s: the plane was called", what)
		}
	}

	for _, tc := range []struct {
		name, want string
		body       []byte
	}{
		{"a manifest naming another tile", "holds the deployment state of apps/other, not apps/cal",
			archive("apps/other", section, record("apps/other"), object)},
		{"a record naming another tile", "record belongs to apps/other, not apps/cal",
			archive("apps/cal", section, record("apps/other"), object)},
		{"two records", "two deployment records",
			archive("apps/cal", section, record("apps/cal"), record("apps/cal"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []putCall
			_, err := b.restore("apps/cal", bytes.NewReader(tc.body), recordingRestorer(t, &calls))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "nothing was restored") {
				t.Fatalf("restore: %v, want a refusal naming %q", err, tc.want)
			}
			untouched(tc.name, calls)
		})
	}

	t.Run("no deployment section", func(t *testing.T) {
		var calls []putCall
		body := archive("apps/cal", nil, record("apps/other"), object)
		if _, err := b.restore("apps/cal", bytes.NewReader(body), recordingRestorer(t, &calls)); err != nil {
			t.Fatalf("restore: %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(root, "apps", "cal", "restored.txt")); err != nil || string(got) != "restored\n" {
			t.Errorf("the tile's files weren't restored: %q %v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(root, ".xbin", "restore")); err == nil || len(calls) != 0 {
			t.Errorf("deployment entries of a section-less archive were used (plane calls %d)", len(calls))
		}
	})
}

// hostileStoreArchive is apps/cal's archive with a real checkpoint store
// (of a tree holding an odd .gitmodules, which fsck's submodule checks would
// refuse) whose archived copy an attacker extended: a config running a command
// (fsmonitor, a hooks path), hooks, info/attributes, alternates onto a host
// object directory, HEAD and an index; refs to a blob, to a missing object,
// to an object only the alternates hold, a retention root whose commit
// carries another tree, and refs outside the store's layout. corrupt
// swaps the content of one of the checkpoint's files for another object,
// well-formed, under the file's object id. It returns the archive, the
// record, the marker any of those commands would write, and the refs a
// restore must keep.
func hostileStoreArchive(t *testing.T, b *Broker, corrupt bool) (body, rec []byte, marker string, keep map[string]string) {
	t.Helper()
	root := b.Reg.Root
	arch := &zeroDataArchiver{}
	b.ProxyHandler = arch
	// an odd .gitmodules is tile content like any other: its checkpoint restores
	if err := os.WriteFile(filepath.Join(root, "apps", "cal", ".gitmodules"), []byte("[submodule \"a\"]\n\tpath = a\n\turl = -evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := captureStore(t, root, "apps/cal")
	rec = testRecord("apps/cal", b.ownerRef("apps/cal"), tree)
	writeTestRecord(t, root, "apps/cal", rec)
	if _, err := b.doBackup("apps/cal"); err != nil {
		t.Fatal(err)
	}
	arch.mu.Lock()
	ms := readArchive(t, arch.body)
	arch.mu.Unlock()
	refs := map[string]string{}
	var blob string
	for _, m := range ms {
		rel := strings.TrimPrefix(m.name, backup.CheckpointsPrefix)
		if strings.HasPrefix(rel, "refs/") {
			refs[rel] = strings.TrimSpace(string(m.body))
		}
	}
	cp, view := refs["refs/xbin/checkpoints/"+tree], refs["refs/xbin/views/"+tree]
	blob = strings.TrimSpace(gitOut(t, "", "--git-dir="+checkpointStoreDir(root, "apps/cal"), "rev-parse", tree+":xbin.json"))
	if !objectID(cp) || !objectID(view) || !objectID(blob) {
		t.Fatalf("fixture: refs %v, blob %q", refs, blob)
	}

	host := t.TempDir()
	marker = filepath.Join(host, "PWNED")
	script := filepath.Join(host, "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	alien := filepath.Join(host, "alien.git")
	gitOut(t, "", "init", "-q", "--bare", alien)
	emptyTree := strings.TrimSpace(gitOut(t, "", "--git-dir="+alien, "mktree"))
	alienCommit := strings.TrimSpace(gitOut(t, "", "--git-dir="+alien, "commit-tree", "-m", "alien", emptyTree))
	missing := strings.Repeat("d", 40)
	other := strings.Repeat("e", 40)
	hostile := []archiveMember{
		{"config", 0o644, []byte("[core]\n\tbare = true\n\tfsmonitor = " + script + "\n\thooksPath = hooks\n[uploadpack]\n\tpackObjectsHook = " + script + "\n")},
		{"hooks/post-checkout", 0o755, []byte("#!/bin/sh\ntouch " + marker + "\n")},
		{"hooks/reference-transaction", 0o755, []byte("#!/bin/sh\ntouch " + marker + "\n")},
		{"info/attributes", 0o644, []byte("* filter=evil\n")},
		{"objects/info/alternates", 0o644, []byte(filepath.Join(alien, "objects") + "\n")},
		{"HEAD", 0o644, []byte("ref: refs/heads/evil\n")},
		{"index", 0o644, []byte("DIRC garbage")},
		{"description", 0o644, []byte("evil\n")},
		{"packed-refs", 0o644, []byte("# pack-refs with: peeled fully-peeled sorted\n" + cp + " refs/xbin/log/prod\n" + missing + " refs/xbin/log/qa\n^" + cp + "\n")},
		{"refs/xbin/log/main", 0o644, []byte(blob + "\n")},
		{"refs/xbin/log/dev", 0o644, []byte(alienCommit + "\n")},
		{"refs/xbin/views/" + missing, 0o644, []byte(missing + "\n")},
		{"refs/xbin/checkpoints/" + other, 0o644, []byte(cp + "\n")},
		{"refs/heads/main", 0o644, []byte(cp + "\n")},
		{"refs/xbin/log/Bad", 0o644, []byte(cp + "\n")},
		{"refs/xbin/log/x/y", 0o644, []byte(cp + "\n")},
	}
	loose := func(id string) string { return backup.CheckpointsPrefix + "objects/" + id[:2] + "/" + id[2:] }
	var forged []byte
	for _, m := range ms {
		if m.name == loose(cp) {
			forged = m.body // a well-formed object, but not the blob its name promises
		}
	}
	var out []archiveMember
	for i, m := range ms {
		out = append(out, m)
		if corrupt && m.name == loose(blob) {
			if forged == nil {
				t.Fatal("fixture: the checkpoint commit isn't a loose object")
			}
			out[len(out)-1].body = forged
		}
		if i == 1 { // after the record: before the store's own members
			for _, h := range hostile {
				out = append(out, archiveMember{backup.CheckpointsPrefix + h.name, h.mode, h.body})
			}
		}
	}
	out = append(out, archiveMember{"source/zz-after.txt", 0o644, []byte("after\n")})
	keep = map[string]string{
		"refs/xbin/restored/checkpoints/" + tree: cp,
		"refs/xbin/restored/views/" + tree:       view,
		"refs/xbin/restored/log/prod":            cp,
	}
	return writeArchive(t, out), rec, marker, keep
}

// checkHostileRestore restores hostileStoreArchive, after isolate (which
// may turn confinement on; the fixtures are built before it), and checks
// what the plane got: the record, only the refs that name commits among the
// archived objects (a retention root's with its own tree) under their
// restore-only names, and a stage of objects and those refs alone — no
// config, hooks, info, alternates, index or archived HEAD — with no command
// of the archive's ever run. A forged object refuses the whole restore
// before anything is written.
func checkHostileRestore(t *testing.T, b *Broker, isolate func()) {
	t.Helper()
	body, rec, marker, keep := hostileStoreArchive(t, b, false)
	forged, _, _, _ := hostileStoreArchive(t, b, true)
	isolate()
	var calls []putCall
	put := recordingRestorer(t, &calls)
	if _, err := b.restore("apps/cal", bytes.NewReader(body), put); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("the plane was called %d times", len(calls))
	}
	c := calls[0]
	if !bytes.Equal(c.record, rec) || !reflect.DeepEqual(c.refs, keep) {
		t.Errorf("the plane got refs %v, want %v (record %t)", c.refs, keep, bytes.Equal(c.record, rec))
	}
	if c.head != "ref: refs/heads/restore\n" {
		t.Errorf("the stage's HEAD is %q, not xbind's", c.head)
	}
	for _, f := range c.files {
		if !(f == "HEAD" || looseObject.MatchString(f) || packFile.MatchString(f) || keep[f] != "") {
			t.Errorf("the stage holds %s", f)
		}
	}
	if _, err := os.Lstat(marker); err == nil {
		t.Error("a command from the archived store ran")
	}

	after := filepath.Join(b.Reg.Root, "apps", "cal", "zz-after.txt")
	if got, err := os.ReadFile(after); err != nil || string(got) != "after\n" {
		t.Fatalf("the tile's files weren't restored: %q %v", got, err)
	}
	if err := os.Remove(after); err != nil {
		t.Fatal(err)
	}
	calls = nil
	_, err := b.restore("apps/cal", bytes.NewReader(forged), put)
	if err == nil || !strings.Contains(err.Error(), "checkpoint store can't be restored") {
		t.Fatalf("restore of a forged store: %v", err)
	}
	if _, err := os.Lstat(after); err == nil || len(calls) != 0 {
		t.Errorf("a forged store's restore wrote the tile's files (plane calls %d)", len(calls))
	}
	if _, err := os.Lstat(filepath.Join(b.Reg.Root, ".xbin", "restore")); err == nil {
		t.Error("the staged repository outlived the restore")
	}
	if _, err := os.Lstat(marker); err == nil {
		t.Error("a command from the archived store ran")
	}
}

// covers T11 (ledger L13, direct mode) — the archived store comes back as
// data: its objects staged in xbind's own directory, its refs read and
// checked against those objects by git that never sees the archive's
// config, hooks, info or alternates; a forged object (a file's content
// swapped under its id) refuses the restore before it writes anything.
// TestRestoreRebuildsStoreConfig runs the same under isolation.
func TestRestoreStagesStoreDataOnly(t *testing.T) {
	needStoreGit(t)
	checkHostileRestore(t, zeroDataBroker(t), func() {})
}
