package deployments

// record.go — the deployment record, data/deployments/<TileKey>.json
// (11-contract §10.1): what it holds, the invariants a load checks
// (05-model §4), and its codec, which keeps every field this xbind doesn't
// know, at the top level and per deployment, through every rewrite (the root
// xbin.json's struct re-marshal drops them; this file must not).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// RecordSchema is the record format this xbind writes and understands. A
// record with a higher schema holds its tile: it was written by a newer
// xbind, and reading it as this one would misroute (11-contract §10.1).
const RecordSchema = 1

// maxRecordBytes bounds a record read: a record is a few hundred bytes, and
// a file this large is not one.
const maxRecordBytes = 1 << 20

// Record is a tile's deployment record. A tile without one is in the zero
// state (D119c); ZeroRecord synthesizes what such a tile answers, and nothing
// writes it. Records the index hands out are shared and read-only: a change
// goes through the index's commit, on a Clone.
type Record struct {
	Schema           int                          `json:"schema"`
	Tile             string                       `json:"tile"`    // the full tile path; must equal the path it is read for (D119i)
	Owner            string                       `json:"owner"`   // the tile's owner ref ("" = workspace-owned); must equal the tile's current one (D119i)
	Created          string                       `json:"created"` // RFC 3339; kept for display and an admin's adopt-or-clear, not compared
	Seq              int64                        `json:"seq"`     // +1 on every committed change; reviewed operations compare-and-set on it
	LiveReload       string                       `json:"liveReload"`
	LastLiveReload   string                       `json:"lastLiveReload"`
	LiveReloadSince  *Stamp                       `json:"liveReloadSince,omitempty"`
	Primary          string                       `json:"primary"`
	ProtectedPrimary bool                         `json:"protectedPrimary"`
	Edges            map[string]string            `json:"edges,omitempty"` // non-primary edge policy overrides; a value this xbind doesn't know reads as block (D127s)
	NextDeploy       int64                        `json:"nextDeploy"`
	Deployments      map[string]*DeploymentRecord `json:"deployments"`

	extra map[string]json.RawMessage // fields this xbind doesn't know, kept verbatim
}

// Stamp is who did something, and when.
type Stamp struct {
	At string `json:"at,omitempty"`
	By string `json:"by,omitempty"`
}

// DeploymentRecord is one deployment's entry in the record.
type DeploymentRecord struct {
	// Checkpoint is the full tree id every (re)start of the deployment runs;
	// nil exactly while live reload drives it (it follows the work tree).
	Checkpoint *string `json:"checkpoint"`
	// State is "" or "failed": the checkpoint is the attempted one of a
	// failed move off the work tree, which restarts retry (D119e).
	State string `json:"state,omitempty"`
	// Deliveries is the off switch of a non-primary deployment's cron jobs
	// and bus push subscriptions (D127h, revised 2026-09-28): absent (nil) is
	// on, the default; false is off, set by a tile manager; true, which an
	// M2 build stored when a manager turned them on, reads as on. An M2
	// build never stored false (it omitted its default off), so no record
	// needs migrating. DeliveriesOn reads it.
	Deliveries *bool            `json:"deliveries,omitempty"`
	AlwaysOn   bool             `json:"alwaysOn,omitempty"`
	Limits     map[string]int64 `json:"limits,omitempty"`
	Created    string           `json:"created,omitempty"`
	By         string           `json:"by,omitempty"`

	extra map[string]json.RawMessage
}

// DeliveriesOn reports whether the deployment's cron jobs and bus push
// subscriptions deliver while it isn't the primary: unless a tile manager
// switched them off.
func (d *DeploymentRecord) DeliveriesOn() bool { return d.Deliveries == nil || *d.Deliveries }

// FollowsWorkTree reports whether the deployment runs the work tree.
func (d *DeploymentRecord) FollowsWorkTree() bool { return d.Checkpoint == nil }

// ZeroRecord is the record a tile without one answers with: main, the
// primary, with live reload attached to it (D119c). Schema and seq are 0, as
// the zero state reports them (11-contract §1.1). It is synthesized per
// call and never written.
func ZeroRecord(tile string) *Record {
	return &Record{
		Tile:           tile,
		LiveReload:     util.MainDeployment,
		LastLiveReload: util.MainDeployment,
		Primary:        util.MainDeployment,
		Deployments:    map[string]*DeploymentRecord{util.MainDeployment: {}},
	}
}

// recordPath is where tile's record lives under the workspace root.
func recordPath(root, tile string) string {
	return filepath.Join(recordDir(root), util.TileKey(tile)+".json")
}

// recordDir holds every record, and each non-main deployment's
// registration directory beside it (11-contract §10.2).
func recordDir(root string) string { return filepath.Join(root, "data", "deployments") }

// Clone returns a deep copy of r, unknown fields included.
func (r *Record) Clone() *Record {
	c := *r
	if r.LiveReloadSince != nil {
		s := *r.LiveReloadSince
		c.LiveReloadSince = &s
	}
	c.Edges = cloneMap(r.Edges)
	c.extra = cloneMap(r.extra)
	if r.Deployments != nil {
		c.Deployments = make(map[string]*DeploymentRecord, len(r.Deployments))
		for name, d := range r.Deployments {
			if d == nil {
				c.Deployments[name] = nil
				continue
			}
			dc := *d
			if d.Checkpoint != nil {
				cp := *d.Checkpoint
				dc.Checkpoint = &cp
			}
			dc.Limits = cloneMap(d.Limits)
			dc.extra = cloneMap(d.extra)
			c.Deployments[name] = &dc
		}
	}
	return &c
}

func cloneMap[V any](m map[string]V) map[string]V {
	if m == nil {
		return nil
	}
	c := make(map[string]V, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// ParseRecord decodes a record and checks it as a load does for tile: the
// JSON, the schema, the tile path, and 05-model §4's invariants. It does not
// check the owner ref, which is the tile's current one at the moment of
// reading. A restore validates an archived record through here.
func ParseRecord(data []byte, tile string) (*Record, error) {
	r, err := decodeRecord(data)
	if err != nil {
		return nil, err
	}
	if r.Tile != tile {
		return nil, fmt.Errorf("the record names %q", r.Tile)
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// errNewerSchema marks a record written by a newer xbind.
type errNewerSchema struct{ schema int }

func (e errNewerSchema) Error() string { return "schema " + strconv.Itoa(e.schema) }

// validate checks 05-model §4's invariants, the schema, the name grammar
// and the stamps. It says nothing about which tile the record belongs to.
func (r *Record) validate() error {
	switch {
	case r.Schema > RecordSchema:
		return errNewerSchema{r.Schema}
	case r.Schema < 1:
		return fmt.Errorf("schema %d", r.Schema)
	case r.Tile == "":
		return errors.New("no tile path")
	case r.Seq < 1:
		return fmt.Errorf("seq %d", r.Seq)
	case r.NextDeploy < 0:
		return fmt.Errorf("nextDeploy %d", r.NextDeploy)
	}
	if _, err := time.Parse(time.RFC3339, r.Created); err != nil {
		return fmt.Errorf("creation stamp %q isn't an RFC 3339 time", r.Created)
	}
	if len(r.Deployments) == 0 {
		return errors.New("no deployments")
	}
	if r.Deployments[util.MainDeployment] == nil {
		return errors.New("no deployment main")
	}
	for _, name := range sortedKeys(r.Deployments) {
		d := r.Deployments[name]
		switch {
		case !util.DeploymentNameOK(name):
			return fmt.Errorf("deployment name %q", name)
		case d == nil:
			return fmt.Errorf("deployment %s is null", name)
		case name == r.LiveReload && d.Checkpoint != nil:
			return fmt.Errorf("live reload drives %s, which is pinned", name)
		case name != r.LiveReload && d.Checkpoint == nil:
			return fmt.Errorf("%s has no checkpoint and live reload doesn't drive it", name)
		case d.Checkpoint != nil && !fullTreeID(*d.Checkpoint):
			return fmt.Errorf("%s's checkpoint %q isn't a full tree id", name, *d.Checkpoint)
		case d.State != "" && d.State != "failed":
			return fmt.Errorf("%s's state %q", name, d.State)
		}
		for k, v := range d.Limits {
			if v < 0 {
				return fmt.Errorf("%s's limit %s is %d", name, k, v)
			}
		}
	}
	switch {
	case r.LiveReload != "" && r.Deployments[r.LiveReload] == nil:
		return fmt.Errorf("live reload drives %q, which doesn't exist", r.LiveReload)
	case r.LastLiveReload != "" && !util.DeploymentNameOK(r.LastLiveReload):
		return fmt.Errorf("lastLiveReload %q", r.LastLiveReload)
	case r.Deployments[r.Primary] == nil:
		return fmt.Errorf("the primary %q doesn't exist", r.Primary)
	case r.ProtectedPrimary && r.LiveReload == r.Primary:
		return fmt.Errorf("live reload drives the protected primary %s", r.Primary) // D127m
	}
	return nil
}

// fullTreeID reports whether s is a full git object id: 40 lowercase hex
// digits (SHA-1, the store's format), or 64 (SHA-256).
func fullTreeID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- the codec ----

// Keys match exactly: encoding/json's case-insensitive struct matching
// would let "Primary" set the primary and still be kept as unknown.

func (r *Record) fields() []field {
	return []field{
		{"schema", &r.Schema, false},
		{"tile", &r.Tile, false},
		{"owner", &r.Owner, false},
		{"created", &r.Created, false},
		{"seq", &r.Seq, false},
		{"liveReload", &r.LiveReload, false},
		{"lastLiveReload", &r.LastLiveReload, false},
		{"liveReloadSince", &r.LiveReloadSince, r.LiveReloadSince == nil},
		{"primary", &r.Primary, false},
		{"protectedPrimary", &r.ProtectedPrimary, false},
		{"edges", &r.Edges, len(r.Edges) == 0},
		{"nextDeploy", &r.NextDeploy, false},
		{"deployments", &r.Deployments, false},
	}
}

func (d *DeploymentRecord) fields() []field {
	return []field{
		{"checkpoint", &d.Checkpoint, false},
		{"state", &d.State, d.State == ""},
		{"deliveries", &d.Deliveries, d.Deliveries == nil},
		{"alwaysOn", &d.AlwaysOn, !d.AlwaysOn},
		{"limits", &d.Limits, len(d.Limits) == 0},
		{"created", &d.Created, d.Created == ""},
		{"by", &d.By, d.By == ""},
	}
}

// field is one known key of an object: where it decodes to, and whether an
// encode leaves it out.
type field struct {
	key  string
	ptr  any
	omit bool
}

// decodeRecord parses a record file, keeping unknown fields.
func decodeRecord(data []byte) (*Record, error) {
	r := &Record{}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, err
	}
	return r, nil
}

// UnmarshalJSON decodes the known keys exactly and keeps the rest.
func (r *Record) UnmarshalJSON(data []byte) error {
	var z Record
	extra, err := decodeObject(data, z.fields())
	if err != nil {
		return err
	}
	z.extra = extra
	*r = z
	return nil
}

// MarshalJSON encodes the known fields in order, then the unknown ones.
func (r *Record) MarshalJSON() ([]byte, error) { return encodeObject(r.fields(), r.extra) }

// UnmarshalJSON decodes the known keys exactly and keeps the rest.
func (d *DeploymentRecord) UnmarshalJSON(data []byte) error {
	var z DeploymentRecord
	extra, err := decodeObject(data, z.fields())
	if err != nil {
		return err
	}
	z.extra = extra
	*d = z
	return nil
}

// MarshalJSON encodes the known fields in order, then the unknown ones.
func (d *DeploymentRecord) MarshalJSON() ([]byte, error) { return encodeObject(d.fields(), d.extra) }

func decodeObject(data []byte, known []field) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	for _, f := range known {
		raw, ok := m[f.key]
		if !ok {
			continue
		}
		delete(m, f.key)
		if err := json.Unmarshal(raw, f.ptr); err != nil {
			return nil, fmt.Errorf("%s: %w", f.key, err)
		}
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

func encodeObject(known []field, extra map[string]json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	put := func(key string, v any) error {
		val, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(val)
		return nil
	}
	for _, f := range known {
		if f.omit {
			continue
		}
		if err := put(f.key, f.ptr); err != nil {
			return nil, err
		}
	}
	for _, k := range sortedKeys(extra) {
		if err := put(k, extra[k]); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// encode is the record's file content: indented JSON and a newline.
func (r *Record) encode() ([]byte, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
