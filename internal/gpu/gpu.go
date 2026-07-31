// Package gpu discovers host NVIDIA GPUs and turns owner-approved gpu:* grants
// into the sandbox binds/env that expose specific GPUs to a component or
// terminal (plans/gpu.md). Rootless: NVIDIA device nodes are world-accessible,
// so binding them + the host driver libs is enough — no root, no container
// toolkit. Multi-GPU isolation is "bind only the granted /dev/nvidiaN nodes".
package gpu

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// Device is one host GPU.
type Device struct {
	Index int    `json:"index"`
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	Node  string `json:"node"` // /dev/nvidiaN
}

var (
	invOnce sync.Once
	invList []Device
)

// Inventory lists the host's NVIDIA GPUs (cached). Empty when there is no driver
// / no GPU / nvidia-smi is absent.
func Inventory() []Device {
	invOnce.Do(func() { invList = discover() })
	return invList
}

// Available reports whether any usable GPU was found.
func Available() bool { return len(Inventory()) > 0 }

func discover() []Device {
	smi, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	out, err := exec.Command(smi, "--query-gpu=index,uuid,name", "--format=csv,noheader").Output()
	if err != nil {
		return nil
	}
	var devs []Device
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, ",", 3)
		if len(f) != 3 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		node := "/dev/nvidia" + strconv.Itoa(idx)
		if _, err := os.Stat(node); err != nil {
			continue // no device node → not usable
		}
		devs = append(devs, Device{
			Index: idx, UUID: strings.TrimSpace(f[1]), Name: strings.TrimSpace(f[2]), Node: node,
		})
	}
	return devs
}

// ValidTarget reports whether t is a syntactically valid gpu:* target.
func ValidTarget(t string) bool {
	spec, ok := strings.CutPrefix(t, "gpu:")
	if !ok {
		return false
	}
	spec = strings.TrimSpace(spec)
	if spec == "all" || spec == "" {
		return true
	}
	if _, err := strconv.Atoi(spec); err == nil {
		return true
	}
	return strings.HasPrefix(strings.ToLower(spec), "gpu-") // UUID form
}

// Resolve maps gpu:* targets to concrete inventory devices: gpu:all → every GPU;
// gpu:<index>; gpu:<uuid> (a prefix suffices). Deduped by index; unknown
// devices are dropped.
func Resolve(targets []string) []Device {
	inv := Inventory()
	if len(inv) == 0 {
		return nil
	}
	seen := map[int]bool{}
	var out []Device
	add := func(d Device) {
		if !seen[d.Index] {
			seen[d.Index] = true
			out = append(out, d)
		}
	}
	for _, t := range targets {
		spec, ok := strings.CutPrefix(t, "gpu:")
		if !ok {
			continue
		}
		spec = strings.TrimSpace(spec)
		switch {
		case spec == "all" || spec == "":
			for _, d := range inv {
				add(d)
			}
		default:
			if idx, err := strconv.Atoi(spec); err == nil {
				for _, d := range inv {
					if d.Index == idx {
						add(d)
					}
				}
				continue
			}
			ls := strings.ToLower(spec)
			for _, d := range inv {
				if strings.HasPrefix(strings.ToLower(d.UUID), ls) {
					add(d)
				}
			}
		}
	}
	return out
}

// libDirs is where host NVIDIA driver libs live across distros.
var libDirs = []string{"/usr/lib", "/usr/lib64", "/usr/lib/x86_64-linux-gnu", "/lib/x86_64-linux-gnu"}

// Binds returns the sandbox binds + extra env that expose devs. Control nodes +
// driver userspace (libcuda/libnvidia-* + nvidia-smi) are always included; the
// per-GPU /dev/nvidiaN nodes only for the granted devices — that selectivity is
// the isolation boundary. Returns nil,nil for an empty set.
func Binds(devs []Device) ([]sandbox.Bind, []string) {
	if len(devs) == 0 {
		return nil, nil
	}
	var binds []sandbox.Bind
	bindNode := func(p string) {
		if _, err := os.Stat(p); err == nil {
			binds = append(binds, sandbox.Bind{Src: p, Dst: p})
		}
	}
	for _, c := range []string{"/dev/nvidiactl", "/dev/nvidia-uvm", "/dev/nvidia-uvm-tools", "/dev/nvidia-modeset"} {
		bindNode(c)
	}
	var idxs []string
	for _, d := range devs {
		bindNode(d.Node)
		idxs = append(idxs, strconv.Itoa(d.Index))
	}
	if smi, err := exec.LookPath("nvidia-smi"); err == nil {
		binds = append(binds, sandbox.Bind{Src: smi, Dst: "/usr/bin/nvidia-smi", RO: true})
	}
	// Driver libs → /usr/lib (a default loader search dir, so no LD_LIBRARY_PATH
	// clobber). Bind the SONAME symlinks; the bind resolves them to the real
	// versioned files, matched to the running host driver.
	seen := map[string]bool{}
	for _, dir := range libDirs {
		for _, pat := range []string{"libnvidia-*.so.1", "libcuda.so.1"} {
			ms, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, m := range ms {
				base := filepath.Base(m)
				if seen[base] {
					continue
				}
				seen[base] = true
				binds = append(binds, sandbox.Bind{Src: m, Dst: "/usr/lib/" + base, RO: true})
			}
		}
	}
	env := []string{
		"NVIDIA_VISIBLE_DEVICES=" + strings.Join(idxs, ","),
		"CUDA_VISIBLE_DEVICES=" + strings.Join(idxs, ","),
		"NVIDIA_DRIVER_CAPABILITIES=compute,utility",
	}
	return binds, env
}
