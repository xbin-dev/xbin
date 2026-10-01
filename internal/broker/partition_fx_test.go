package broker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// partFxFiles is a workspace with two partitioned tiles and an
// unpartitioned one (plans/partitions/03 §B.2): apps/docs keeps each
// person's data apart, with a global instance, and declares a partitioned
// kv (docs), a "read" kv (pub), a shared kv (board), file resources of each
// kind and a bus of each kind (bus, a shared wall, a "read" news);
// apps/agent is partitioned too, without a global instance, and holds a
// grant on docs, as does the unpartitioned apps/plain, which also holds one
// on the agent's mem and has a kv of its own (notes).
var partFxFiles = map[string]string{
	"xbin.json": `{"schema":1,"grants":[
		{"from":"apps/agent","target":"res:apps/docs/docs","role":"writer"},
		{"from":"apps/plain","target":"res:apps/docs/docs","role":"writer"},
		{"from":"apps/plain","target":"res:apps/agent/mem","role":"writer"}]}`,
	"apps/docs/scope.json": `{"resources":{
		"docs":{"type":"kv"},"pub":{"type":"kv","shared":"read"},"board":{"type":"kv","shared":true},
		"files":{"type":"filesystem"},"notes":{"type":"sqlite"},
		"team":{"type":"sqlite","shared":true},"conf":{"type":"filesystem","shared":"read"},
		"box":{"type":"blob"},"beat":{"type":"cron"},"bus":{"type":"bus"},
		"wall":{"type":"bus","shared":true},"news":{"type":"bus","shared":"read"}}}`,
	"apps/docs/xbin.json": `{"runtime":"go","partition":["user","global"],
		"uses":[{"target":"res:apps/docs/docs","role":"writer"},{"target":"res:apps/docs/pub","role":"writer"},
		        {"target":"res:apps/docs/board","role":"writer"},{"target":"res:apps/docs/files","role":"writer"},
		        {"target":"res:apps/docs/notes","role":"writer"},{"target":"res:apps/docs/team","role":"writer"},
		        {"target":"res:apps/docs/conf","role":"writer"},{"target":"res:apps/docs/box","role":"writer"},
		        {"target":"res:apps/docs/bus","role":"writer"},{"target":"res:apps/docs/wall","role":"writer"},
		        {"target":"res:apps/docs/news","role":"writer"}]}`,
	"apps/agent/scope.json": `{"resources":{"mem":{"type":"kv"}}}`,
	"apps/agent/xbin.json": `{"runtime":"go","partition":["user"],
		"uses":[{"target":"res:apps/agent/mem","role":"writer"},{"target":"res:apps/docs/docs","role":"writer"}]}`,
	"apps/plain/scope.json": `{"resources":{"notes":{"type":"kv"}}}`,
	"apps/plain/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/docs/docs","role":"writer"},
		{"target":"res:apps/agent/mem","role":"writer"},{"target":"res:apps/plain/notes","role":"writer"}]}`,
}

// partFx is partFxFiles with a users store — alice and carol read apps/docs
// and apps/agent, bob reads apps/agent only — and the identity plane's
// seams stood in (stubPartitionIdentity).
func partFx(t *testing.T) *partWS {
	t.Helper()
	w := partFxWith(t, nil)
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

// partFxWith is partFx's workspace with extra files written before the
// broker starts (a mode record, say), its states not checked.
func partFxWith(t *testing.T, extra map[string]string) *partWS {
	t.Helper()
	files := map[string]string{}
	for k, v := range partFxFiles {
		files[k] = v
	}
	for k, v := range extra {
		files[k] = v
	}
	w := newPartWS(t, files, nil)
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
	return w
}

// partitionConsentStub stands in for F10's consent records while a test
// runs (nil: nobody consented).
var partitionConsentStub func(user, caller, target string) bool

// stubPartitionIdentity stands the identity plane's seams in for a test, as
// F2's addressedPartition answers (02 §3, 05 §1): a person acts in their own
// partition of a partitioned tile, the tile's person-less principals (its
// global instance) and the root token in global, a view-as principal
// nowhere; another tile's principal in its own partition mapped onto the
// target — the same person's, when they can read it and, with
// partitionConsent on, consented (partitionConsentStub) — or, acting in
// none (global, or an unpartitioned tile), the target's global instance,
// refused without one. A person's uid is "uid-<id>". The bus stamp is the
// wired one (events.Event.Partition); a test may unset it, and it comes
// back at cleanup.
func stubPartitionIdentity(t *testing.T) {
	t.Helper()
	prevAddr, prevMint, prevUID := addressedPartitionSeam, partitionMintUIDSeam, partitionUIDSeam
	prevStamp, prevRead, prevConsent := stampBusPartition, busEventPartition, partitionConsentStub
	t.Cleanup(func() {
		addressedPartitionSeam, partitionMintUIDSeam, partitionUIDSeam = prevAddr, prevMint, prevUID
		stampBusPartition, busEventPartition, partitionConsentStub = prevStamp, prevRead, prevConsent
	})
	var addressed func(b *Broker, p auth.Principal, tile string) (string, error)
	addressed = func(b *Broker, p auth.Principal, tile string) (string, error) {
		c, ok := b.Reg.Component(tile)
		if !ok || !partitionedNow(c) {
			return "", nil
		}
		spec, _ := c.Partitioned()
		if _, isTile := b.Reg.Component(p.Component); isTile && p.Component != tile {
			cp, err := addressed(b, p, p.Component)
			if err != nil {
				return "", err
			}
			if user, ok := strings.CutPrefix(cp, "user:"); ok {
				switch {
				case !stubPersonReads(b, user, tile):
					return "", fmt.Errorf("%s can't read %s: a person's partition reaches only tiles they can read", user, tile)
				case b.Policies().PartitionConsent && (partitionConsentStub == nil || !partitionConsentStub(user, p.Component, tile)):
					return "", fmt.Errorf("%s hasn't let %s use their %s data", user, p.Component, tile)
				}
				return cp, nil
			}
			if spec.Global {
				return partGlobalKey, nil
			}
			return "", fmt.Errorf("%s is partitioned: only partitioned tiles reach its people's data, and it has no global instance", tile)
		}
		switch {
		case p.Impersonator != "":
			return "", fmt.Errorf("%s keeps %s's data private: view-as can't open it", tile, p.UserID)
		case p.UserID != "":
			return "user:" + p.UserID, nil
		case (p.Component == tile || p.Owner) && spec.Global:
			return partGlobalKey, nil
		}
		return "", fmt.Errorf("%s is partitioned and has no global instance", tile)
	}
	addressedPartitionSeam = addressed
	partitionMintUIDSeam = func(b *Broker, userID string) (string, error) { return "uid-" + userID, nil }
	partitionUIDSeam = func(b *Broker, userID string) string { return "uid-" + userID }
	partitionConsentStub = nil
}

// stubPersonReads is F2's personLive for the stub: userID exists, is
// enabled and can read tile.
func stubPersonReads(b *Broker, userID, tile string) bool {
	if u, ok := b.Users.Get(userID); !ok || u.Disabled {
		return false
	}
	acc, ok := b.Users.Access(userID)
	return ok && acc.CanReadTile(tile)
}

// setPartitionConsent writes the workspace's partitionConsent policy.
func (w *partWS) setPartitionConsent(on bool) {
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
