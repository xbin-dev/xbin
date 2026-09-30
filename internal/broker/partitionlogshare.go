package broker

// partitionlogshare.go — people's partitions' backend logs (plans/partitions
// 06 §5; PD-24, C15, S12). A user partition's instance logs to its own file,
// .xbin/partition/<TileKey>/<dep>/<pkey>/backend.log (the runner writes it).
// On a partitioned tile, GET /api/xbin/logs answers:
//
//   - the caller's own partition's log — a person's session, their frames,
//     terminals and agent sessions, and the partition's own backend — at any
//     level (their own data);
//   - ?user=<id>: that person's log, for the person, or for an admin or a
//     tile manager while the person shares it (POST /partitions/share-log);
//   - ?xbin-partition=global: the global instance's log, under today's rule
//     (admins, terminal level, the tile's own credentials — never a person's
//     partition's);
//   - a credential that acts in no person's partition (the root token,
//     another tile): the global instance's log, as today;
//   - ?partition=: 400 — the credential decides, never a parameter.
//
// A tile that isn't partitioned is answered as before.
//
//	POST   /api/xbin/partitions/share-log {tile, days?}   (PersonOnly; days 1–14, default 7)
//	DELETE /api/xbin/partitions/share-log {tile}
//
// A share lives in the partition's directory (log-share.json), so a reset,
// a purge or a switch takes it with the partition.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	logShareFile    = "log-share.json"
	logShareMaxDays = 14
	logShareDefault = 7
)

var logShareNow = time.Now // tests stand in

// logShareDoc is a partition's log-share.json.
type logShareDoc struct {
	Schema int       `json:"schema"`
	Until  time.Time `json:"until"`
	At     time.Time `json:"at"`
}

// logShareView is a share on the wire.
type logShareView struct {
	Until time.Time `json:"until"`
}

// logShareOf is partition pkey of tile's live share, nil for none.
func (b *Broker) logShareOf(tile, pkey string) *logShareView {
	dir, err := b.partitionRecordDir(tile, b.primaryOf(tile), pkey)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, logShareFile))
	var doc logShareDoc
	if err != nil || json.Unmarshal(raw, &doc) != nil || !logShareNow().Before(doc.Until) {
		return nil
	}
	return &logShareView{Until: doc.Until.UTC()}
}

// apiPartitionShareLog — POST shares the caller's own partition's log of
// tile for days; DELETE ends the share. PersonOnly (the class gate).
func (b *Broker) apiPartitionShareLog(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !personOwn(w, p) {
		return
	}
	var body struct {
		Tile string `json:"tile"`
		Days int    `json:"days"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || strings.Trim(body.Tile, "/") == "" {
		server.WriteError(w, http.StatusBadRequest, "need {tile, days?}", "/docs/protocol.md")
		return
	}
	tile := strings.Trim(body.Tile, "/")
	if body.Days < 0 || body.Days > logShareMaxDays {
		server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("days is 1 to %d", logShareMaxDays), "/docs/protocol.md")
		return
	}
	if status, msg := b.partitionedTile(tile); status != 0 {
		server.WriteError(w, status, msg, modeDocs)
		return
	}
	uid := b.storedPartitionUID(p.UserID)
	dir, err := b.partitionRecordDir(tile, b.primaryOf(tile), util.PartitionKey(p.UserID, uid))
	if uid == "" || err != nil {
		server.WriteError(w, http.StatusNotFound, "you have no partition of "+tile, modeDocs)
		return
	}
	mu := b.partRecLock()
	mu.Lock()
	defer mu.Unlock()
	if _, ok, _ := readPartitionRecordAt(dir); !ok {
		server.WriteError(w, http.StatusNotFound, "you have no partition of "+tile+" (it never started)", modeDocs)
		return
	}
	path := filepath.Join(dir, logShareFile)
	if r.Method == http.MethodDelete {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			server.WriteError(w, http.StatusInternalServerError, hostless(err).Error())
			return
		}
		slog.Info("audit", "who", p.From(), "method", "DELETE", "path", "/partitions/share-log", "status", http.StatusOK, "tile", tile)
		server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tile": tile, "shared": false})
		return
	}
	now := logShareNow().UTC()
	doc := logShareDoc{Schema: 1, At: now, Until: now.Add(time.Duration(cmp.Or(body.Days, logShareDefault)) * 24 * time.Hour)}
	raw, _ := json.MarshalIndent(doc, "", "  ")
	if err := writeFileIn(dir, logShareFile, append(raw, '\n')); err != nil {
		server.WriteError(w, http.StatusInternalServerError, hostless(err).Error())
		return
	}
	slog.Info("audit", "who", p.From(), "method", "POST", "path", "/partitions/share-log", "status", http.StatusOK, "tile", tile, "until", doc.Until)
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tile": tile, "shared": true, "until": doc.Until})
}

// PartitionLog is the observability plane's question for GET /logs on tile
// (obs.Plane.PartitionLog): partitioned false — the tile keeps no people's
// partitions, and today's rules answer; otherwise rel is the partition log
// (workspace-relative) the request reads, "" for the global instance's
// (today's file and gate), part the partition it names, or status and err
// refuse.
func (b *Broker) PartitionLog(p auth.Principal, tile string, q url.Values) (rel string, part util.Partition, partitioned bool, status int, err error) {
	_, on, perr := b.tilePartitioning(tile)
	switch {
	case perr != nil:
		return "", "", true, http.StatusConflict, perr
	case !on:
		return "", "", false, 0, nil
	case q.Has("partition"):
		return "", "", true, http.StatusBadRequest, errors.New("a partitioned tile's logs take no ?partition=: your credential decides — ?user=<id> reads a log its person shares with you, ?xbin-partition=global the global instance's")
	}
	primary := cmp.Or(b.primaryOf(tile), util.MainDeployment)
	if dep := q.Get("deployment"); dep != "" && dep != primary {
		return "", "", true, 0, nil // a deployment beyond the primary runs one instance: today's
	}
	if xp := q.Get("xbin-partition"); xp != "" {
		if xp != string(util.PartitionGlobal) {
			return "", "", true, http.StatusBadRequest, errors.New("?xbin-partition= takes global only")
		}
		if p.Partition.IsUser() {
			return "", "", true, http.StatusForbidden, errors.New("a person's partition reads its own log, never the global instance's")
		}
		return "", util.PartitionGlobal, true, 0, nil
	}
	if who := q.Get("user"); who != "" {
		return b.sharedLog(p, tile, primary, who)
	}
	var own util.Partition
	switch {
	case ownCredential(p, tile):
		own = p.Partition // the partition gate's stamp: the partition it acts in on its tile
	case p.Component != "":
		return "", util.PartitionGlobal, true, 0, nil // another tile's credential: today's rule (it reads no log)
	default:
		var err error
		if own, err = b.addressedPartition(p, tile); err != nil {
			return "", "", true, http.StatusForbidden, err
		}
	}
	id, ok := own.User()
	if !ok {
		return "", util.PartitionGlobal, true, 0, nil // the root token, another tile: the global instance's, as today
	}
	return b.partitionLogRel(tile, primary, id, own)
}

// sharedLog answers ?user=who: the person's own, or — for an admin or a
// tile manager in their own session — a log its person shares now.
func (b *Broker) sharedLog(p auth.Principal, tile, dep, who string) (string, util.Partition, bool, int, error) {
	part := util.UserPartition(who)
	if !util.PartitionUserIDOK(who) {
		return "", "", true, http.StatusBadRequest, errors.New("?user= names a person")
	}
	self := p.Component == "" && p.Impersonator == "" && p.UserID == who || ownCredential(p, tile) && p.Partition == part
	if !self {
		if p.Component != "" || p.Impersonator != "" || !b.IsAdmin(p) && !b.mayManageTile(p, tile) {
			return "", "", true, http.StatusForbidden, fmt.Errorf("%s's partition log is %s's: an admin or a manager of %s reads it only while they share it", who, who, tile)
		}
	}
	uid := b.storedPartitionUID(who)
	if uid == "" {
		return "", "", true, http.StatusNotFound, fmt.Errorf("%s has no partition of %s", who, tile)
	}
	if !self && b.logShareOf(tile, util.PartitionKey(who, uid)) == nil {
		return "", "", true, http.StatusForbidden, fmt.Errorf("%s doesn't share their partition log of %s (they can: bx partition share-log %s)", who, tile, tile)
	}
	return b.partitionLogRel(tile, dep, who, part)
}

// ownCredential reports p as one of tile's own credentials — its backend,
// frames, terminals — whose stamped partition is the one it acts in on
// tile. Another tile's credential acting for a person (a nested tile's and
// an xbin.window sub-path's included, as canReadLogs has it) never reads
// that person's log of tile.
func ownCredential(p auth.Principal, tile string) bool {
	return p.Component != "" && p.Component == tile
}

// partitionLogRel is person id's partition log of tile, workspace-relative.
func (b *Broker) partitionLogRel(tile, dep, id string, part util.Partition) (string, util.Partition, bool, int, error) {
	uid := b.storedPartitionUID(id)
	if uid == "" {
		return "", "", true, http.StatusNotFound, errors.New("no logs yet — this partition hasn't started")
	}
	return ".xbin/partition/" + util.TileKey(tile) + "/" + dep + "/" + util.PartitionKey(id, uid) + "/backend.log", part, true, 0, nil
}
