package registry

import (
	"encoding/json"
	"testing"
)

func TestVMOpt(t *testing.T) {
	for _, c := range []struct {
		in       string
		on       bool
		mem, cpu int
		bad      bool
	}{
		{`{"vm": true}`, true, 0, 0, false},
		{`{"vm": false}`, false, 0, 0, false},
		{`{"vm": {"memory": "2G", "vcpus": 4}}`, true, 2048, 4, false},
		{`{"vm": {"memory": "512M"}}`, true, 512, 0, false},
		{`{"vm": {"memory": 768}}`, true, 768, 0, false},
		{`{"vm": {"memory": "lots"}}`, false, 0, 0, true},
		{`{"vm": {"vcpus": 99}}`, false, 0, 0, true},
		{`{"vm": "yes"}`, false, 0, 0, true},
		{`{}`, false, 0, 0, false},
	} {
		var m Manifest
		err := json.Unmarshal([]byte(c.in), &m)
		if (err != nil) != c.bad {
			t.Errorf("%s: err=%v, want bad=%v", c.in, err, c.bad)
			continue
		}
		if c.bad {
			continue
		}
		if m.VM.Enabled() != c.on || (m.VM != nil && (m.VM.MemMiB != c.mem || m.VM.VCPUs != c.cpu)) {
			t.Errorf("%s: got %+v", c.in, m.VM)
		}
	}
}
