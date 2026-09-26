//go:build !linux

package host

import (
	"fmt"
	"os"
)

// Main refuses: VM sandboxes are Linux-only.
func Main(string) int {
	fmt.Fprintln(os.Stderr, "vm sandbox: Linux only")
	return 125
}
