#!/usr/bin/env bash
# native/ios/scripts/app-fonts.sh — the app's bundled faces (D185), made
# from the workspace's own (web/vendor/fonts, pinned by hack/vendor.sh), so
# the phone draws the glyphs the web does:
#
#   App/Resources/Fonts/BricolageGrotesque-ExtraBold.ttf   large titles
#   App/Resources/Fonts/JetBrainsMono-Regular.ttf          terminals, code
#   App/Resources/Fonts/JetBrainsMono-Bold.ttf             their bold
#   App/Resources/Fonts/OFL-*.txt                          the licences (SIL OFL 1.1)
#
# iOS takes TrueType, not WOFF2: each file is decompressed, and Bricolage
# Grotesque's latin and latin-ext halves (theme.css's unicode-range split)
# are merged into one font (fontTools), then cleaned with pyftsubset. The
# names stay as published (PostScript BricolageGrotesque96ptExtraBold-
# ExtraBold, JetBrainsMono-Regular, JetBrainsMono-Bold: XbinFaces in
# XbinRendererModel/Tokens.swift); Info.plist lists the files (UIAppFonts).
#
# Needs woff2_decompress and python3 with fontTools (pyftsubset). Run it
# after hack/vendor.sh changes a font, then commit what changed.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
src=$repo/web/vendor/fonts
out=$repo/native/ios/App/Resources/Fonts
for tool in woff2_decompress python3 pyftsubset; do
  command -v "$tool" >/dev/null || { echo "app-fonts: $tool not found" >&2; exit 1; }
done
tmp=$(mktemp -d "${TMPDIR:-/tmp}/app-fonts.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
cp "$src/bricolage-grotesque-800.woff2" "$src/bricolage-grotesque-800-ext.woff2" \
  "$src/jetbrains-mono-400.woff2" "$src/jetbrains-mono-700.woff2" "$tmp/"
for f in "$tmp"/*.woff2; do woff2_decompress "$f" >/dev/null; done
python3 - "$tmp" <<'EOF'
import sys
from fontTools.merge import Merger, Options
from fontTools.ttLib import TTFont
d = sys.argv[1] + '/'
font = Merger(options=Options(drop_tables=['STAT'])).merge(
    [d + 'bricolage-grotesque-800.ttf', d + 'bricolage-grotesque-800-ext.ttf'])
# The published font's own dates, not the merge's: the same bytes every run.
src = TTFont(d + 'bricolage-grotesque-800.ttf')['head']
font['head'].created, font['head'].modified = src.created, src.modified
font.recalcTimestamp = False
font.save(d + 'bricolage-merged.ttf')
EOF
mkdir -p "$out"
pyftsubset "$tmp/bricolage-merged.ttf" --unicodes='*' --layout-features='*' --glyph-names --notdef-outline \
  --name-IDs='*' --name-languages='*' --output-file="$out/BricolageGrotesque-ExtraBold.ttf"
cp "$tmp/jetbrains-mono-400.ttf" "$out/JetBrainsMono-Regular.ttf"
cp "$tmp/jetbrains-mono-700.ttf" "$out/JetBrainsMono-Bold.ttf"
cp "$src/OFL-bricolage-grotesque.txt" "$src/OFL-jetbrains-mono.txt" "$out/"
ls -l "$out"
