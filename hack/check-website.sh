#!/usr/bin/env bash
# hack/check-website.sh — the xbin.dev site's guard: `make website-guard`, part of
# `make guards` and the first half of `make website-check` (the second is the site in
# a browser, hack/website-check.mjs); `make website` runs it with --dist before it
# assembles dist/.
#
# It holds website/ to the rules in website/README.md → "Checks":
#   - the preserved files: install.sh byte-identical, app/ios.json parses as the
#     iOS app's kill switch, the privacy page's words unchanged;
#   - no third-party loads (src, srcset, stylesheet/icon/preload links, url(),
#     @import, JS imports) and only the allowed external links;
#   - no document.cookie, localStorage, sessionStorage or indexedDB in the JS;
#   - the per-page budgets: HTML+CSS ≤ 60 KB, JS ≤ 80 KB, fonts ≤ 200 KB,
#     first-screen images ≤ 250 KB;
#   - one header and one footer, the same markup on every page;
#   - every <img> has alt, width and height; colours only from css/tokens.css;
#   - the site's mark files are the masters in plans/brand/marks/;
#   - website/media.lock is well formed (--dist: website/media/ matches it).
# The rules themselves are in hack/check-website.mjs.
set -euo pipefail
cd "$(dirname "$0")/.."
exec node hack/check-website.mjs "$@"
