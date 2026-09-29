package broker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// partFxFiles is a workspace with two partitioned tiles and an
// unpartitioned one (plans/partitions/03 §B.2): apps/docs keeps each
// person's data apart, with a global instance, and declares a partitioned
// kv (docs), a "read" kv (pub), a shared kv (board) and file resources of
// each kind; apps/agent is partitioned too and holds a grant on docs, as
// does the unpartitioned apps/plain.
var partFxFiles = map[string]string{
	"xbin.json": `{"schema":1,"grants":[
		{"from":"apps/agent","target":"res:apps/docs/docs","role":"writer"},
		{"from":"apps/plain","target":"res:apps/docs/docs","role":"writer"}]}`,
	"apps/docs/scope.json": `{"resources":{
		"docs":{"type":"kv"},"pub":{"type":"kv","shared":"read"},"board":{"type":"kv","shared":true},
		"files":{"type":"filesystem"},"notes":{"type":"sqlite"},
		"team":{"type":"sqlite","shared":true},"conf":{"type":"filesystem","shared":"read"},
		"box":{"type":"blob"},"beat":{"type":"cron"},"bus":{"type":"bus"}}}`,
	"apps/docs/xbin.json": `{"runtime":"go","partition":["user","global"],
		"uses":[{"target":"res:apps/docs/docs","role":"writer"},{"target":"res:apps/docs/pub","role":"writer"},
		        {"target":"res:apps/docs/board","role":"writer"},{"target":"res:apps/docs/files","role":"writer"},
		        {"target":"res:apps/docs/notes","role":"writer"},{"target":"res:apps/docs/team","role":"writer"},
		        {"target":"res:apps/docs/conf","role":"writer"},{"target":"res:apps/docs/box","role":"writer"},
		        {"target":"res:apps/docs/bus","role":"writer"}]}`,
	"apps/agent/scope.json": `{"resources":{"mem":{"type":"kv"}}}`,
	"apps/agent/xbin.json": `{"runtime":"go","partition":["user"],
		"uses":[{"target":"res:apps/agent/mem","role":"writer"},{"target":"res:apps/docs/docs","role":"writer"}]}`,
	"apps/plain/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/docs/docs","role":"writer"}]}`,
}

// partFx is partFxFiles with a users store — alice and carol read apps/docs
// and apps/agent, bob reads apps/agent only — and the identity plane's
// seams stood in: a person acts in their own partition of a partitioned
// tile, the tile's person-less principals (its global instance) and the
// root token in global, a view-as principal nowhere; a person's uid is
// "uid-<id>".
func partFx(t *testing.T) *partWS {
	t.Helper()
	w := newPartWS(t, partFxFiles, nil)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for id, tiles := range map[string]map[string]string{
		"alice": {"apps/docs": "read", "apps/agent": "read"},
		"carol": {"apps/docs": "read", "apps/agent": "read"},
		"bob":   {"apps/agent": "read"},
	} {
		if _, err := st.Upsert(users.User{ID: id, Tiles: tiles}, "pw-"+id); err != nil {
			t.Fatal(err)
		}
	}
	w.b.Users = st
	stubPartitionIdentity(t)
	for _, tile := range []string{"apps/docs", "apps/agent"} {
		if _, ok := w.b.Reg.Component(tile); !ok {
			t.Fatalf("%s isn't registered", tile)
		}
		if st, _, _ := w.state(tile); st.String() != "partitioned" {
			t.Fatalf("%s: %s, want partitioned (auto, on an empty tile)", tile, st)
		}
	}
	return w
}

// stubPartitionIdentity stands the identity plane's seams in for a test.
func stubPartitionIdentity(t *testing.T) {
	t.Helper()
	prevAddr, prevMint, prevUID := addressedPartitionSeam, partitionMintUIDSeam, partitionUIDSeam
	t.Cleanup(func() { addressedPartitionSeam, partitionMintUIDSeam, partitionUIDSeam = prevAddr, prevMint, prevUID })
	addressedPartitionSeam = func(b *Broker, p auth.Principal, tile string) (string, error) {
		c, ok := b.Reg.Component(tile)
		if !ok || !partitionedNow(c) {
			return "", nil
		}
		spec, _ := c.Partitioned()
		switch {
		case p.Impersonator != "":
			return "", errors.New(tile + " keeps " + p.UserID + "'s data private: view-as can't open it")
		case p.UserID != "":
			return "user:" + p.UserID, nil
		case (p.Component == tile || p.Owner) && spec.Global:
			return partGlobalKey, nil
		}
		return "", fmt.Errorf("%s is partitioned and has no global instance", tile)
	}
	partitionMintUIDSeam = func(b *Broker, userID string) (string, error) { return "uid-" + userID, nil }
	partitionUIDSeam = func(b *Broker, userID string) string { return "uid-" + userID }
}

// setConsentPolicy writes the workspace's partitionConsent policy.
func (w *partWS) setConsentPolicy(on bool) {
	w.t.Helper()
	body := fmt.Sprintf(`{"schema":1,"partitionConsent":%v,"credentialResetConfirm":false}`, on)
	p := filepath.Join(w.root, "data", "workspace-policies.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		w.t.Fatal(err)
	}
	later := time.Now().Add(time.Duration(len(body)) * time.Second)
	_ = os.Chtimes(p, later, later) // the cache keys on size and mtime
	if got := w.b.Policies().PartitionConsent; got != on {
		w.t.Fatalf("partitionConsent reads %v, want %v", got, on)
	}
}

// Principals of the fixture.
var (
	aliceDocs   = auth.Principal{Component: "apps/docs", UserID: "alice", Via: "frame", Role: "writer"}
	carolDocs   = auth.Principal{Component: "apps/docs", UserID: "carol", Via: "frame", Role: "writer"}
	docsGlobal  = auth.Principal{Component: "apps/docs", Via: "instance", Role: "admin"}
	aliceAgent  = auth.Principal{Component: "apps/agent", UserID: "alice", Via: "instance"}
	bobAgent    = auth.Principal{Component: "apps/agent", UserID: "bob", Via: "instance"}
	plainTile   = auth.Principal{Component: "apps/plain", Via: "instance"}
	viewAsAlice = auth.Principal{Component: "apps/docs", UserID: "alice", Via: "frame", Impersonator: "root"}
)
