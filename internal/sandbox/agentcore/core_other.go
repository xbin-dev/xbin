//go:build !linux

// Package agentcore is the exec core that runs inside a Linux sandbox (the
// VM guest's agent, `bx __sbx-agent`); elsewhere it is empty.
package agentcore
