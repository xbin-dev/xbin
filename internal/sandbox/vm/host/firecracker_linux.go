//go:build linux

package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// bootArgs: serial console for diagnostics (kept in a ring, shown on
// failure), reboot/panic exit Firecracker, no legacy keyboard probing.
const bootArgs = "console=ttyS0 reboot=k panic=1 pci=off quiet loglevel=3 " +
	"i8042.noaux i8042.nomux i8042.dumbkbd random.trust_cpu=on " +
	"memhp_default_state=online_movable"

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
	c.Boot.Args = bootArgs
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
	s.fc = exec.Command(s.hs.Firecracker, // exec-ok: runs inside the namespace sandbox
		"--api-sock", filepath.Join(s.hs.RunDir, "fc.sock"),
		"--config-file", cfgPath,
		"--level", level,
	)
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	s.fc.Stdin = devnull
	s.fc.Stdout, s.fc.Stderr = s.serial, s.serial
	if s.hs.Debug {
		s.serial.echo = os.Stderr
	}
	if err := s.fc.Start(); err != nil {
		return err
	}
	s.fcDone = make(chan struct{})
	go func() { _ = s.fc.Wait(); close(s.fcDone) }()
	return nil
}

func (s *shim) killFirecracker() {
	if s.fc != nil && s.fc.Process != nil {
		_ = s.fc.Process.Kill()
		<-s.fcDone
	}
}

func (s *shim) fcExited() bool {
	if s.fcDone == nil {
		return false
	}
	select {
	case <-s.fcDone:
		return true
	default:
		return false
	}
}

// ring keeps the last n bytes written.
type ring struct {
	mu   sync.Mutex
	buf  []byte
	n    int
	echo interface{ Write([]byte) (int, error) }
}

func newRing(n int) *ring { return &ring{n: n} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.n {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.n:]...)
	}
	if r.echo != nil {
		_, _ = r.echo.Write([]byte(strings.ReplaceAll(string(p), "\n", "\r\n")))
	}
	return len(p), nil
}

func (r *ring) tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buf
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}
