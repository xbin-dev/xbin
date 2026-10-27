// Package limits is the arithmetic of a tile's resource limits (D203,
// docs/isolation.md §Resource limits): the kinds a tile may ask more of,
// the sizes a manifest writes, how the default, an approved manifest ask and
// a raise combine into the limit that applies, the +25/50/100 % choices an
// approver is offered, and whether a choice fits the workspace. Pure — the
// broker's store (data/tile-limits.json) and the runtime feed it numbers.
package limits

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Kind names one limit, after its unit. Every value of a kind is an integer
// in that unit, on the wire and in the store.
type Kind string

const (
	MemMiB     Kind = "memMiB"     // the backend's memory.max (each leaf: every deployment, every person's partition)
	DiskGiB    Kind = "diskGiB"    // the scope's data quota; keyed by the scope's data key, asked by the tile that roots it
	VMMemMiB   Kind = "vmMemMiB"   // a VM backend's guest memory
	VMVCPUs    Kind = "vmVCPUs"    // a VM backend's vCPUs
	SbxMemMiB  Kind = "sbxMemMiB"  // a sandbox manager's tile sandboxes, all together (perTile)
	SbxVCPUs   Kind = "sbxVCPUs"   //   〃 vCPUs
	SbxDiskGiB Kind = "sbxDiskGiB" //   〃 disk
)

// Kinds is every kind, in display order.
var Kinds = []Kind{MemMiB, DiskGiB, VMMemMiB, VMVCPUs, SbxMemMiB, SbxVCPUs, SbxDiskGiB}

// Meta is how a kind is shown, asked for and stepped.
type Meta struct {
	Label    string // "memory", "data disk", …
	Unit     string // "MiB" | "GiB" | "vCPUs"
	Step     int64  // a raise's granularity, in the unit
	Alias    string // the bx name: memory, disk, vm-memory, …
	Manifest string // where a manifest asks: "limits.memory", "vm.memory", …
	Min      int64  // the smallest value a manifest may ask
}

var metas = map[Kind]Meta{
	MemMiB:     {"memory", "MiB", 64, "memory", "limits.memory", 128},
	DiskGiB:    {"data disk", "GiB", 1, "disk", "limits.disk", 1},
	VMMemMiB:   {"VM memory", "MiB", 64, "vm-memory", "vm.memory", 128},
	VMVCPUs:    {"VM vCPUs", "vCPUs", 1, "vm-vcpus", "vm.vcpus", 1},
	SbxMemMiB:  {"sandbox memory", "MiB", 64, "sandbox-memory", "limits.sandboxes.memory", 128},
	SbxVCPUs:   {"sandbox vCPUs", "vCPUs", 1, "sandbox-vcpus", "limits.sandboxes.vcpus", 1},
	SbxDiskGiB: {"sandbox disk", "GiB", 1, "sandbox-disk", "limits.sandboxes.disk", 1},
}

// Meta returns k's metadata (the zero Meta for an unknown kind).
func (k Kind) Meta() Meta { return metas[k] }

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { _, ok := metas[k]; return ok }

// CPU reports whether k counts vCPUs (fit is against the host's CPUs, not
// free room).
func (k Kind) CPU() bool { return metas[k].Unit == "vCPUs" }

// Parse resolves a kind by its name or its bx alias ("memory", "disk",
// "vm-memory", "sandbox-vcpus", …).
func Parse(s string) (Kind, bool) {
	if k := Kind(s); k.Valid() {
		return k, true
	}
	for k, m := range metas {
		if strings.EqualFold(m.Alias, s) {
			return k, true
		}
	}
	return "", false
}

// ParseSize reads a size a manifest or bx writes: a number with a binary
// unit, case-insensitive — K, M, G, T, alone or with "i", "B" or "iB" ("4G",
// "512MiB", "1.5gb"); every unit is binary. A bare number is an error: the
// caller says which unit it means (vm.memory's legacy MiB).
func ParseSize(s string) (bytes int64, err error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	t = strings.TrimSuffix(strings.TrimSuffix(t, "B"), "I")
	shift := 0
	switch {
	case strings.HasSuffix(t, "K"):
		shift = 10
	case strings.HasSuffix(t, "M"):
		shift = 20
	case strings.HasSuffix(t, "G"):
		shift = 30
	case strings.HasSuffix(t, "T"):
		shift = 40
	default:
		return 0, fmt.Errorf("%q: a size like \"512M\" or \"4G\"", s)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(t[:len(t)-1]), 64)
	if err != nil || !(f > 0) || math.IsInf(f, 0) || f*float64(int64(1)<<shift) > 1<<62 {
		return 0, fmt.Errorf("%q: a size like \"512M\" or \"4G\"", s)
	}
	return int64(math.Ceil(f * float64(int64(1)<<shift))), nil
}

// FromBytes converts bytes to k's unit, rounding up (a 1.5G disk ask is
// 2 GiB). vCPU kinds have no byte form: it returns n unchanged.
func FromBytes(k Kind, n int64) int64 {
	switch metas[k].Unit {
	case "MiB":
		return (n + 1<<20 - 1) >> 20
	case "GiB":
		return (n + 1<<30 - 1) >> 30
	}
	return n
}

// ToBytes is FromBytes' inverse (vCPU kinds: n).
func ToBytes(k Kind, v int64) int64 {
	switch metas[k].Unit {
	case "MiB":
		return v << 20
	case "GiB":
		return v << 30
	}
	return v
}

// ParseValue reads a value of kind k as bx and the API's callers write it:
// a size for a byte kind ("6G", "1536M"), a count for vCPUs.
func ParseValue(k Kind, s string) (int64, error) {
	if !k.Valid() {
		return 0, fmt.Errorf("unknown limit kind %q", k)
	}
	if k.CPU() {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil || n < 1 || n > 256 {
			return 0, fmt.Errorf("%q: a number of vCPUs, 1–256", s)
		}
		return n, nil
	}
	b, err := ParseSize(s)
	if err != nil {
		return 0, err
	}
	return FromBytes(k, b), nil
}

// Format renders v of kind k for people: "2 GiB", "1.5 GiB", "512 MiB",
// "75 GiB", "4 vCPUs".
func Format(k Kind, v int64) string {
	switch metas[k].Unit {
	case "MiB":
		if v >= 1024 {
			return trimFloat(float64(v)/1024) + " GiB"
		}
		return strconv.FormatInt(v, 10) + " MiB"
	case "GiB":
		if v >= 1024 {
			return trimFloat(float64(v)/1024) + " TiB"
		}
		return strconv.FormatInt(v, 10) + " GiB"
	case "vCPUs":
		if v == 1 {
			return "1 vCPU"
		}
		return strconv.FormatInt(v, 10) + " vCPUs"
	}
	return strconv.FormatInt(v, 10)
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// Sources of a decision.
const (
	SourceAsk   = "manifest-ask" // an approver approved the manifest's ask
	SourceRaise = "raise"        // an approver raised the cap (an at-cap prompt, ⚙, bx limits raise)
	SourceSet   = "admin-set"    // an approver set a value outright (the admin console, bx limits set)
)

// ErrSource is a decision source the API doesn't know.
var ErrSource = errors.New(`source must be "manifest-ask", "raise" or "admin-set"`)

// ValidSource reports whether s is a decision source.
func ValidSource(s string) bool { return s == SourceAsk || s == SourceRaise || s == SourceSet }

// Decision is one approver's decision on one kind.
type Decision struct {
	Value  int64     `json:"value"`
	By     string    `json:"by"` // the approver: "user:alice", "root"
	At     time.Time `json:"at"`
	Source string    `json:"source"`
}

// Entry is a kind's two independent slots. Ask approves the manifest's
// ask: it applies only while the manifest still asks above the default, up
// to what it asks — dropped from the manifest, it goes dormant (kept, like a
// D118 chrome approval) and asking again up to the approved value needs no
// new prompt. Set is a raise or an outright value, which holds whatever the
// manifest says until reverted.
type Entry struct {
	Ask *Decision `json:"ask,omitempty"`
	Set *Decision `json:"set,omitempty"`
}

// Empty reports whether neither slot holds a decision.
func (e Entry) Empty() bool { return e.Ask == nil && e.Set == nil }

func (d *Decision) value() int64 {
	if d == nil {
		return 0
	}
	return d.Value
}

// Raised is what e grants (0 for nothing), given the manifest's standing
// ask (0 when it asks nothing) and the default: the set value, or the
// approved ask while the manifest still asks above the default.
func (e Entry) Raised(manifestAsk, def int64) int64 {
	ask := int64(0)
	if manifestAsk > def {
		ask = min(e.Ask.value(), manifestAsk)
	}
	return max(e.Set.value(), ask)
}

// Effective is the limit that applies: the default, raised by e, never past
// max (the workspace's maximum for the kind; 0 = no maximum).
func Effective(def, maxV int64, e Entry, manifestAsk int64) int64 {
	r := e.Raised(manifestAsk, def)
	if maxV > 0 {
		r = min(r, maxV)
	}
	return max(def, r)
}

// Pending reports whether the manifest asks more than anything already
// covers — the default, the set value, the approved ask — counting the ask
// only up to the maximum (an ask past it is offered at the maximum).
func Pending(def, maxV int64, e Entry, manifestAsk int64) bool {
	want := manifestAsk
	if maxV > 0 {
		want = min(want, maxV)
	}
	return want > max(def, e.Set.value(), e.Ask.value())
}

// Hit is a tile meeting a cap: an OOM kill, a full disk quota, a sandbox
// refused or stopped at its tile's total. Sticky until a decision on the
// kind or an approver dismisses it.
type Hit struct {
	At         time.Time `json:"at"`    // the latest
	Since      time.Time `json:"since"` // the first since the hit was last cleared
	Count      int       `json:"count"`
	Value      int64     `json:"value"`            // the limit it met, in the kind's unit
	Detail     string    `json:"detail,omitempty"` // "OOM-killed (2 processes)", "a 3 GiB sandbox refused", …
	Deployment string    `json:"deployment,omitempty"`
	Partition  bool      `json:"partition,omitempty"` // a person's partition (named to admins only, never the person)
}

// Book is one tile's (or, for diskGiB, one scope's) decisions and hits.
type Book struct {
	Limits map[Kind]Entry `json:"limits,omitempty"`
	Hits   map[Kind]Hit   `json:"hits,omitempty"`
}

// File is data/tile-limits.json: xbind-owned, never a tile's (a manifest
// only asks). Disk is keyed by the scope's data key (util.ScopeKey —
// "workspace" for the workspace scope), what the disk monitor measures;
// every other kind by tile path.
type File struct {
	Schema int              `json:"schema"`
	Tiles  map[string]*Book `json:"tiles,omitempty"`
	Scopes map[string]*Book `json:"scopes,omitempty"`
}

// FileSchema is File.Schema's current value.
const FileSchema = 1
