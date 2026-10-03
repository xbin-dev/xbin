#!/usr/bin/env bash
# hack/check-website.sh — the xbin.dev site's guard: `make website-guard`, part of
# `make guards` and the first half of `make website-check` (the second is the site in
# a browser, hack/website-check.mjs); `make website` runs it with --dist before it
# assembles dist/.
#
# It holds website/ to the rules in website/README.md → "Checks":
#   - the preserved files: install.sh byte-identical, app/ios.json parses as the
#     iOS app's kill switch, the privacy page's words unchanged;
#   - no third-party loads (src, srcset, stylesheet/icon/preload/prerender links,
#     url(), @import, JS imports, any absolute URL a script names, ping
#     attributes) and only the allowed external links;
#   - nothing stored: no cookies (document.cookie, cookieStore), web storage,
#     IndexedDB, WebSQL, Cache API or service worker in the JS;
#   - the per-page budgets: HTML+CSS ≤ 60 KB, JS ≤ 80 KB, fonts ≤ 200 KB,
#     first-screen images ≤ 250 KB;
#   - one header and one footer, the same markup on every page;
#   - every <img> has alt, width and height; colours only from css/tokens.css;
#   - the site's mark files are the masters in plans/brand/marks/, and the
#     wordmark's <img> sizes fit the wordmark in use;
#   - website/media.lock is well formed.
# With --dist (make website), also what may be deployed: website/media/ matches
# media.lock, website/static-helpers/ holds every set hack/helpers.sha256 lists,
# and no page carries a {{DATA}} slot, a shot waiting for its capture or a stub.
# The rules themselves are in hack/check-website.mjs.
set -euo pipefail
cd "$(dirname "$0")/.."
exec node hack/check-website.mjs "$@"
