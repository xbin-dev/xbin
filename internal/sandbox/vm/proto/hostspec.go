package proto

// HostSpec is what xbind (internal/vm) hands the shim through the sandbox
// spec: where the VM's pieces sit inside the namespace sandbox, how big the
// guest is, and what to run in it. Paths are as seen inside the sandbox.
type HostSpec struct {
	Firecracker string `json:"firecracker"`
	Kernel      string `json:"kernel"`
	Initrd      string `json:"initrd"`
	Image       string `json:"image"`               // read-only rootfs image
	ImageType   string `json:"imageType,omitempty"` // "erofs" (default) | "ext4"
	Disk        string `json:"disk,omitempty"`      // persistent upper disk image ("" = tmpfs upper)
	RunDir      string `json:"runDir"`              // the shim's own sockets (never exported)

	VCPUs  int `json:"vcpus"`
	MemMiB int `json:"memMiB"`

	// Tap is the netns TAP Firecracker attaches the guest NIC to ("" = no
	// network); the sandbox init created and routed it.
	Tap      string `json:"tap,omitempty"`
	GuestMAC string `json:"guestMac,omitempty"`
	Net      *Net   `json:"net,omitempty"`

	Hostname string   `json:"hostname,omitempty"`
	Mounts   []Mount  `json:"mounts,omitempty"` // exported to the guest, parents first
	Local    []string `json:"local,omitempty"`  // guest-local dirs (Config.Local)

	// Backends: Listen is the host path the shim serves once the guest
	// process listens on Guest.Listen; Gateway the host socket the guest's
	// Guest.Gateway reaches (vsock GatewayPort).
	Listen  string `json:"listen,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	Guest   Exec   `json:"guest"` // session 1

	Debug bool `json:"debug,omitempty"`
}

// Guest network constants: the guest owns the relay's classic sandbox
// address; the netns routes to it over the TAP.
const (
	GuestAddr = "10.0.2.15"
	GatewayIP = "10.0.2.2"
	GuestDNS  = "10.0.2.3"
	GuestMAC  = "02:78:62:00:00:02"
	TapMAC    = "02:78:62:00:00:01"
	TapName   = "vmtap0"
)
