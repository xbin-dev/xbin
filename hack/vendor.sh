#!/bin/sh
# Fetches pinned ESM/UMD builds of buxon's frontend dependencies into
# web/vendor/. Run from the repo root; commit the results. These are the only
# third-party frontend deps — keep it that way (no-build rule, decision D12).
set -eu

V="web/vendor"
mkdir -p "$V"

LIT=3.3.1
XTERM=5.5.0
XTERM_FIT=0.10.0
MARKED=15.0.12

curl -fsSL "https://cdn.jsdelivr.net/gh/lit/dist@${LIT}/all/lit-all.min.js" -o "$V/lit-all.min.js"
curl -fsSL "https://cdn.jsdelivr.net/gh/lit/dist@${LIT}/all/lit-all.min.js.map" -o "$V/lit-all.min.js.map" || true
curl -fsSL "https://cdn.jsdelivr.net/npm/@xterm/xterm@${XTERM}/lib/xterm.js" -o "$V/xterm.js"
curl -fsSL "https://cdn.jsdelivr.net/npm/@xterm/xterm@${XTERM}/css/xterm.css" -o "$V/xterm.css"
curl -fsSL "https://cdn.jsdelivr.net/npm/@xterm/addon-fit@${XTERM_FIT}/lib/addon-fit.js" -o "$V/addon-fit.js"
curl -fsSL "https://cdn.jsdelivr.net/npm/marked@${MARKED}/lib/marked.esm.js" -o "$V/marked.esm.js"

echo "vendored: lit@$LIT xterm@$XTERM addon-fit@$XTERM_FIT marked@$MARKED"
