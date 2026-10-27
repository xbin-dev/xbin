#!/usr/bin/env python3
"""native/ios/scripts/licences.py — writes App/Resources/Licences/OpenSourceLicences.json,
what Settings → Open-source licences shows (App/Shell/Licences.swift). licences.sh runs
it; by hand:

  licences.py --about <cargo-about json> --iroh-ffi <tag> --swiftterm <version> \
              --swiftterm-licence <LICENSE file> --out <json>

The file: {"v":1, "source":{…}, "groups":[{"title","footer"?,"entries":[{"name",
"version"?,"licence","url"?,"texts":[id…]}]}], "texts":{id: text}}. Texts are shared by
id (the first 12 hex digits of their SHA-256), so the 300-odd Rust crates under a
handful of licences cost each text once. Deterministic: the same inputs give the same
bytes, so a regenerated file diffs only where a dependency changed.
"""
import argparse
import hashlib
import json
import os
import re
import sys

ios = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
repo = os.path.normpath(os.path.join(ios, "..", ".."))


def text_id(text):
    return "t" + hashlib.sha256(text.encode("utf-8")).hexdigest()[:12]


def clean(text):
    # Licence files come with CRLFs, tabs and trailing spaces; the screen shows them as
    # plain text.
    lines = [l.rstrip() for l in text.replace("\r\n", "\n").replace("\r", "\n").split("\n")]
    return "\n".join(lines).strip() + "\n"


def vendor_pin(name):
    with open(os.path.join(repo, "hack", "vendor.sh"), encoding="utf-8") as f:
        m = re.search(r"^%s=(\S+)" % re.escape(name), f.read(), re.M)
    return m.group(1) if m else None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--about", required=True, help="cargo-about generate --format json output")
    ap.add_argument("--iroh-ffi", required=True, help="the iroh-ffi tag IrohLib is pinned to")
    ap.add_argument("--swiftterm", required=True, help="SwiftTerm's exact version (project.yml)")
    ap.add_argument("--swiftterm-licence", required=True, help="SwiftTerm's LICENSE at that version")
    ap.add_argument("--targets", default="", help="the targets cargo-about filtered for (for the record)")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()

    texts = {}

    def add(text):
        t = clean(text)
        i = text_id(t)
        texts[i] = t
        return i

    groups = []

    # The Swift packages the app links from outside the repository (project.yml).
    with open(a.swiftterm_licence, encoding="utf-8") as f:
        st = add(f.read())
    groups.append({"title": "Swift packages", "entries": [
        {"name": "SwiftTerm", "version": a.swiftterm, "licence": "MIT",
         "url": "https://github.com/migueldeicaza/SwiftTerm", "texts": [st]},
    ]})

    # The faces (D185): App/Resources/Fonts, their OFL texts beside them.
    fonts = os.path.join(ios, "App", "Resources", "Fonts")
    entries = []
    for name, file, url, pin in [
        ("Bricolage Grotesque", "OFL-bricolage-grotesque.txt", "https://github.com/ateliertriay/bricolage",
         "FONTSOURCE_BRICOLAGE"),
        ("JetBrains Mono", "OFL-jetbrains-mono.txt", "https://github.com/JetBrains/JetBrainsMono", "JETBRAINS_MONO"),
    ]:
        with open(os.path.join(fonts, file), encoding="utf-8") as f:
            e = {"name": name, "licence": "OFL-1.1", "url": url, "texts": [add(f.read())]}
        v = vendor_pin(pin)
        if v and pin == "JETBRAINS_MONO":
            e["version"] = v
        entries.append(e)
    groups.append({"title": "Fonts", "entries": entries})

    # IrohLib: every crate cargo-about found in iroh-ffi's graph for the iOS targets.
    with open(a.about, encoding="utf-8") as f:
        about = json.load(f)
    by_crate = {}
    for lic in about.get("licenses", []):
        i = add(lic["text"])
        for u in lic.get("used_by", []):
            c = u["crate"]
            key = (c["name"], c["version"])
            e = by_crate.setdefault(key, {
                "name": c["name"], "version": c["version"],
                "licence": c.get("license") or lic.get("id") or "",
                "texts": [],
            })
            url = c.get("repository") or c.get("homepage")
            if url:
                e["url"] = url
            if i not in e["texts"]:
                e["texts"].append(i)
    if not by_crate:
        sys.exit("licences.py: the cargo-about output names no crates")
    rust = sorted(by_crate.values(), key=lambda e: (e["name"] != "iroh-ffi", e["name"], e["version"]))
    for e in rust:
        e["texts"].sort()
    groups.append({
        "title": "IrohLib (app tunnels)",
        "footer": "n0's iroh-ffi %s and the Rust crates it builds into the app for iOS, as cargo-about "
                  "found them in its Cargo.lock." % a.iroh_ffi,
        "entries": rust,
    })

    out = {
        "v": 1,
        "source": {"by": "native/ios/scripts/licences.sh", "irohFFI": a.iroh_ffi, "swiftTerm": a.swiftterm,
                   "targets": [t for t in a.targets.split() if t]},
        "groups": groups,
        "texts": texts,
    }
    os.makedirs(os.path.dirname(os.path.abspath(a.out)), exist_ok=True)
    with open(a.out, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=1, sort_keys=True, ensure_ascii=False)
        f.write("\n")
    n = sum(len(g["entries"]) for g in groups)
    print("licences.py: %d entries, %d texts → %s" % (n, len(texts), os.path.relpath(a.out, repo)))


if __name__ == "__main__":
    main()
