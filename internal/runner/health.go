package runner

import (
	"errors"
	"net"
	"time"
)

// waitHealthy dials the backend's socket until it answers, the process exits
// (exited closes: a VM that dies at boot fails at once, not after the whole
// health timeout) or the timeout passes.
func waitHealthy(sock string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-exited:
			return errExited
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("timeout dialing backend socket")
}
