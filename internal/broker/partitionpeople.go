package broker

// partitionpeople.go — people's lifecycle on partitioned tiles
// (plans/partitions/06 §9; PD-20, PD-26, PD-43) and their notices (06 §8,
// §12.1).
//
//   - A person deleted: their partition instances stop and their tokens are
//     revoked; their partitions (namespaces and records) are orphaned
//     (user-deleted) and swept after the retention, or purged, their backup
//     subkeys erased then; their personal binds and consents go with their
//     uid; and, if they held partitions, homes/<id> and
//     data/agent-history/<id> move to data/orphans/<id>-<uid8>/ (S17), so a
//     person created again under the id starts with neither.
//   - A person disabled, or who lost read on a tile: dormant — the liveness
//     gate refuses every start and delivery (PD-20), and their running
//     instances stop at once (PartitionPeopleChanged, told of every users
//     event). Regaining access resumes.
//
// Notices are what xbind tells a person about their partitions: a wipe (a
// mode switch, an admin's reset), a credential an admin made for them
// (partitioncreds.go). They live in data/partitions/people/<uid>.json with
// the person's held credentials — keyed by the incarnation, so a person
// created again never reads the old one's — and reach the person's own
// sockets as the partitions event, op "notice".

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	peopleDir        = "people" // data/partitions/people/<uid>.json
	personDocSchema  = 1
	noticesKeep      = 50
	noticesKeepFor   = 90 * 24 * time.Hour
	orphansDir       = "orphans" // data/orphans/<id>-<uid8>/
	noticeKindWipe   = "partition-deleted"
	noticeKindCredit = "credential"
)

var peopleNow = time.Now // tests stand in

// personDoc is one person's (incarnation's) notices and held credentials.
type personDoc struct {
	Schema  int              `json:"schema"`
	User    string           `json:"user"`
	UID     string           `json:"uid"`
	Notices []personNotice   `json:"notices,omitempty"`
	Held    []heldCredential `json:"held,omitempty"`
}

// personNotice is one notice on the wire and on disk.
type personNotice struct {
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Tile string    `json:"tile,omitempty"`
	Text string    `json:"text"`
	Hold string    `json:"hold,omitempty"` // the held credential it asks about
}

var peopleLocks sync.Map // workspace root → *sync.Mutex

func (b *Broker) peopleLock() *sync.Mutex {
	v, _ := peopleLocks.LoadOrStore(b.Reg.Root, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (b *Broker) personDocPath(uid string) (string, bool) {
	if !users.UIDOK(uid) {
		return "", false
	}
	return filepath.Join(b.Reg.Root, "data", partitionsDir, peopleDir, uid+".json"), true
}

// readPersonDoc reads uid's document (nil for none); callers hold peopleLock.
func (b *Broker) readPersonDoc(uid string) (*personDoc, error) {
	path, ok := b.personDocPath(uid)
	if !ok {
		return nil, nil
	}
	return readPersonDocAt(path, uid)
}

// readPersonDocAt reads the document of uid at path (nil for none). Its
// writes are atomic, so a reader without the people lock sees a whole one.
func readPersonDocAt(path, uid string) (*personDoc, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var d personDoc
	if err := json.Unmarshal(raw, &d); err != nil || d.Schema != personDocSchema || d.UID != uid {
		return nil, errors.New("a person's notices file this xbind can't read")
	}
	return &d, nil
}

// writePersonDoc stores d (removes it when it holds nothing); callers hold
// peopleLock.
func (b *Broker) writePersonDoc(d *personDoc) error {
	path, ok := b.personDocPath(d.UID)
	if !ok {
		return errors.New("no uid")
	}
	if len(d.Notices) == 0 && len(d.Held) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removeEmptyDirs(filepath.Dir(path), filepath.Dir(filepath.Dir(path)))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(raw, '\n'), 0o600)
}

// editPersonDoc applies edit to user's current incarnation's document
// (made when missing); false from edit writes nothing.
func (b *Broker) editPersonDoc(user string, edit func(d *personDoc) bool) error {
	uid := b.storedPartitionUID(user)
	if uid == "" {
		return errors.New(user + " holds no partitions")
	}
	mu := b.peopleLock()
	mu.Lock()
	defer mu.Unlock()
	d, err := b.readPersonDoc(uid)
	if err != nil {
		return err
	}
	if d == nil {
		d = &personDoc{Schema: personDocSchema, User: user, UID: uid}
	}
	if d.User != user || !edit(d) {
		return nil
	}
	return b.writePersonDoc(d)
}

func newNoticeID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// notePartitionNotice keeps a notice for user (of tile, "" for none) and
// tells their own sockets (op "notice"). A person without partitions keeps
// none: nothing of theirs is partitioned.
func (b *Broker) notePartitionNotice(user, tile, kind, text string) {
	b.addNotice(user, personNotice{Kind: kind, Tile: tile, Text: text})
}

func (b *Broker) addNotice(user string, n personNotice) {
	n.ID, n.At = newNoticeID(), peopleNow().UTC()
	err := b.editPersonDoc(user, func(d *personDoc) bool {
		d.Notices = append(d.Notices, n)
		cut := peopleNow().Add(-noticesKeepFor)
		d.Notices = slices.DeleteFunc(d.Notices, func(o personNotice) bool { return o.At.Before(cut) })
		if len(d.Notices) > noticesKeep {
			d.Notices = d.Notices[len(d.Notices)-noticesKeep:]
		}
		return true
	})
	if err != nil {
		slog.Warn("partitions: a person's notice not kept", "user", user, "kind", n.Kind, "err", err)
	}
	b.publishPersonEvent(user, n.Tile, map[string]any{"op": "notice", "notice": n})
}

// noticesOf are user's notices (of tile and none, or all for tile ""),
// newest first.
func (b *Broker) noticesOf(user, tile string) []personNotice {
	uid := b.storedPartitionUID(user)
	mu := b.peopleLock()
	mu.Lock()
	d, _ := b.readPersonDoc(uid)
	mu.Unlock()
	out := []personNotice{}
	if d == nil || d.User != user {
		return out
	}
	for i := len(d.Notices) - 1; i >= 0; i-- {
		if n := d.Notices[i]; tile == "" || n.Tile == "" || n.Tile == tile {
			out = append(out, n)
		}
	}
	return out
}

// audienceEvent is a partitions event's data for one person's own sockets —
// their session, app or device, never a tile's credential or view-as — or,
// with tile set and readers, for the tile's readers (op mode).
type audienceEvent struct {
	data    map[string]any
	user    string // the person; "" for a tile's readers
	tile    string
	readers bool
}

func (e audienceEvent) MarshalJSON() ([]byte, error) { return json.Marshal(e.data) }

// VisibleTo is the server's per-event audience check.
func (e audienceEvent) VisibleTo(p auth.Principal) bool {
	if e.readers {
		return p.IsAdmin() || p.Component == e.tile || p.Component == "" && p.CanReadTile(e.tile)
	}
	return p.Component == "" && p.Impersonator == "" && p.UserID == e.user && e.user != ""
}

// PersonOnly marks the data as the person's own (partitionEventFor).
func (e audienceEvent) PersonOnly() bool { return !e.readers }

func (b *Broker) publishPersonEvent(user, tile string, data map[string]any) {
	if b.Hub != nil {
		b.Hub.Publish(events.Event{Type: "partitions", Component: tile, Data: audienceEvent{data: data, user: user, tile: tile}})
	}
}

// ---- the users store's hooks ----

// PartitionPersonDeleted is the users-store delete hook for person userID,
// whose incarnation was uid (read before the delete): 06 §9's "deleted".
func (b *Broker) PartitionPersonDeleted(userID, uid string) {
	b.stopPartitionsOfPerson(userID)
	if uid == "" {
		return // never held a partition: nothing is keyed by them
	}
	holder := b.heldPartitions(userID, uid)
	b.PartitionUserDeleted(userID, uid)     // namespaces and records orphaned (user-deleted), swept later
	b.PersonalBindsUserDeleted(userID, uid) // their binds apply to no one now
	if err := b.dropPartitionConsents(uid); err != nil {
		slog.Warn("partitions: a deleted person's consents", "user", userID, "err", err)
	}
	mu := b.peopleLock()
	mu.Lock()
	if path, ok := b.personDocPath(uid); ok {
		_ = os.Remove(path) // their notices and held credentials go with them
	}
	mu.Unlock()
	if holder {
		b.moveToOrphans(userID, uid)
	}
	slog.Info("partitions: a person deleted", "user", userID, "held", holder)
}

// heldPartitions reports whether userID's incarnation uid has, or had, a
// partition anywhere: a record or a namespace of theirs.
func (b *Broker) heldPartitions(userID, uid string) bool {
	held := false
	b.eachRecordIdentity(func(user, recUID, _ string, _ bool) { held = held || user == userID && recUID == uid })
	if held {
		return true
	}
	pkey := util.PartitionKey(userID, uid)
	_ = b.eachPartitionNamespace("", func(id nsID) { held = held || id.pkey == pkey })
	return held
}

// moveToOrphans moves a deleted partition holder's home and agent-session
// history to data/orphans/<id>-<uid8>/ (S17): a rename of each directory
// entry (never a walk of what a sandbox wrote in it), refused for anything
// but a real directory.
func (b *Broker) moveToOrphans(userID, uid string) {
	key := term.HomeKey(auth.Principal{UserID: userID})
	dst := filepath.Join(b.Reg.Root, "data", orphansDir, key+"-"+uid[:min(8, len(uid))])
	for _, m := range []struct{ from, name string }{
		{term.HomeDir(b.Reg.Root, key), "home"},
		{filepath.Join(b.Reg.Root, "data", "agent-history", key), "agent-history"},
	} {
		fi, err := os.Lstat(m.from)
		if err != nil || !fi.IsDir() {
			continue // none, or not a directory (a symlink is never followed)
		}
		if err := os.MkdirAll(dst, 0o700); err != nil {
			slog.Warn("partitions: a deleted person's orphans", "user", userID, "err", err)
			return
		}
		if err := os.Rename(m.from, filepath.Join(dst, m.name)); err != nil {
			slog.Warn("partitions: a deleted person's "+m.name+" not moved", "user", userID, "err", err)
			continue
		}
		slog.Info("partitions: a deleted person's "+m.name+" moved to the orphans", "user", userID, "to", filepath.Join("data", orphansDir, filepath.Base(dst), m.name))
	}
}

// PartitionPeopleChanged is told a users-plane change (a users event:
// disabled, levels, orgs): every running partition instance whose person
// is no longer live on its tile — deleted, disabled, lost read — stops,
// its tokens revoked (PD-20). Nothing else changes: regaining access
// resumes.
func (b *Broker) PartitionPeopleChanged() {
	stopped := map[string]bool{}
	for _, in := range b.partitionInstances() {
		id, ok := util.Partition(in.Partition).User()
		k := in.Tile + "\x00" + in.Partition
		if !ok || stopped[k] || b.personLive(id, in.Tile) == nil {
			continue
		}
		stopped[k] = true
		b.stopPartitionInstance(in.Tile, cmp.Or(in.Dep, b.primaryOf(in.Tile), util.MainDeployment), in.Partition)
		slog.Info("partitions: a person's instance stopped: no longer live on the tile", "tile", in.Tile, "partition", in.Partition)
	}
}

func init() {
	// F13a's wipe notices land in the person's notices (partitionswitch.go)
	partitionNotice = func(b *Broker, user, tile, text string) { b.notePartitionNotice(user, tile, noticeKindWipe, text) }
}
