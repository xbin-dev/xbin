//go:build linux && !amd64

package host

// tscKHz: emulated VMs are x86_64 guests on x86_64 hosts for now.
func tscKHz() int { return 0 }
