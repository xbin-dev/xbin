package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// VMOpt is a manifest's "vm" key: a bool, or an object sizing the VM.
type VMOpt struct {
	On     bool
	MemMiB int // 0 = the workspace policy's size
	VCPUs  int // 0 = the policy's
}

// Enabled reports whether the manifest asks for a VM.
func (v *VMOpt) Enabled() bool { return v != nil && v.On }

// UnmarshalJSON accepts true/false or {"memory": "512M"|"2G"|<MiB>, "vcpus": n}.
func (v *VMOpt) UnmarshalJSON(b []byte) error {
	var on bool
	if err := json.Unmarshal(b, &on); err == nil {
		*v = VMOpt{On: on}
		return nil
	}
	var o struct {
		Memory json.RawMessage `json:"memory"`
		VCPUs  int             `json:"vcpus"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return errors.New(`"vm" must be true, false or {"memory": "1G", "vcpus": 2}`)
	}
	mem := 0
	if len(o.Memory) > 0 {
		var err error
		if mem, err = parseMiB(o.Memory); err != nil {
			return fmt.Errorf(`"vm".memory: %w`, err)
		}
	}
	if o.VCPUs < 0 || o.VCPUs > 32 {
		return errors.New(`"vm".vcpus must be between 1 and 32`)
	}
	*v = VMOpt{On: true, MemMiB: mem, VCPUs: o.VCPUs}
	return nil
}

// MarshalJSON writes the short form when nothing is sized.
func (v VMOpt) MarshalJSON() ([]byte, error) {
	if v.MemMiB == 0 && v.VCPUs == 0 {
		return json.Marshal(v.On)
	}
	o := map[string]any{}
	if v.MemMiB > 0 {
		o["memory"] = strconv.Itoa(v.MemMiB) + "M"
	}
	if v.VCPUs > 0 {
		o["vcpus"] = v.VCPUs
	}
	return json.Marshal(o)
}

// parseMiB reads a size: a number of MiB, or a string with an M/G suffix.
func parseMiB(raw json.RawMessage) (int, error) {
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		if n < 128 {
			return 0, errors.New("at least 128 (MiB)")
		}
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, errors.New(`a size like "512M" or "2G"`)
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	mul := 1
	switch {
	case strings.HasSuffix(s, "G"), strings.HasSuffix(s, "GB"), strings.HasSuffix(s, "GIB"):
		mul = 1024
	}
	num := strings.TrimRight(s, "GMIB")
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return 0, errors.New(`a size like "512M" or "2G"`)
	}
	if n*mul < 128 {
		return 0, errors.New("at least 128M")
	}
	return n * mul, nil
}
