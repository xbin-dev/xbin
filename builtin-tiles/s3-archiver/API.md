# S3 Archiver — API

An `archive` interface provider (see `plans/lifecycle.md`). buxond, acting as the
owner, streams component backup tars here; this tile stores them in S3 as
`<prefix>/<key>/<version>.tar` and serves them back. Bind a component's
`@archive` to it, or make it the workspace default:

    bx bind '*' @archive=apps/s3-archiver

Configure the endpoint/region/bucket/prefix on the tile's page; the access key
and secret are stored in the tile's **vault** (never in a resource).

## Archive contract (called by buxond only — admin)

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

The S3 client is path-style and SigV4-signed, so it works with AWS S3, MinIO,
Cloudflare R2, Backblaze B2, and other S3-compatible stores. It has no external
dependencies; the signing is unit-tested against AWS's published vector.
