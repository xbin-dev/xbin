//go:build linux

package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// bootArgs: serial console for diagnostics (kept in a ring, shown on
// failure), reboot/panic exit Firecracker, no legacy keyboard probing.
const bootArgs = "console=ttyS0 reboot=k panic=1 pci=off quiet loglevel=3 " +
	"i8042.noaux i8042.nomux i8042.dumbkbd random.trust_cpu=on " +
	"memhp_default_state=online_movable"

// kernelArgs is bootArgs, verbose in debug mode.
func (s *shim) kernelArgs(args string) string {
	if s.hs.Debug {
		return strings.Replace(args, "quiet loglevel=3", "loglevel=7", 1)
	}
	return args
}

type fcDrive struct {
	ID       string `json:"drive_id"`
	Path     string `json:"path_on_host"`
	Root     bool   `json:"is_root_device"`
	ReadOnly bool   `json:"is_read_only"`
	Cache    string `json:"cache_type,omitempty"`
}

type fcConfig struct {
	Boot struct {
		Kernel string `json:"kernel_image_path"`
		Initrd string `json:"initrd_path"`
		Args   string `json:"boot_args"`
	} `json:"boot-source"`
	Drives  []fcDrive `json:"drives"`
	Machine struct {
		VCPUs  int `json:"vcpu_count"`
		MemMiB int `json:"mem_size_mib"`
	} `json:"machine-config"`
	Net []struct {
		ID       string `json:"iface_id"`
		HostDev  string `json:"host_dev_name"`
		GuestMAC string `json:"guest_mac"`
	} `json:"network-interfaces,omitempty"`
	Vsock struct {
		CID  int    `json:"guest_cid"`
		Path string `json:"uds_path"`
	} `json:"vsock"`
	Balloon *struct {
		MiB        int  `json:"amount_mib"`
		Deflate    bool `json:"deflate_on_oom"`
		Reporting  bool `json:"free_page_reporting"`
		StatsEvery int  `json:"stats_polling_interval_s"`
	} `json:"balloon,omitempty"`
}

// startFirecracker cold-boots the VM from a config file. Its stdin is
// /dev/null — never the PTY, which Firecracker would claim as the serial
// console — and its output (console + VMM log) goes to the ring.
func (s *shim) startFirecracker() error {
	var c fcConfig
	c.Boot.Kernel = s.hs.Kernel
	c.Boot.Initrd = s.hs.Initrd
	c.Boot.Args = s.kernelArgs(bootArgs)
	c.Drives = []fcDrive{{ID: "rootfs", Path: s.hs.Image, ReadOnly: true}}
	if s.hs.Disk != "" {
		c.Drives = append(c.Drives, fcDrive{ID: "disk", Path: s.hs.Disk, Cache: "Writeback"})
	}
	c.Machine.VCPUs = max(1, s.hs.VCPUs)
	c.Machine.MemMiB = max(128, s.hs.MemMiB)
	if s.hs.Tap != "" {
		c.Net = append(c.Net, struct {
			ID       string `json:"iface_id"`
			HostDev  string `json:"host_dev_name"`
			GuestMAC string `json:"guest_mac"`
		}{"eth0", s.hs.Tap, s.hs.GuestMAC})
	}
	c.Vsock.CID = 3
	c.Vsock.Path = filepath.Join(s.hs.RunDir, "v.sock")
	c.Balloon = &struct {
		MiB        int  `json:"amount_mib"`
		Deflate    bool `json:"deflate_on_oom"`
		Reporting  bool `json:"free_page_reporting"`
		StatsEvery int  `json:"stats_polling_interval_s"`
	}{Deflate: true, Reporting: true}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(s.hs.RunDir, "vm.json")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		return err
	}
	level := "Warn"
	if s.hs.Debug {
		level = "Info"
	}
	// inside the VM sandbox: the shim is the namespace sandbox's PID 1 and
	// Firecracker its child, jailed by that sandbox (plans/vm-sandbox.md)
	s.vmm = exec.Command(s.hs.Firecracker, // exec-ok: runs inside the namespace sandbox
		"--api-sock", filepath.Join(s.hs.RunDir, "fc.sock"),
		"--config-file", cfgPath,
		"--level", level,
	)
	return s.startVMM(s.vmm)
}
