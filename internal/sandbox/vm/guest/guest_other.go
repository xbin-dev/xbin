//go:build !linux

package guest

import (
	"fmt"
	"os"
)

// Main refuses: the agent is Linux-only.
func Main() {
	fmt.Fprintln(os.Stderr, "xbin-vmagent: Linux only")
	os.Exit(2)
}
