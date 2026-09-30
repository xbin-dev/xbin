package xbin

import "sync"

// ResetClient drops the shared gateway client, so the next Client() reads
// XBIN_GATEWAY and XBIN_TOKEN again — for the external tests (package
// xbin_test), as the internal ones reset clientOnce themselves.
func ResetClient() { clientOnce = sync.Once{} }
