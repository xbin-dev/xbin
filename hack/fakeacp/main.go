// fakeacp is the scripted ACP agent for tests and the UI harness (provider
// "fake", registered when xbind runs with XBIN_AGENT_FAKE=<path to this
// binary>; the "fake" harness of hack/fakesandbox). The engine — its
// scripts, flags and the `login` subcommand — is the SDK's
// github.com/xbin-dev/xbin/sdk/acp/acptest; its package doc lists them.
package main

import "github.com/xbin-dev/xbin/sdk/acp/acptest"

func main() { acptest.Main() }
