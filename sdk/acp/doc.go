// Package acp is a client for the Agent Client Protocol (ACP,
// https://agentclientprotocol.com, protocol version 1) on the standard
// library alone. It drives a coding agent's ACP adapter — claude-agent-acp,
// codex-acp, gemini --acp, opencode acp — over the adapter's stdio
// (JSON-RPC 2.0, one message per line) and turns what the agent does into
// one typed stream of Events: message and thought deltas, tool calls and
// their updates, plans, permission requests and questions held until
// someone answers, status (modes, config options, slash commands, usage,
// sign-in) and the end of each turn.
//
// It is the client xbind's Agent tab runs; a tile backend runs the same one.
// One Client is one session:
//
//	c := acp.New()
//	perms := acp.NewPermissions()
//	p, _ := acp.Lookup("claude")
//	err := c.Start(ctx, acp.Config{Provider: p, Argv: p.Argv, Cwd: "/work", Spawn: spawn, Perms: perms})
//	go func() {
//		for e := range c.Events() { … } // closed when the agent is gone
//	}()
//	err = c.Prompt(ctx, acp.Prompt{Text: "fix the build"})
//	// a permission.request event names a pid: answer it
//	res, err := perms.Resolve(pid, "", acp.AllowOnce, "user:alice")
//	err = c.RespondPermission(res)
//
// The Spawner starts the adapter wherever it runs and hands back its stdio;
// ClientOptions (NewWith) are the embedder's seams: the capabilities
// advertised, where a prompt's files go, the sign-in wording, extension
// notifications, request ids, and taking over a session another process
// started (Attach: an embedder that keeps the agent running across its own
// restarts saves State with each event's Wire offset, and its successor
// resumes mid-turn).
//
// Files: rpc.go the codec (Conn, Decoder, Encode); types.go the protocol
// subset; event.go the events; permissions.go the pending requests and
// their session rules; providers.go the catalog of known adapters;
// attachments.go a prompt's files; config.go what a session and a client
// are set up with; client.go, handshake.go, updates.go, status.go,
// prompt.go, elicit.go, toolmeta.go, commands.go the client itself;
// state.go taking a session over; steer.go steering a running turn;
// auth.go signing the agent in; signin.go a CLI's own sign-in driven for a
// person (a provider's Signin, and reading what it prints).
package acp
