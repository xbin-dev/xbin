# S3 Archiver — API

An `archive` interface provider (see `docs/overview/14-lifecycle.md`). xbind, acting as the
owner, streams component backup archives here; this tile stores them in S3 as
`<prefix>/<key>/<version>.tar` and serves them back. In a workspace with a
vault, every archive is **sealed** by xbind (`XBINSEAL`, a cleartext header
naming the backup key's id, then AES-256-GCM): this tile sees the key's
opaque id, sizes and times, never plaintext or key material. Bind a component's
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
| PUT | `/archive/{key}` | archive stream; `X-XBin-Backup-Subkey: bk-…` when sealed | `{version, size}` |
| GET | `/archive/{key}/versions` | — | `{versions: [{version, time, size}]}` |
| GET | `/archive/{key}/versions/{v}` | `v` or `latest` | the archive stream |
| GET | `/archive/{key}/versions/{v}/file` | `?path=` | one member's bytes (a plain tar); 422 `sealed archive: xbind extracts it` for a sealed one |
| DELETE | `/archive/{key}/versions/{v}` | — | `{ok}` (a sealed version's marker goes with it) |
| POST | `/archive/erase` | `{"subkeys": ["bk-…"]}` | `{deleted}` — every version sealed under those keys, across keys |

A sealed PUT also writes an empty marker object,
`<prefix>/.subkeys/<id>/<key>/<version>` (S3 listings carry only names, sizes
and times), which `POST /archive/erase` follows when xbind erases a backup key
(`bx backup erase`, a deleted partition): the data sealed under it is
unreadable from then on, and this deletes the dead versions (`deleted` counts
the ones still there). A PUT whose marker can't be stored removes the archive
again and answers 502, so no backup names a version an erase can't find; a
retention DELETE removes the marker of the key the version's cleartext header
names. New xbinds
extract single files themselves; the `/file` route stays for older ones.

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
