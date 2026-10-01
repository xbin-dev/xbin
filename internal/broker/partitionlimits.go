package broker

// partitionlimits.go — the limits of people's partitions (plans/partitions
// 03 §A.5, §B.7, 06 §6; PD-18): how many of a tile's user partitions run at
// once, how many run in the whole workspace, and how much data one
// partition of a tile may hold. The runner derives the running caps from
// host memory; an admin may set others, and a tile manager may lower their
// own tile's (never above what an admin set or the default).
//
//	POST /api/xbin/partitions/limits {tile?, maxRunning?, partitionBytes?}
//
// Without a tile it sets the workspace's cap (maxRunning; admin only). With
// one it sets that tile's per-tile cap and per-partition byte ceiling: an
// admin's value, or a tile manager's lower one. 0 clears a value (back to
// the admin's, or the default). The answer is the effective limits.
//
// Kept in data/partition-limits.json (xbind's own, beside the other
// workspace files, never under a tile's partition data so a mode switch
// never deletes it): {"schema": 1, "workspace": {"maxRunning"}, "tiles":
// {"<tile>": {"maxRunning", "partitionBytes", "lowered": {…}}}}. No file is
// written until someone sets a limit. A file this xbind can't read counts
// as none (the defaults), and is never overwritten: POST answers 500 until
// it is fixed by hand.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
)

const (
	partitionLimitsRel    = "data/partition-limits.json"
	partitionLimitsSchema = 1
	maxRunningCeiling     = 4096    // the most a cap may say
	minPartitionBytes     = 1 << 20 // the least a partition ceiling may say
)

// limitSet is one set of partition limits; 0 = unset.
type limitSet struct {
	MaxRunning     int   `json:"maxRunning,omitempty"`
	PartitionBytes int64 `json:"partitionBytes,omitempty"`
}

// tileLimits is a tile's: an admin's values, and a tile manager's lower
// ones.
type tileLimits struct {
	limitSet
	Lowered *limitSet `json:"lowered,omitempty"`
}

type limitsDoc struct {
	Schema    int                   `json:"schema"`
	Workspace limitSet              `json:"workspace"`
	Tiles     map[string]tileLimits `json:"tiles,omitempty"`
}

// partitionLimits is the store; the zero value is ready.
type partitionLimits struct {
	mu       sync.Mutex
	doc      *limitsDoc                      // nil: not read yet
	bad      error                           // why the file can't be read
	defaults func() (perTile, workspace int) // the runner's defaults (boot); nil = unknown
}

// SetPartitionCapDefaults installs the running caps that apply where no
// limit is set: the runner's, derived from memory (boot).
func (b *Broker) SetPartitionCapDefaults(f func() (perTile, workspace int)) {
	b.plim.mu.Lock()
	b.plim.defaults = f
	b.plim.mu.Unlock()
}

// limits is the document: read once, and again on every call while it
// can't be read (a hand fix takes effect); callers hold b.plim.mu.
func (b *Broker) limits() *limitsDoc {
	l := &b.plim
	if l.doc != nil {
		return l.doc
	}
	doc := &limitsDoc{Schema: partitionLimitsSchema}
	bs, err := os.ReadFile(filepath.Join(b.Reg.Root, filepath.FromSlash(partitionLimitsRel)))
	var bad error
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		bad = hostless(err)
	case json.Unmarshal(bs, doc) != nil:
		bad = errors.New("it doesn't parse")
	case doc.Schema != partitionLimitsSchema:
		bad = fmt.Errorf("schema %d, this xbind reads %d", doc.Schema, partitionLimitsSchema)
	}
	if bad != nil {
		if l.bad == nil || l.bad.Error() != bad.Error() {
			slog.Error("partition limits: the file can't be read; the defaults apply", "path", partitionLimitsRel, "err", bad)
		}
		l.bad = bad
		return &limitsDoc{Schema: partitionLimitsSchema}
	}
	l.doc, l.bad = doc, nil
	return doc
}

// lowerOf is a if b is unset, b if a is, else the lower.
func lowerOf[T int | int64](a, b T) T {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	}
	return min(a, b)
}

// effective is tile's limits as set: the admin's, lowered by a manager's.
func (d *limitsDoc) effective(tile string) limitSet {
	t := d.Tiles[tile]
	out := t.limitSet
	if lw := t.Lowered; lw != nil {
		out.MaxRunning = lowerOf(out.MaxRunning, lw.MaxRunning)
		out.PartitionBytes = lowerOf(out.PartitionBytes, lw.PartitionBytes)
	}
	return out
}

// PartitionCaps answers the running caps set for tile's user partitions and
// the workspace's, 0 where none is (the runner's default applies): the
// runner's PartitionCapsFor.
func (b *Broker) PartitionCaps(tile string) (perTile, workspace int) {
	b.plim.mu.Lock()
	defer b.plim.mu.Unlock()
	d := b.limits()
	return d.effective(tile).MaxRunning, d.Workspace.MaxRunning
}

// PartitionBytes is the byte ceiling set for each user partition of tile;
// 0 = none (the tile's per-namespace ceiling applies): for the data plane's
// per-partition quota (03 §B.7).
func (b *Broker) PartitionBytes(tile string) int64 {
	b.plim.mu.Lock()
	defer b.plim.mu.Unlock()
	return b.limits().effective(tile).PartitionBytes
}

func (b *Broker) registerPartitionLimits(srv *server.Server) {
	srv.RegisterAPI("POST /partitions/limits", b.apiPartitionLimits)
}

// limitsBody is the POST body; nil = unchanged.
type limitsBody struct {
	Tile           string `json:"tile"`
	MaxRunning     *int   `json:"maxRunning"`
	PartitionBytes *int64 `json:"partitionBytes"`
}

func (b *Broker) apiPartitionLimits(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body limitsBody
	if err := server.DecodeJSON(r, &body); err != nil || body.MaxRunning == nil && body.PartitionBytes == nil {
		server.WriteError(w, http.StatusBadRequest, "need {tile?, maxRunning?, partitionBytes?} with maxRunning or partitionBytes", "/docs/protocol.md")
		return
	}
	switch {
	case body.MaxRunning != nil && (*body.MaxRunning < 0 || *body.MaxRunning > maxRunningCeiling):
		server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("maxRunning is 1 to %d, or 0 to clear it", maxRunningCeiling), "/docs/protocol.md")
		return
	case body.PartitionBytes != nil && (*body.PartitionBytes < 0 || *body.PartitionBytes > 0 && *body.PartitionBytes < minPartitionBytes):
		server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("partitionBytes is at least %d, or 0 to clear it", minPartitionBytes), "/docs/protocol.md")
		return
	case body.Tile == "" && body.PartitionBytes != nil:
		server.WriteError(w, http.StatusBadRequest, "partitionBytes is set per tile: name the tile", "/docs/protocol.md")
		return
	}
	if body.Tile != "" && p.Component == body.Tile { // a tile's own code never sets its limits
		server.WriteError(w, http.StatusForbidden, "a tile's own credentials can't set its partition limits", "/docs/partitions.md")
		return
	}
	// A manager acts with their own session, app or device (PD-49), and only
	// lowers their tile's. The admin console is an admin only when its
	// driver is one (partitionsAdmin, partitionadmin.go).
	admin := b.partitionsAdmin(p)
	manager := !admin && body.Tile != "" && humanID(p) != "" && b.mayManageTile(p, body.Tile)
	if !admin && !manager {
		server.WriteError(w, http.StatusForbidden, "admin only; a tile manager may lower their own tile's limits", "/docs/partitions.md")
		return
	}
	if _, ok := b.Reg.Component(body.Tile); body.Tile != "" && !ok {
		server.WriteError(w, http.StatusNotFound, "no such tile: "+body.Tile, "/docs/protocol.md")
		return
	}
	view, status, err := b.setPartitionLimits(body, admin)
	if err != nil {
		server.WriteError(w, status, err.Error(), "/docs/partitions.md")
		return
	}
	args := []any{"who", p.From(), "method", "POST", "path", "/partitions/limits", "status", http.StatusOK, "tile", body.Tile}
	if body.MaxRunning != nil {
		args = append(args, "maxRunning", *body.MaxRunning)
	}
	if body.PartitionBytes != nil {
		args = append(args, "partitionBytes", *body.PartitionBytes)
	}
	slog.Info("audit", append(args, "as", map[bool]string{true: "admin", false: "manager"}[admin])...)
	server.WriteJSON(w, http.StatusOK, view)
}

// setPartitionLimits applies body — as an admin's values, or a tile
// manager's lowering — and saves the file.
func (b *Broker) setPartitionLimits(body limitsBody, admin bool) (map[string]any, int, error) {
	l := &b.plim
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := b.limits()
	if l.doc == nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("%s can't be read (%v): fix or remove it by hand", partitionLimitsRel, l.bad)
	}
	next := *cur
	next.Tiles = map[string]tileLimits{}
	for k, v := range cur.Tiles {
		next.Tiles[k] = v
	}
	defTile, defWS := 0, 0
	if l.defaults != nil {
		defTile, defWS = l.defaults()
	}
	switch {
	case body.Tile == "":
		next.Workspace.MaxRunning = *body.MaxRunning
	case admin:
		t := next.Tiles[body.Tile]
		if body.MaxRunning != nil {
			t.MaxRunning = *body.MaxRunning
		}
		if body.PartitionBytes != nil {
			t.PartitionBytes = *body.PartitionBytes
		}
		next.Tiles[body.Tile] = t
	default:
		t := next.Tiles[body.Tile]
		lw := limitSet{}
		if t.Lowered != nil {
			lw = *t.Lowered
		}
		if n := body.MaxRunning; n != nil {
			if ceil := lowerOf(t.MaxRunning, defTile); *n > 0 && ceil > 0 && *n > ceil {
				return nil, http.StatusForbidden, fmt.Errorf("a tile manager may only lower maxRunning: at most %d", ceil)
			}
			lw.MaxRunning = *n
		}
		if n := body.PartitionBytes; n != nil {
			_, quota, _ := b.TileDiskStatus(body.Tile)
			if ceil := lowerOf(t.PartitionBytes, quota); *n > 0 && ceil > 0 && *n > ceil {
				return nil, http.StatusForbidden, fmt.Errorf("a tile manager may only lower partitionBytes: at most %d", ceil)
			}
			lw.PartitionBytes = *n
		}
		t.Lowered = &lw
		if lw == (limitSet{}) {
			t.Lowered = nil
		}
		next.Tiles[body.Tile] = t
	}
	for k, v := range next.Tiles {
		if v.limitSet == (limitSet{}) && v.Lowered == nil {
			delete(next.Tiles, k)
		}
	}
	bs, err := json.MarshalIndent(&next, "", "  ")
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	path := filepath.Join(b.Reg.Root, filepath.FromSlash(partitionLimitsRel))
	if err := fsutil.WriteFileAtomicIn(path, append(bs, '\n'), 0o600); err != nil {
		slog.Warn("partition limits: can't save", "path", path, "err", err)
		return nil, http.StatusInternalServerError, fmt.Errorf("can't save %s: %w", partitionLimitsRel, hostless(err))
	}
	l.doc = &next
	eff := next.effective(body.Tile)
	view := map[string]any{
		"workspace": map[string]any{"maxRunning": setOr(next.Workspace.MaxRunning, defWS)},
		"defaults":  map[string]any{"maxRunning": defTile, "workspaceMaxRunning": defWS},
	}
	if body.Tile != "" {
		view["tile"] = body.Tile
		view["limits"] = map[string]any{"maxRunning": setOr(eff.MaxRunning, defTile), "partitionBytes": eff.PartitionBytes}
	}
	return view, http.StatusOK, nil
}

// setOr is set, or def while set is unset (0).
func setOr(set, def int) int {
	if set > 0 {
		return set
	}
	return def
}
