package auth

import (
	"context"
	"net/http"
)

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalOf returns the verified principal stored by the auth middleware.
// Zero value (not owner, no component) if the middleware didn't run.
func PrincipalOf(r *http.Request) Principal {
	p, _ := r.Context().Value(ctxKey{}).(Principal)
	return p
}

type agentReadKey struct{}

// WithAgentRead marks a request xbind itself judged a read made by a
// person's agent though its method isn't GET: an MCP server's read method
// (initialize, ping, tools/list) or the call of a tool its server marks
// read-only, under the delegate gate's rules (D239). A tile at agent level
// "read" takes it as it takes a GET (proxy.ServeHTTP). Only xbind's own
// code sets it: it is a context value, never a header.
func WithAgentRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentReadKey{}, true)
}

// AgentRead reports a request marked with WithAgentRead.
func AgentRead(ctx context.Context) bool {
	v, _ := ctx.Value(agentReadKey{}).(bool)
	return v
}
