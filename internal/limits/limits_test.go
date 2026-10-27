package limits

import (
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"4G": 4 << 30, "4g": 4 << 30, "4GiB": 4 << 30, "4GB": 4 << 30, "4 Gi": 4 << 30,
		"512M": 512 << 20, "512MiB": 512 << 20, "1.5G": 3 << 29, "64K": 64 << 10, "2T": 2 << 40,
	} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "4", "G", "-1G", "0G", "1X", "abcG", "1e30T", "NaNG", "InfG"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

func TestUnits(t *testing.T) {
	if got := FromBytes(DiskGiB, 3<<29); got != 2 {
		t.Errorf("1.5 GiB of disk = %d GiB, want 2 (rounded up)", got)
	}
	if got := FromBytes(MemMiB, 1<<20+1); got != 2 {
		t.Errorf("1 MiB + 1 byte = %d MiB, want 2", got)
	}
	if ToBytes(MemMiB, 3) != 3<<20 || ToBytes(DiskGiB, 3) != 3<<30 || ToBytes(VMVCPUs, 3) != 3 {
		t.Error("ToBytes")
	}
	for k, v := range map[Kind]int64{MemMiB: 2048, DiskGiB: 75, VMVCPUs: 4} {
		s := map[Kind]string{MemMiB: "2G", DiskGiB: "75G", VMVCPUs: "4"}[k]
		if got, err := ParseValue(k, s); err != nil || got != v {
			t.Errorf("ParseValue(%s, %q) = %d, %v", k, s, got, err)
		}
	}
	if _, err := ParseValue(SbxVCPUs, "0"); err == nil {
		t.Error("0 vCPUs accepted")
	}
	for want, in := range map[string][2]any{
		"2 GiB": {MemMiB, int64(2048)}, "1.5 GiB": {MemMiB, int64(1536)}, "512 MiB": {MemMiB, int64(512)},
		"75 GiB": {DiskGiB, int64(75)}, "1 vCPU": {VMVCPUs, int64(1)}, "8 vCPUs": {SbxVCPUs, int64(8)},
	} {
		if got := Format(in[0].(Kind), in[1].(int64)); got != want {
			t.Errorf("Format(%v) = %q, want %q", in, got, want)
		}
	}
	for s, want := range map[string]Kind{"memory": MemMiB, "disk": DiskGiB, "vm-vcpus": VMVCPUs, "sbxDiskGiB": SbxDiskGiB} {
		if k, ok := Parse(s); !ok || k != want {
			t.Errorf("Parse(%q) = %q", s, k)
		}
	}
	if _, ok := Parse("pids"); ok {
		t.Error("pids is not a kind (owner: memory, disk, sandboxes/VM only)")
	}
}

func dec(v int64, src string) *Decision { return &Decision{Value: v, By: "user:a", Source: src} }

func TestEffectiveAndPending(t *testing.T) {
	const def, maxV = 2048, 65536
	cases := []struct {
		name    string
		e       Entry
		ask     int64
		eff     int64
		pending bool
	}{
		{"nothing", Entry{}, 0, def, false},
		{"ask at the default is covered", Entry{}, def, def, false},
		{"unapproved ask", Entry{}, 4096, def, true},
		{"approved ask", Entry{Ask: dec(4096, SourceAsk)}, 4096, 4096, false},
		{"ask lowered: the approval applies only up to it", Entry{Ask: dec(4096, SourceAsk)}, 3072, 3072, false},
		{"ask dropped: the approval goes dormant", Entry{Ask: dec(4096, SourceAsk)}, 0, def, false},
		{"ask back up to the approved value: no new prompt", Entry{Ask: dec(4096, SourceAsk)}, 4096, 4096, false},
		{"ask above the approval: pending again", Entry{Ask: dec(4096, SourceAsk)}, 8192, 4096, true},
		{"a raise survives the dropped ask", Entry{Ask: dec(4096, SourceAsk), Set: dec(3072, SourceRaise)}, 0, 3072, false},
		{"a raise covers a smaller ask", Entry{Set: dec(6144, SourceRaise)}, 4096, 6144, false},
		{"the larger of the two slots", Entry{Ask: dec(8192, SourceAsk), Set: dec(3072, SourceRaise)}, 8192, 8192, false},
		{"the maximum clamps", Entry{Set: dec(1<<20, SourceSet)}, 0, maxV, false},
		{"an ask past the maximum is pending only up to it", Entry{Set: dec(maxV, SourceSet)}, 1 << 20, maxV, false},
		{"a set below the default never lowers", Entry{Set: dec(1024, SourceSet)}, 0, def, false},
	}
	for _, c := range cases {
		if got := Effective(def, maxV, c.e, c.ask); got != c.eff {
			t.Errorf("%s: Effective = %d, want %d", c.name, got, c.eff)
		}
		if got := Pending(def, maxV, c.e, c.ask); got != c.pending {
			t.Errorf("%s: Pending = %v, want %v", c.name, got, c.pending)
		}
	}
}

func TestStepAndChoices(t *testing.T) {
	if got := Step(MemMiB, 2048, 50); got != 3072 {
		t.Errorf("2 GiB +50%% = %d", got)
	}
	if got := Step(MemMiB, 2000, 25); got != 2560 { // 2500 → up to a 64 MiB step
		t.Errorf("2000 MiB +25%% = %d", got)
	}
	if got := Step(DiskGiB, 1, 25); got != 2 { // at least one step
		t.Errorf("1 GiB +25%% = %d", got)
	}
	if got := Step(VMVCPUs, 2, 25); got != 3 {
		t.Errorf("2 vCPUs +25%% = %d", got)
	}
	c := Capacity{Kind: MemMiB, Total: 16384, Free: 4096, Promised: 1024}.WithReserve() // reserve 1638: room 1434
	ch := Choices(MemMiB, 2048, c)
	if len(ch) != 3 || ch[0].Value != 2560 || ch[1].Value != 3072 || ch[2].Value != 4096 {
		t.Fatalf("choices %+v", ch)
	}
	if !ch[0].Fits || !ch[1].Fits || ch[2].Fits || ch[2].Why == "" || !ch[1].Default || ch[0].Default {
		t.Errorf("fits/default %+v", ch)
	}
	if !strings.Contains(ch[2].Why, "needs +2 GiB") || !strings.Contains(ch[2].Why, "1 GiB granted to other tiles but unused") {
		t.Errorf("why = %q", ch[2].Why)
	}
}

func TestFit(t *testing.T) {
	disk := Capacity{Kind: DiskGiB, Max: 900, Total: 1000, Free: 300}.WithReserve() // room 200
	if ok, _ := disk.Fit(50, 250); !ok {
		t.Error("+200 of 200 room")
	}
	if ok, why := disk.Fit(50, 251); ok || !strings.Contains(why, "200 GiB free after the 10% reserve") {
		t.Errorf("+201: %v %q", ok, why)
	}
	if ok, why := disk.Fit(50, 901); ok || !strings.Contains(why, "maximum") {
		t.Errorf("past max: %v %q", ok, why)
	}
	if ok, _ := disk.Fit(80, 60); !ok {
		t.Error("lowering always fits")
	}
	cpu := Capacity{Kind: SbxVCPUs, Max: 8, Total: 8}
	if ok, _ := cpu.Fit(4, 8); !ok {
		t.Error("vCPUs fit up to the maximum, never reserved")
	}
	if ok, _ := cpu.Fit(4, 9); ok {
		t.Error("vCPUs past the maximum")
	}
	if ok, _ := (Capacity{Kind: MemMiB}).Fit(2048, 1<<20); !ok {
		t.Error("unknown capacity, no maximum: the approver's call")
	}
	// The owner's rule: a tile at its default promises nothing; a raise
	// promises its extra less what it already uses.
	for _, c := range [][4]int64{{50, 10, 50, 0}, {75, 10, 50, 25}, {75, 60, 50, 15}, {75, 80, 50, 0}} {
		if got := Promise(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("Promise(eff %d, usage %d, def %d) = %d, want %d", c[0], c[1], c[2], got, c[3])
		}
	}
}
