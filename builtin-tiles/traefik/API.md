# Public HTTPS (Traefik) — API

The terminator's own control surface (its frontend is admin of it; nothing
else needs to call this — publishing happens via xbind bindings).

- `GET /state` (reader) — `{routes, settings, running, restarts, error,
  acmeReady}`: the routes bound through this terminator (from xbind's
  `/api/xbin/ingress-routes`), the ACME settings, and the supervised traefik
  process state.
- `POST /settings` (writer) — `{email, staging, noTls}`: ACME account email
  (required for TLS), Let's Encrypt staging toggle, or plain-HTTP mode (when
  TLS is terminated in front of this tile).

How it works end to end: **docs/ingress.md**. Traffic: internet → host
:80/:443 (runtime L4 relay, this tile's `web`/`websecure` exposes) → traefik
(TLS ends here, ACME via its own `net=internet` egress) →
`$XBIN_INGRESS_FORWARD_URL` (xbind's ingress-forward door) → the target tile,
as the anonymous `ingress` principal, confined to its declared public paths.
