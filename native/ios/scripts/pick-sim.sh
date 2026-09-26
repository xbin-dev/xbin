#!/usr/bin/env bash
# native/ios/scripts/pick-sim.sh — choose the iOS simulator CI builds and
# tests on, and print its xcodebuild destination on stdout:
#
#   platform=iOS Simulator,id=<UDID>
#
# Rule: an iPhone before anything else; among iPhones the newest iOS
# runtime, then the newest model generation (hardware modelIdentifier
# "iPhone18,3" → 18, else the first number in the name), then the shortest
# name (the base model before Pro/Max). No iPhone → any iOS simulator (an
# iPad) by the same order. No iOS simulator at all but an iOS runtime →
# create one from the newest iPhone device type the newest runtime
# supports. Unavailable devices and runtimes never count. The choice (name,
# runtime, UDID) goes to stderr.
#
#   XBIN_SIM="iPhone 17 Pro"   prefer this device name when it exists
#   XBIN_SIM_ENSURE=xbin-e2e   use the device of exactly this name, creating
#                              it (newest runtime, newest iPhone, as above)
#                              when there is none — a simulator of its own
#                              that a run may erase (mac-setup.sh, e2e)
#
# Runs under macOS's bash 3.2 (no mapfile, no associative arrays). Needs
# python3 (Xcode's command line tools ship it) to read `simctl list -j`.
set -euo pipefail

say() { echo "pick-sim: $*" >&2; }

command -v xcrun >/dev/null 2>&1 || { say "xcrun not found — this runs on macOS with Xcode"; exit 1; }
command -v python3 >/dev/null 2>&1 || { say "python3 not found"; exit 1; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/pick-sim.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# One listing: devices, device types, runtimes (and pairs).
xcrun simctl list -j >"$tmp/list.json"

# The picker, written out first: bash 3.2 mis-parses heredocs inside $(…).
cat >"$tmp/pick.py" <<'PY'
import json, os, re, sys

with open(sys.argv[1]) as f:
    data = json.load(f)
want = os.environ.get("XBIN_SIM", "")
ensure = os.environ.get("XBIN_SIM_ENSURE", "")

def vkey(s):
    return tuple(int(x) for x in re.findall(r"\d+", s or ""))

runtimes = {}
for r in data.get("runtimes", []):
    ident = r.get("identifier", "")
    ios = r.get("platform") == "iOS" or ".SimRuntime.iOS-" in ident
    if ios and r.get("isAvailable", True):
        ver = r.get("version") or ident.rsplit("iOS-", 1)[-1].replace("-", ".")
        runtimes[ident] = {"version": ver, "name": r.get("name", "iOS " + ver),
                           "types": r.get("supportedDeviceTypes") or []}

dtypes = {t.get("identifier"): t for t in data.get("devicetypes", [])}

def family(name, type_id):
    t = dtypes.get(type_id) or {}
    fam = t.get("productFamily")
    if fam:
        return fam
    return "iPhone" if name.startswith("iPhone") else "other"

def generation(name, type_id):
    t = dtypes.get(type_id) or {}
    m = re.match(r"iPhone(\d+),(\d+)$", t.get("modelIdentifier", ""))
    if m:
        return int(m.group(1))
    m = re.search(r"\d+", name)
    return int(m.group(0)) if m else 0

def rank(name, type_id, runtime_version):
    # Sorted descending: iPhones first, then newer runtime, newer
    # generation, shorter name; the name itself breaks the last tie.
    return (family(name, type_id) == "iPhone", vkey(runtime_version),
            generation(name, type_id), -len(name), [-ord(c) for c in name])

devices = []
for rid, devs in (data.get("devices") or {}).items():
    rt = runtimes.get(rid)
    if rt is None:
        continue
    for d in devs:
        if d.get("isAvailable") is False or not d.get("udid"):
            continue
        devices.append((d.get("name", ""), d.get("deviceTypeIdentifier", ""), rt, d["udid"]))

def emit(*fields):
    print("\t".join(fields))
    sys.exit(0)

if ensure:
    named = [d for d in devices if d[0] == ensure]
    if named:
        n, tid, rt, udid = max(named, key=lambda d: rank(d[0], d[1], d[2]["version"]))
        emit("device", udid, "%s (%s)" % (n, rt["name"]))
    devices = []  # none of that name: create it below

if want:
    named = [d for d in devices if d[0] == want]
    if named:
        n, tid, rt, udid = max(named, key=lambda d: rank(d[0], d[1], d[2]["version"]))
        emit("device", udid, "%s (%s)" % (n, rt["name"]))
    print("XBIN_SIM=%r is not an available iOS simulator; picking by the rule" % want, file=sys.stderr)

if devices:
    n, tid, rt, udid = max(devices, key=lambda d: rank(d[0], d[1], d[2]["version"]))
    emit("device", udid, "%s (%s)" % (n, rt["name"]))

if not runtimes:
    print("no available iOS simulator runtime (xcodebuild -downloadPlatform iOS)", file=sys.stderr)
    sys.exit(3)

rid = max(runtimes, key=lambda i: vkey(runtimes[i]["version"]))
rt = runtimes[rid]
types = [(t.get("name", ""), t.get("identifier", "")) for t in rt["types"]]
if not types:
    types = [(t.get("name", ""), t.get("identifier", "")) for t in dtypes.values()
             if t.get("productFamily") in ("iPhone", "iPad")]
types = [t for t in types if t[1]]
if not types:
    print("iOS runtime %s lists no device types to create a simulator from" % rt["name"], file=sys.stderr)
    sys.exit(3)
n, tid = max(types, key=lambda t: rank(t[0], t[1], rt["version"]))
emit("create", n, tid, rid, "%s (%s)" % (n, rt["name"]))
PY

choice=$(XBIN_SIM="${XBIN_SIM:-}" XBIN_SIM_ENSURE="${XBIN_SIM_ENSURE:-}" python3 "$tmp/pick.py" "$tmp/list.json")
IFS=$'\t' read -r kind f1 f2 f3 f4 <<<"$choice"
case $kind in
device)
  udid=$f1 desc=$f2
  ;;
create)
  name=$f1 type_id=$f2 runtime_id=$f3 desc=$f4
  if [ -n "${XBIN_SIM_ENSURE:-}" ]; then
    say "no simulator named $XBIN_SIM_ENSURE; creating it: $desc"
    udid=$(xcrun simctl create "$XBIN_SIM_ENSURE" "$type_id" "$runtime_id")
    desc="$XBIN_SIM_ENSURE — $desc"
  else
    say "no iOS simulator exists; creating $desc"
    udid=$(xcrun simctl create "xbin-ci $name" "$type_id" "$runtime_id")
  fi
  ;;
*)
  say "unexpected picker output: $choice"
  exit 1
  ;;
esac

say "$desc $udid"
echo "platform=iOS Simulator,id=$udid"
