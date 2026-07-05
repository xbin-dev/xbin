# S3 Archiver — API

An `archive` interface provider (see `plans/lifecycle.md`). xbind, acting as the
owner, streams component backup tars here; this tile stores them in S3 as
`<prefix>/<key>/<version>.tar` and serves them back. Bind a component's
`@archive` to it, or make it the workspace default:

    bx bind '*' @archive=apps/s3-archiver

Configure the endpoint/region/bucket/prefix on the tile's page; the access key
and secret are stored in the tile's **vault** (never in a resource).

It reaches the S3 endpoint over the network, so it declares a `net` interface the
owner must bind (it has zero egress under isolation until then):

    bx bind apps/s3-archiver net=internet     # public bucket (AWS/R2/B2)
    # or bind to a lan:<cidr> / a provider tile for a LAN MinIO or a VPN/proxy

## Archive contract (called by xbind only — admin)

| Method | Path | Body / Query | Returns |
|--------|------|--------------|---------|
| PUT | `/archive/{key}` | tar stream | `{version, size}` |
| GET | `/archive/{key}/versions` | — | `{versions: [{version, time, size}]}` |
| GET | `/archive/{key}/versions/{v}` | `v` or `latest` | the tar stream |
| GET | `/archive/{key}/versions/{v}/file` | `?path=` | one member's bytes |
| DELETE | `/archive/{key}/versions/{v}` | — | `{ok}` |

## Settings (this tile's own frontend)

| Method | Path | Returns |
|--------|------|---------|
| GET | `/config` | `{config:{endpoint,region,bucket,prefix}, hasCreds}` |
| PUT | `/config` | `{ok}` — save endpoint/region/bucket/prefix |
| POST | `/check` | `{ok, bucket}` — probe the bucket with the stored config + creds; `{error}` (with a net-binding hint on a dial failure) if unreachable |

Credentials are written straight to the tile's **vault** by the settings page
(`PUT /api/xbin/vault/<self>/{accessKey,secretKey}` with a `{"value":…}` body)
and read by the backend with `xbin.Secret` — they never pass through `/config`
or a resource. The page runs a `/check` on every save so a bad endpoint, wrong
keys, or an unbound `net` interface surface immediately.

The S3 client is path-style and SigV4-signed, so it works with AWS S3, MinIO,
Cloudflare R2, Backblaze B2, and other S3-compatible stores. It has no external
dependencies; the signing is unit-tested against AWS's published vector.
