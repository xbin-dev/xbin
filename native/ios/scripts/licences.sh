#!/bin/sh
# native/ios/scripts/licences.sh — regenerate the app's open-source licences,
# App/Resources/Licences/OpenSourceLicences.json, which Settings → Open-source
# licences shows (App/Shell/Licences.swift; D204's App Store list):
#
#   - SwiftTerm, at project.yml's exactVersion (its LICENSE from that tag);
#   - the faces (App/Resources/Fonts, their OFL texts);
#   - IrohLib: n0's iroh-ffi at the tag the app pins, and every crate in its
#     Cargo.lock that builds into the app for iOS — cargo-about over the
#     normal dependencies for the targets in licences-about.toml, which also
#     lists the licences we accept (a new one fails the run: review it).
#
#   native/ios/scripts/licences.sh [--iroh-ffi <tag>]
#
# The tag defaults to project.yml's IrohLib exactVersion ("v" + it). Rerun
# whenever either pin moves, and commit the JSON with the bump.
#
# Needs network, git, curl, python3 and cargo-about 0.6.x (`cargo install
# cargo-about@0.6.6 --locked`; CARGO_ABOUT names another binary). iroh-ffi is
# edition 2024: the cargo that cargo-about runs must be 1.85 or newer
# (RUSTUP_TOOLCHAIN=nightly or a newer stable through rustup).
# clearlydefined.io is consulted when reachable; without it cargo-about reads
# the licence files the crates ship.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
ios=$(cd "$here/.." && pwd)
out=$ios/App/Resources/Licences/OpenSourceLicences.json
about=${CARGO_ABOUT:-cargo-about}

pin() { # pin <package> — its exactVersion under project.yml's packages:
  awk -v p="  $1:" '
    $0 == p { inpkg = 1; next }
    inpkg && /^  [A-Za-z]/ { inpkg = 0 }
    inpkg && /exactVersion:/ { print $2; exit }
  ' "$ios/project.yml"
}

tag=
while [ $# -gt 0 ]; do
  case $1 in
    --iroh-ffi) tag=$2; shift 2 ;;
    *) echo "usage: $0 [--iroh-ffi <tag>]" >&2; exit 2 ;;
  esac
done
if [ -z "$tag" ]; then
  v=$(pin IrohLib)
  [ -n "$v" ] || { echo "licences.sh: project.yml pins no IrohLib exactVersion; pass --iroh-ffi <tag>" >&2; exit 1; }
  tag=v$v
fi
st=$(pin SwiftTerm)
[ -n "$st" ] || { echo "licences.sh: project.yml pins no SwiftTerm exactVersion" >&2; exit 1; }
command -v "$about" >/dev/null || { echo "licences.sh: no $about (cargo install cargo-about@0.6.6 --locked)" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo ">> iroh-ffi $tag"
git -c advice.detachedHead=false clone -q --depth 1 --branch "$tag" https://github.com/n0-computer/iroh-ffi "$tmp/iroh-ffi"
targets=$(awk -F'"' '/^targets/ { for (i = 2; i < NF; i += 2) printf "%s ", $i }' "$here/licences-about.toml")
echo ">> cargo-about ($targets)"
"$about" generate --format json --locked -c "$here/licences-about.toml" \
  -m "$tmp/iroh-ffi/Cargo.toml" -o "$tmp/about.json" 2>"$tmp/about.log" || {
  grep -v 'clearly defined\|has not been harvested' "$tmp/about.log" >&2 || true
  exit 1
}
echo ">> SwiftTerm $st"
curl -fsSL "https://raw.githubusercontent.com/migueldeicaza/SwiftTerm/v$st/LICENSE" -o "$tmp/swiftterm-LICENSE"
python3 -I "$here/licences.py" --about "$tmp/about.json" --iroh-ffi "$tag" --swiftterm "$st" \
  --swiftterm-licence "$tmp/swiftterm-LICENSE" --targets "$targets" --out "$out"
