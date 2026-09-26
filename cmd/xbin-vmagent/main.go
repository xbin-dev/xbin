// Command xbin-vmagent is PID 1 inside a VM sandbox (plans/vm-sandbox.md):
// xbind packs it as the guest's initramfs. See internal/sandbox/vm/guest.
package main

import "github.com/xbin-dev/xbin/internal/sandbox/vm/guest"

func main() { guest.Main() }
