package broker

// RefreshLimitAsks is W1's (the store re-reads the manifests' asks and
// primes the max clamp LimitGrant uses); boot calls it once right after
// SetLimitSources. This no-op stands in on the runtime's branch: Phase 2
// deletes this file when the store lands (D203).
func (b *Broker) RefreshLimitAsks() {}
