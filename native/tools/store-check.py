#!/usr/bin/env python3
"""native/tools/store-check.py — the app's App Store metadata agrees with
its code (native/AGENTS.md → "App Store"). Runs anywhere python3 does
(ci-local-check.sh, so ci.yml's Linux native job too):

  1. App/Resources/PrivacyInfo.xcprivacy parses, says no tracking and no
     tracking domains, and declares a required-reason category for every
     one the app's code uses (UserDefaults in our sources; SwiftTerm's
     stat/fstat, a file-timestamp API, while the app links SwiftTerm)
  2. what it declares as collected matches the push relay the app ships
     with: none while Support/App-Info.plist's XbinPushRelay is empty (push
     off, nothing leaves for a relay); with a relay, the APNs token the app
     registers there — Device ID, not linked, not tracking, App
     Functionality — which App Store Connect's App Privacy must say too
  3. the export-compliance answer (ITSAppUsesNonExemptEncryption) is set
  4. App/Resources/AppIcon.icon parses, every layer's image exists, and the
     app target names it (ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon)

Exit status: non-zero on any failure.
"""
import json
import os
import plistlib
import re
import sys

ios = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "ios")
errs = []


def check(cond, msg):
    if not cond:
        errs.append(msg)


def load_plist(rel):
    with open(os.path.join(ios, rel), "rb") as f:
        return plistlib.load(f)


def swift_files(*rels):
    for rel in rels:
        for root, dirs, files in os.walk(os.path.join(ios, rel)):
            dirs[:] = [d for d in dirs if d not in (".build", "Tests")]
            for n in files:
                if n.endswith(".swift"):
                    yield os.path.join(root, n)


manifest_rel = "App/Resources/PrivacyInfo.xcprivacy"
manifest = load_plist(manifest_rel)
info = load_plist("Support/App-Info.plist")
with open(os.path.join(ios, "project.yml")) as f:
    project = f.read()

# ---- 1. tracking and required-reason APIs --------------------------------
check(manifest.get("NSPrivacyTracking") is False, f"{manifest_rel}: NSPrivacyTracking must be false")
check(manifest.get("NSPrivacyTrackingDomains") == [], f"{manifest_rel}: NSPrivacyTrackingDomains must be empty")
declared = {
    a.get("NSPrivacyAccessedAPIType"): a.get("NSPrivacyAccessedAPITypeReasons") or []
    for a in manifest.get("NSPrivacyAccessedAPITypes", [])
}
for api, reasons in declared.items():
    check(reasons, f"{manifest_rel}: {api} without a reason")

needed = {}  # category → where it is used
# The app target's sources and the local packages it links.
pattern = re.compile(r"\bUserDefaults\b|@AppStorage\b")
for path in swift_files("App", "Shared", "Widgets/Shared", "Packages"):
    with open(path, encoding="utf-8") as f:
        if pattern.search(f.read()):
            needed.setdefault("NSPrivacyAccessedAPICategoryUserDefaults", os.path.relpath(path, ios))
if re.search(r"^\s*- package: SwiftTerm\s*$", project, re.M):
    needed["NSPrivacyAccessedAPICategoryFileTimestamp"] = "SwiftTerm (KittyGraphics.swift: stat, fstat)"
for api, where in needed.items():
    check(api in declared, f"{manifest_rel}: {api} is used ({where}) but not declared")

# ---- 2. collected data vs the push relay ---------------------------------
relay = str(info.get("XbinPushRelay", "")).strip()
collected = manifest.get("NSPrivacyCollectedDataTypes", [])
if not relay:
    check(collected == [], f"{manifest_rel}: declares collected data, but XbinPushRelay is empty (push is off, nothing is collected)")
else:
    device = [c for c in collected if c.get("NSPrivacyCollectedDataType") == "NSPrivacyCollectedDataTypeDeviceID"]
    check(device, f"XbinPushRelay is {relay}: {manifest_rel} must declare NSPrivacyCollectedDataTypeDeviceID (the APNs token the relay keeps) — and App Store Connect's App Privacy the same")
    for c in device:
        check(c.get("NSPrivacyCollectedDataTypeLinked") is False, "DeviceID: NSPrivacyCollectedDataTypeLinked must be false (the relay knows no user)")
        check(c.get("NSPrivacyCollectedDataTypeTracking") is False, "DeviceID: NSPrivacyCollectedDataTypeTracking must be false")
        check("NSPrivacyCollectedDataTypePurposeAppFunctionality" in (c.get("NSPrivacyCollectedDataTypePurposes") or []),
              "DeviceID: purpose must include NSPrivacyCollectedDataTypePurposeAppFunctionality")

# ---- 3. export compliance ------------------------------------------------
check(isinstance(info.get("ITSAppUsesNonExemptEncryption"), bool),
      "Support/App-Info.plist: ITSAppUsesNonExemptEncryption must be set (App Store Connect asks otherwise)")

# ---- 4. the icon ---------------------------------------------------------
icon = os.path.join(ios, "App/Resources/AppIcon.icon")
try:
    with open(os.path.join(icon, "icon.json")) as f:
        doc = json.load(f)
    layers = [l for g in doc.get("groups", []) for l in g.get("layers", [])]
    check(layers, "AppIcon.icon: no layers")
    for l in layers:
        name = l.get("image-name", "")
        check(name and os.path.isfile(os.path.join(icon, "Assets", name)), f"AppIcon.icon: layer image {name!r} not in Assets/")
except (OSError, ValueError) as e:
    errs.append(f"AppIcon.icon/icon.json: {e}")
check(re.search(r"^\s*ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon\s*$", project, re.M),
      "project.yml: the app target needs ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon")

if errs:
    for e in errs:
        print("store-check: " + e, file=sys.stderr)
    sys.exit(1)
print(f"store-check: ok (declared: {', '.join(sorted(a.replace('NSPrivacyAccessedAPICategory', '') for a in declared))}; "
      f"collected: {len(collected)}; relay: {relay or 'none'})")
