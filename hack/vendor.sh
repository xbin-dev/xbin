#!/bin/sh
# Fetches pinned ESM/UMD builds of xbin's frontend dependencies into
# web/vendor/, and the workspace's self-hosted fonts into web/vendor/fonts/
# (D184). Run from the repo root; commit the results. These are the only
# third-party frontend deps — keep it that way (no-build rule, decision D12).
set -eu

V="web/vendor"
mkdir -p "$V"

LIT=3.3.1
XTERM=5.5.0
XTERM_FIT=0.10.0
XTERM_WEBLINKS=0.11.0
MARKED=15.0.12
HLJS=11.11.1
QRCODE=2.0.4
# Fonts (SIL OFL 1.1; each family's licence beside its files as
# fonts/OFL-<family>.txt). Instrument Sans and Bricolage Grotesque are
# Google Fonts' static instances, split latin / latin-ext (theme.css's
# unicode-range rules), as @fontsource publishes them; JetBrains Mono is the
# upstream webfont with its full character set (box drawing, blocks,
# arrows: what terminals draw).
FONTSOURCE_INSTRUMENT_SANS=5.3.0   # @fontsource/instrument-sans: Google Fonts v4
FONTSOURCE_BRICOLAGE=5.3.0         # @fontsource/bricolage-grotesque: Google Fonts v9
JETBRAINS_MONO=2.304

curl -fsSL "https://cdn.jsdelivr.net/gh/lit/dist@${LIT}/all/lit-all.min.js" -o "$V/lit-all.min.js"
curl -fsSL "https://cdn.jsdelivr.net/gh/lit/dist@${LIT}/all/lit-all.min.js.map" -o "$V/lit-all.min.js.map" || true
curl -fsSL "https://cdn.jsdelivr.net/npm/@xterm/xterm@${XTERM}/lib/xterm.js" -o "$V/xterm.js"
curl -fsSL "https://cdn.jsdelivr.net/npm/@xterm/xterm@${XTERM}/css/xterm.css" -o "$V/xterm.css"
curl -fsSL "https://cdn.jsdelivr.net/npm/@xterm/addon-fit@${XTERM_FIT}/lib/addon-fit.js" -o "$V/addon-fit.js"
curl -fsSL "https://cdn.jsdelivr.net/npm/@xterm/addon-web-links@${XTERM_WEBLINKS}/lib/addon-web-links.js" -o "$V/addon-web-links.js"
curl -fsSL "https://cdn.jsdelivr.net/npm/marked@${MARKED}/lib/marked.esm.js" -o "$V/marked.esm.js"
# highlight.js: single-file ESM with the ~36 common languages bundled
# (syntax highlighting in the Admin code/diff viewer).
curl -fsSL "https://cdn.jsdelivr.net/npm/@highlightjs/cdn-assets@${HLJS}/es/highlight.min.js" -o "$V/highlight.min.js"
# qrcode-generator (Kazuhiko Arase, MIT — the license header is in the
# file): a single-file ESM QR encoder; the shell's devices panel draws the
# xbin://enroll link with it (docs/auth.md §Device login). Loaded on demand.
curl -fsSL "https://cdn.jsdelivr.net/npm/qrcode-generator@${QRCODE}/dist/qrcode.mjs" -o "$V/qrcode.mjs"

F="$V/fonts"
mkdir -p "$F"
IS="https://cdn.jsdelivr.net/npm/@fontsource/instrument-sans@${FONTSOURCE_INSTRUMENT_SANS}"
BG="https://cdn.jsdelivr.net/npm/@fontsource/bricolage-grotesque@${FONTSOURCE_BRICOLAGE}"
JB="https://cdn.jsdelivr.net/gh/JetBrains/JetBrainsMono@v${JETBRAINS_MONO}"
for w in 400 600; do
  curl -fsSL "$IS/files/instrument-sans-latin-$w-normal.woff2" -o "$F/instrument-sans-$w.woff2"
  curl -fsSL "$IS/files/instrument-sans-latin-ext-$w-normal.woff2" -o "$F/instrument-sans-$w-ext.woff2"
done
curl -fsSL "$IS/files/instrument-sans-latin-400-italic.woff2" -o "$F/instrument-sans-400-italic.woff2"
curl -fsSL "$IS/files/instrument-sans-latin-ext-400-italic.woff2" -o "$F/instrument-sans-400-italic-ext.woff2"
for w in 600 800; do
  curl -fsSL "$BG/files/bricolage-grotesque-latin-$w-normal.woff2" -o "$F/bricolage-grotesque-$w.woff2"
  curl -fsSL "$BG/files/bricolage-grotesque-latin-ext-$w-normal.woff2" -o "$F/bricolage-grotesque-$w-ext.woff2"
done
curl -fsSL "$JB/fonts/webfonts/JetBrainsMono-Regular.woff2" -o "$F/jetbrains-mono-400.woff2"
curl -fsSL "$JB/fonts/webfonts/JetBrainsMono-Medium.woff2" -o "$F/jetbrains-mono-500.woff2"
curl -fsSL "$JB/fonts/webfonts/JetBrainsMono-Bold.woff2" -o "$F/jetbrains-mono-700.woff2"
curl -fsSL "$IS/LICENSE" -o "$F/OFL-instrument-sans.txt"
curl -fsSL "$BG/LICENSE" -o "$F/OFL-bricolage-grotesque.txt"
curl -fsSL "$JB/OFL.txt" -o "$F/OFL-jetbrains-mono.txt"

# Pin what was fetched: hack/check-pins.sh verifies the tree against this
# list (a hand-edited vendored file, or a CDN serving different bytes for
# the same version, fails the release preflight). Paths are relative to
# web/vendor/, fonts/ included.
(cd "$V" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum) > hack/vendor.sha256

echo "vendored: lit@$LIT xterm@$XTERM addon-fit@$XTERM_FIT addon-web-links@$XTERM_WEBLINKS marked@$MARKED highlight.js@$HLJS qrcode-generator@$QRCODE"
echo "fonts: instrument-sans@$FONTSOURCE_INSTRUMENT_SANS bricolage-grotesque@$FONTSOURCE_BRICOLAGE (@fontsource) jetbrains-mono@$JETBRAINS_MONO"
