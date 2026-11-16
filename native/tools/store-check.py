#!/usr/bin/env python3
"""native/tools/store-check.py — the app's App Store metadata agrees with
its code (native/AGENTS.md → "App Store"). Runs anywhere python3 does
(ci-local-check.sh, so ci.yml's Linux native job too):

  1. App/Resources/PrivacyInfo.xcprivacy parses, says no tracking and no
     tracking domains, and declares a required-reason category for every
     one the app's code uses (UserDefaults in our sources; SwiftTerm's
     stat/fstat, a file-timestamp API, while the app links SwiftTerm;
     IROHLIB_APIS while it links IrohLib — app tunnels, D204)
  2. what it declares as collected matches the push relay the app ships
     with: none while Support/App-Info.plist's XbinPushRelay is empty (push
     off, nothing leaves for a relay); with a relay, the APNs token the app
     registers there — Device ID, not linked, not tracking, App
     Functionality — which App Store Connect's App Privacy must say too
  3. the export-compliance answer: ITSAppUsesNonExemptEncryption is false —
     the owner answered App Store Connect's questions for the app tunnel's
     encryption (standard algorithms; no documents needed) and App Store
     Connect said to declare it here (native/AGENTS.md → "Export compliance")
  4. the Local Network purpose string names the tunnel (iOS shows it before
     the tunnel's first direct path on the LAN)
  5. App/Resources/AppIcon.icon parses, every layer's image exists, and the
     app target names it (ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon)
  6. the open-source licences screen's list (App/Resources/Licences/
     OpenSourceLicences.json, native/ios/scripts/licences.sh) was made for
     the SwiftTerm and IrohLib versions project.yml pins
  7. Support/review-notes.md (the App Review Notes draft): what follows its
     rule fits App Store Connect's 4000 characters, and it describes the
     features the app has that a reviewer must be told about (guideline
     2.5.19) — the workspace agent while the app ships its chrome (D233)
  8. the Notification Service Extension holds no credential (D241): its
     sources are the push keys' and the settings' files only, and name no
     session, device key or workspace call; and what really keeps them out:
     the app's keychain groups are [its own, the shared one] in that order
     (an item written without a group lands in the first), the extension's
     and the widgets' only the shared one, and the credential stores write
     without a group

Exit status: non-zero on any failure.
"""
import json
import os
import plistlib
import re
import sys

ios = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "ios")
errs = []

# The required-reason categories IrohLib's binary needs: from `nm -u` on a build
# that links it (plans/app-tunnel.md Phase 6) — category → reasons. Measured on
# iroh-ffi 1.2.0 (2026-10-27, ios-arm64 libiroh_ffi.a and the Release app):
# fstat (Rust's std sizing what it reads) — File timestamp, the reason the app
# already gives; no mach_absolute_time/systemUptime (boot time), no
# statfs/statvfs family (disk space), no UserDefaults. Its clock_gettime,
# sysctl(byname), getifaddrs, open/opendir are not required-reason APIs.
# Re-measure with every bump, together with App/Resources/PrivacyInfo.xcprivacy.
IROHLIB_APIS = {"NSPrivacyAccessedAPICategoryFileTimestamp": ["C617.1"]}


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
links_iroh = bool(re.search(r"^\s*- package: IrohLib\s*$", project, re.M)
                  or re.search(r"^  IrohLib:\s*$", project, re.M))
if links_iroh:
    for api in IROHLIB_APIS:
        needed.setdefault(api, "IrohLib (nm -u)")
for api, where in needed.items():
    check(api in declared, f"{manifest_rel}: {api} is used ({where}) but not declared")
if links_iroh:
    for api, reasons in IROHLIB_APIS.items():
        for r in reasons:
            check(r in declared.get(api, []), f"{manifest_rel}: {api} needs reason {r} for IrohLib")

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
check(info.get("ITSAppUsesNonExemptEncryption") is False,
      "Support/App-Info.plist: ITSAppUsesNonExemptEncryption must be false — App Store Connect's verdict on the "
      "owner's answers (standard algorithms, no documents needed); change it only with a new answer there "
      "(native/AGENTS.md → \"Export compliance\")")

# ---- 4. the Local Network purpose string ---------------------------------
local = str(info.get("NSLocalNetworkUsageDescription", ""))
check("tunnel" in local.lower(),
      "Support/App-Info.plist: NSLocalNetworkUsageDescription must say the app connects through the workspace's "
      "tunnel (iroh asks for the local network to find a direct path)")

# ---- 5. the icon ---------------------------------------------------------
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

# ---- 6. the licences list matches the pins -------------------------------
def pin(package):
    m = re.search(r"^  %s:\s*\n((?:    .*\n)+)" % re.escape(package), project, re.M)
    v = m and re.search(r"^\s*exactVersion:\s*(\S+)", m.group(1), re.M)
    return v.group(1) if v else None


licences_rel = "App/Resources/Licences/OpenSourceLicences.json"
try:
    with open(os.path.join(ios, licences_rel), encoding="utf-8") as f:
        lic = json.load(f)
    src = lic.get("source", {})
    check(src.get("swiftTerm") == pin("SwiftTerm"),
          f"{licences_rel}: made for SwiftTerm {src.get('swiftTerm')}, project.yml pins {pin('SwiftTerm')} — rerun native/ios/scripts/licences.sh")
    if links_iroh:
        want = pin("IrohLib")
        check(want and src.get("irohFFI") == "v" + want,
              f"{licences_rel}: made for iroh-ffi {src.get('irohFFI')}, project.yml pins IrohLib {want} — rerun native/ios/scripts/licences.sh")
    texts = lic.get("texts", {})
    for g in lic.get("groups", []):
        for e in g.get("entries", []):
            check(e.get("texts") and all(t in texts for t in e["texts"]), f"{licences_rel}: {e.get('name')} has no licence text")
except (OSError, ValueError) as e:
    errs.append(f"{licences_rel}: {e}")

# 7. The App Review Notes draft fits the field and names what the app has.
notes_rel = "Support/review-notes.md"
try:
    with open(os.path.join(ios, notes_rel), encoding="utf-8") as f:
        notes = f.read()
    body = notes.split("\n---\n", 1)[1] if "\n---\n" in notes else notes
    check(len(body) <= 4000, f"{notes_rel}: the notes are {len(body)} characters, App Store Connect takes 4000")
    if os.path.exists(os.path.join(ios, "App/Model/WorkspaceAgentModel.swift")):
        check("workspace agent" in body.lower(), f"{notes_rel}: the app has the workspace agent (D233) and the notes don't describe it")
except OSError as e:
    errs.append(f"{notes_rel}: {e}")

# 8. The Notification Service Extension holds no credential (D241): it opens
# pushes with the per-workspace push keys and reads the person's notification
# settings — nothing else. Its sources (project.yml's list) never name a
# session, a device key, the app's auth or a way to call a workspace, so a
# notification's action can't be decided there: only the app decides, with
# the person's session, after re-reading the request.
nse_allowed = {"NotificationService", "Shared/PushCrypto.swift", "Shared/Keychain.swift", "Shared/AgentNoticeStore.swift", "Shared/AgentNoticeDraw.swift"}
try:
    with open(os.path.join(ios, "project.yml"), encoding="utf-8") as f:
        proj = f.read()
    m = re.search(r"\n  XbinNotificationService:\n(.*?)\n  [A-Za-z]+:\n", proj, re.S)
    nse = m.group(1) if m else ""
    srcs = set(re.findall(r"- path: (\S+)", nse.split("dependencies:")[0]))
    check(srcs and srcs <= nse_allowed, f"project.yml: the notification extension's sources are {sorted(srcs)}; only {sorted(nse_allowed)} may be (it holds no credential)")
    forbidden = ["WorkspaceAuth", "SessionCredential", "DeviceKey", "EnclaveKey", "dev.xbin.session", "APITransport",
                 "URLSession", "AppTransport", "Authorization", "approve", "DelegateRoute"]
    files = [os.path.join(ios, p) for p in srcs if p.endswith(".swift")]
    d = os.path.join(ios, "NotificationService")
    files += [os.path.join(d, f) for f in os.listdir(d) if f.endswith(".swift")] if os.path.isdir(d) else []
    for path in files:
        with open(path, encoding="utf-8") as f:
            code = "\n".join(l for l in f.read().splitlines() if not l.strip().startswith("//"))
        for word in forbidden:
            check(word not in code, f"{os.path.relpath(path, ios)}: the notification extension names {word!r} — it holds no credential and decides nothing")
except OSError as e:
    errs.append(f"project.yml: {e}")

# 8b. What actually keeps credentials from the extension (review finding 8):
# a Keychain item written without an access group lands in the FIRST group
# of the writer's keychain-access-groups. The app's first group is its own
# (dev.xbin.app); the shared one (dev.xbin.shared) comes second, and the
# extension and the widgets hold only the shared one. Sessions, device keys
# and tunnel secrets are written without a group — so into the app's own —
# and the shared group the Info.plists name is dev.xbin.shared.
PRIVATE, SHARED = "$(AppIdentifierPrefix)dev.xbin.app", "$(AppIdentifierPrefix)dev.xbin.shared"
def groups(rel):
    try:
        return load_plist(rel).get("keychain-access-groups")
    except (OSError, plistlib.InvalidFileException) as e:
        errs.append(f"{rel}: {e}")
        return None
check(groups("Support/Xbin.entitlements") == [PRIVATE, SHARED],
      f"Support/Xbin.entitlements: keychain-access-groups must be [{PRIVATE}, {SHARED}] in that order — items written without a group land in the first")
for rel in ("Support/NotificationService.entitlements", "Support/Widgets.entitlements"):
    check(groups(rel) == [SHARED], f"{rel}: keychain-access-groups must be [{SHARED}] alone — the extension never reads the app's own group")
    try:
        extra = sorted(k for k in load_plist(rel) if k != "keychain-access-groups")
        check(not extra, f"{rel}: entitlements beyond the shared keychain group: {extra}")
    except (OSError, plistlib.InvalidFileException):
        pass
for rel in ("Support/App-Info.plist", "Support/NotificationService-Info.plist"):
    try:
        check(load_plist(rel).get("XbinKeychainGroup") == SHARED, f"{rel}: XbinKeychainGroup must be {SHARED}")
    except (OSError, plistlib.InvalidFileException) as e:
        errs.append(f"{rel}: {e}")
for rel in ("App/Model/Stores.swift", "App/Model/DeviceKeys.swift", "App/Model/TunnelKeyStore.swift"):
    try:
        with open(os.path.join(ios, rel), encoding="utf-8") as f:
            code = f.read()
        for call in re.findall(r"Keychain\.(?:write|read|delete)\((?:[^()]|\([^()]*\))*\)", code):
            check("group:" not in call, f"{rel}: a credential written with an access group ({call[:60]}…) — it must land in the app's own, the first")
    except OSError as e:
        errs.append(f"{rel}: {e}")

if errs:
    for e in errs:
        print("store-check: " + e, file=sys.stderr)
    sys.exit(1)
print(f"store-check: ok (declared: {', '.join(sorted(a.replace('NSPrivacyAccessedAPICategory', '') for a in declared))}; "
      f"collected: {len(collected)}; relay: {relay or 'none'}; IrohLib: {'linked' if links_iroh else 'not linked'})")
