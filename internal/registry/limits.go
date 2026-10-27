package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/limits"
)

// LimitsAsk is a manifest's "limits" key (D203, docs/elements.md): what the
// tile asks above the workspace's defaults — its backend's memory, its
// scope's data disk, and (a sandbox-manager tile) its tile sandboxes' total.
// Only an ask: a workspace admin, or an admin of the owning org within the
// org's ceiling, approves it (data/tile-limits.json, never the manifest,
// which the tile's own terminals and agents can write).
//
//	"limits": {"memory": "4G", "disk": "100G",
//	           "sandboxes": {"memory": "16G", "vcpus": 8, "disk": "200G"}}
//
// Its parse never fails the manifest (a failing value would otherwise take
// the whole tile down, the partition key's cautionary tale): a value it
// can't read, a bare number, a size below the minimum or an unknown key is
// a Problem — surfaced on ManifestErr (ValidateLimits) — and ignored.
type LimitsAsk struct {
	Values   map[limits.Kind]int64 // in each kind's unit; only the values that parsed
	Problems []string
}

// limitKeys are the "limits" object's keys, and "sandboxes"' keys, → kind.
var (
	limitKeys   = map[string]limits.Kind{"memory": limits.MemMiB, "disk": limits.DiskGiB}
	sandboxKeys = map[string]limits.Kind{"memory": limits.SbxMemMiB, "vcpus": limits.SbxVCPUs, "disk": limits.SbxDiskGiB}
)

// UnmarshalJSON reads the key leniently: it never returns an error.
func (l *LimitsAsk) UnmarshalJSON(b []byte) error {
	*l = LimitsAsk{}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil || top == nil {
		l.problem(`"limits" must be an object like {"memory": "4G"}; ignored`)
		return nil
	}
	for _, k := range sortedRaw(top) {
		if k == "sandboxes" {
			var sb map[string]json.RawMessage
			if err := json.Unmarshal(top[k], &sb); err != nil || sb == nil {
				l.problem(`"limits".sandboxes must be an object like {"memory": "16G", "vcpus": 8}; ignored`)
				continue
			}
			for _, sk := range sortedRaw(sb) {
				l.read("sandboxes."+sk, sandboxKeys[sk], sb[sk])
			}
			continue
		}
		l.read(k, limitKeys[k], top[k])
	}
	return nil
}

func sortedRaw(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (l *LimitsAsk) problem(s string) { l.Problems = append(l.Problems, s) }

// read records one value of kind k ("" = an unknown key) at name.
func (l *LimitsAsk) read(name string, k limits.Kind, raw json.RawMessage) {
	at := `"limits".` + name
	if k == "" {
		l.problem(at + `: not a limit xbind knows (memory, disk, sandboxes.{memory,vcpus,disk}); ignored`)
		return
	}
	var v int64
	if k.CPU() {
		var n json.Number
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		i, err := int64(0), d.Decode(&n)
		if err == nil {
			i, err = strconv.ParseInt(n.String(), 10, 64)
		}
		if err != nil || i < 1 || i > 256 {
			l.problem(at + `: a whole number of vCPUs, 1–256; ignored`)
			return
		}
		v = i
	} else {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			l.problem(at + `: use a size like "4G" (a bare number has no unit); ignored`)
			return
		}
		n, err := limits.ParseSize(s)
		if err != nil {
			l.problem(at + ": " + err.Error() + "; ignored")
			return
		}
		v = limits.FromBytes(k, n)
		if m := k.Meta(); v < m.Min {
			l.problem(fmt.Sprintf("%s: at least %s; ignored", at, limits.Format(k, m.Min)))
			return
		}
	}
	if l.Values == nil {
		l.Values = map[limits.Kind]int64{}
	}
	l.Values[k] = v
}

// MarshalJSON writes the values back in the manifest's own form (problems
// are the parse's, not part of the key).
func (l LimitsAsk) MarshalJSON() ([]byte, error) {
	top, sb := map[string]any{}, map[string]any{}
	size := func(k limits.Kind, v int64) string {
		if k.Meta().Unit == "GiB" {
			return strconv.FormatInt(v, 10) + "G"
		}
		return strconv.FormatInt(v, 10) + "M"
	}
	for k, v := range l.Values {
		switch k {
		case limits.MemMiB:
			top["memory"] = size(k, v)
		case limits.DiskGiB:
			top["disk"] = size(k, v)
		case limits.SbxMemMiB:
			sb["memory"] = size(k, v)
		case limits.SbxDiskGiB:
			sb["disk"] = size(k, v)
		case limits.SbxVCPUs:
			sb["vcpus"] = v
		}
	}
	if len(sb) > 0 {
		top["sandboxes"] = sb
	}
	return json.Marshal(top)
}

// ValidateLimits is the "limits" key's problems as one error, for
// ManifestErr — informational: the tile keeps serving, the values that
// parsed still ask.
func ValidateLimits(m Manifest) error {
	if m.Limits == nil || len(m.Limits.Problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(m.Limits.Problems, "; "))
}

// LimitAsk is what the manifest asks of kind k, in the kind's unit (0 =
// nothing): the "limits" key, and for the VM kinds the "vm" key's sizes —
// above the workspace's VM policy they are an ask an approver approves,
// below it they lower the VM as before (D203).
func (m Manifest) LimitAsk(k limits.Kind) int64 {
	switch k {
	case limits.VMMemMiB:
		if m.VM.Enabled() {
			return int64(m.VM.MemMiB)
		}
		return 0
	case limits.VMVCPUs:
		if m.VM.Enabled() {
			return int64(m.VM.VCPUs)
		}
		return 0
	}
	if m.Limits == nil {
		return 0
	}
	return m.Limits.Values[k]
}
