#!/usr/bin/env bash
# native/ios/scripts/ci-local-check.sh — check the Apple CI without a Mac,
# before pushing (native/AGENTS.md → "Apple"): a CI round trip costs
# minutes, a broken workflow file costs one for nothing.
#
#   1. .github/workflows/ios.yml parses (python3 + PyYAML, else ruby) and
#      keeps its shape: triggers, concurrency, the xcode-27 jobs, that no
#      job waits on another, the artifact names and paths the scripts
#      write to, no secrets, every script it runs exists and is executable
#   2. actionlint, when installed (or ACTIONLINT=/path/to/actionlint)
#   3. bash -n, then shellcheck (hack/check-sh.sh: a local binary, else the
#      pinned container image, else skipped with a note) on these scripts
#   4. ci-dry-test.sh: pick-sim.sh and the ci-*.sh scripts against fake
#      Apple tools
#   5. with CI_LOCAL_BASH32=1: ci-dry-test.sh again under bash 3.2 — the
#      bash macOS runs them with — in the bash:3.2 container (docker or
#      podman; the container installs python3, so it needs the network)
#
# Exit status: non-zero when any check fails.
set -euo pipefail

repo=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo"
wf=.github/workflows/ios.yml
scripts=native/ios/scripts
failed=""

step() { printf '\n== %s\n' "$*"; }
fail() {
  echo "FAIL: $*" >&2
  failed="$failed
  - $*"
}

# ---- 1. the workflow file ------------------------------------------------
step "workflow: parse and shape ($wf)"
if command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' 2>/dev/null; then
  python3 - "$wf" "$scripts" <<'PY' || fail "workflow shape ($wf)"
import os, re, sys, yaml

wf_path, scripts = sys.argv[1], sys.argv[2]
text = open(wf_path).read()
wf = yaml.safe_load(text)
errs = []
def check(cond, msg):
    if not cond:
        errs.append(msg)

# PyYAML reads the bare key `on` as the boolean True (YAML 1.1).
on = wf.get("on", wf.get(True)) or {}
push = on.get("push") or {}
check("master" in (push.get("branches-ignore") or []), "on.push.branches-ignore must list master (feature branches only)")
check("branches" not in push, "on.push must not also set branches")
check("tags" not in push, "on.push must not run on tags")
paths = push.get("paths") or []
for p in ("native/ios/**", "native/fixtures/**", "native/spec/**", ".github/workflows/ios.yml"):
    check(p in paths, "on.push.paths must include %s" % p)
check("workflow_dispatch" in on, "on must include workflow_dispatch")
check("pull_request" not in on and "pull_request_target" not in on, "no pull_request triggers (macOS minutes; no forks)")

conc = wf.get("concurrency") or {}
check("github.ref" in str(conc.get("group", "")), "concurrency.group must be per ref")
check(conc.get("cancel-in-progress") is True, "concurrency.cancel-in-progress must be true")
check((wf.get("permissions") or {}) == {"contents": "read"}, "permissions must be exactly contents: read")
check("secrets." not in text, "the workflow must not use secrets (no signing, no provisioning)")

jobs = wf.get("jobs") or {}
for name in ("packages", "app", "snapshots"):
    check(name in jobs, "job %s missing" % name)
for name, job in jobs.items():
    check(job.get("runs-on") == "xcode-27", "job %s: runs-on must be xcode-27" % name)
    check(isinstance(job.get("timeout-minutes"), int), "job %s: needs timeout-minutes" % name)
    check("needs" not in job, "job %s: jobs stay independent (a failing app build must not stop the snapshots)" % name)
    steps = job.get("steps") or []
    check(bool(steps) and str(steps[0].get("uses", "")).startswith("actions/checkout@"), "job %s: first step checks out" % name)
    for st in steps:
        run = st.get("run") or ""
        for ref in re.findall(r"native/ios/scripts/[A-Za-z0-9_.-]+\.sh", run):
            check(os.path.isfile(ref), "job %s runs %s, which does not exist" % (name, ref))
            check(os.access(ref, os.X_OK), "job %s runs %s, which is not executable" % (name, ref))

def uploads(job):
    return [s for s in (jobs.get(job) or {}).get("steps", []) if str(s.get("uses", "")).startswith("actions/upload-artifact@")]

names = {}
for job in jobs:
    for s in uploads(job):
        w = s.get("with") or {}
        names[w.get("name")] = (job, s, str(w.get("path", "")))
        check(s.get("if") == "always()", "upload %s: must be if: always()" % w.get("name"))
check("snapshots" in names, "an artifact named snapshots (the PNGs)")
check(any(n and n.startswith("xcresult-") for n in names), "artifacts named xcresult-*")

# The paths the scripts write to (ci-lib.sh: $RUNNER_TEMP/xbin-ci;
# ci-snapshots.sh: TEST_RUNNER_SNAPSHOT_DIR) are the paths uploaded.
if "xcresult-app" in names:
    p = names["xcresult-app"][2]
    check("${{ runner.temp }}/xbin-ci/app-build.xcresult" in p, "xcresult-app must upload $RUNNER_TEMP/xbin-ci/app-build.xcresult")
if "xcresult-snapshots" in names:
    p = names["xcresult-snapshots"][2]
    check("${{ runner.temp }}/xbin-ci/snapshots-test.xcresult" in p, "xcresult-snapshots must upload $RUNNER_TEMP/xbin-ci/snapshots-test.xcresult")
snapdir = None
for st in (jobs.get("snapshots") or {}).get("steps", []):
    if "ci-snapshots.sh" in (st.get("run") or ""):
        snapdir = (st.get("env") or {}).get("TEST_RUNNER_SNAPSHOT_DIR")
check(snapdir == "${{ runner.temp }}/snapshots", "the snapshots step sets TEST_RUNNER_SNAPSHOT_DIR=${{ runner.temp }}/snapshots")
if "snapshots" in names and snapdir:
    check(names["snapshots"][2].startswith(snapdir + "/"), "the snapshots artifact uploads from TEST_RUNNER_SNAPSHOT_DIR")

if errs:
    for e in errs:
        print("  - " + e, file=sys.stderr)
    sys.exit(1)
print("ok: %d jobs, %d artifacts (%s)" % (len(jobs), len(names), ", ".join(sorted(n for n in names if n))))
PY
elif command -v ruby >/dev/null 2>&1; then
  echo "(no PyYAML: parse only, with ruby)"
  ruby -ryaml -e 'YAML.safe_load(File.read(ARGV[0])); puts "ok: parses"' "$wf" || fail "workflow YAML ($wf)"
else
  fail "cannot parse YAML: need python3 with PyYAML (python3-yaml) or ruby"
fi

# ---- 2. actionlint -------------------------------------------------------
step "actionlint"
al=${ACTIONLINT:-$(command -v actionlint 2>/dev/null || true)}
if [ -n "$al" ] && [ -x "$al" ]; then
  cfg=$(mktemp "${TMPDIR:-/tmp}/actionlint.XXXXXX")
  # xcode-27 is the project's Apple runner label, not a GitHub-hosted one.
  printf 'self-hosted-runner:\n  labels: [xcode-27]\n' >"$cfg"
  if "$al" -config-file "$cfg" "$wf"; then echo "ok"; else fail "actionlint $wf"; fi
  rm -f "$cfg"
else
  echo "skipped: no actionlint (go install github.com/rhysd/actionlint/cmd/actionlint@latest, or ACTIONLINT=…)"
fi

# ---- 3. shell ------------------------------------------------------------
step "bash -n"
for f in "$scripts"/*.sh; do
  bash -n "$f" || fail "bash -n $f"
done
echo "ok"
step "shellcheck"
./hack/check-sh.sh "$scripts"/*.sh || fail "shellcheck $scripts"

# ---- 4. behaviour --------------------------------------------------------
step "ci-dry-test.sh"
"$scripts/ci-dry-test.sh" || fail "ci-dry-test.sh"

if [ "${CI_LOCAL_BASH32:-0}" = 1 ]; then
  step "ci-dry-test.sh under bash 3.2"
  engine=""
  for e in podman docker; do
    if command -v "$e" >/dev/null 2>&1; then engine=$e; break; fi
  done
  if [ -z "$engine" ]; then
    fail "CI_LOCAL_BASH32=1 needs docker or podman"
  else
    "$engine" run --rm -v "$repo:/w:ro" -w /w docker.io/library/bash:3.2 \
      sh -c 'apk add --no-cache python3 >/dev/null && bash native/ios/scripts/ci-dry-test.sh' ||
      fail "ci-dry-test.sh under bash 3.2"
  fi
fi

if [ -n "$failed" ]; then
  printf '\nci-local-check: FAILED%s\n' "$failed" >&2
  exit 1
fi
printf '\nci-local-check: ok\n'
