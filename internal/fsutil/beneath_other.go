//go:build !linux

package fsutil

import "os"

func openIn(root, sub, rel string) (*os.File, error) { return openInFallback(root, sub, rel) }

func openResolved(root, rel string, allow func(string) bool) (*os.File, string, error) {
	_, real, r, err := resolveIn(root, rel, allow)
	if err != nil {
		return nil, "", err
	}
	return openResolvedFallback(real, r)
}
