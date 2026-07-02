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
