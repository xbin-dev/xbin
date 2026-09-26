//go:build !linux

package fsutil

import "os"

func openBeneath(dir, rel string) (*os.File, error) { return openBeneathFallback(dir, rel) }
