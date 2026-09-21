package acp

import "github.com/xbin-dev/xbin/internal/agent"

// Driver is the agent.Driver constructor for provider.Driver == "acp".
func Driver() agent.Driver { return New() }

var _ agent.Driver = (*Client)(nil)
