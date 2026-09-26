#!/usr/bin/env bash
# native/ios/scripts/ci-local-check.sh — check the Apple CI without a Mac,
# before pushing (native/AGENTS.md → "Apple"): a CI round trip costs
# minutes, a broken workflow file costs one for nothing. ci.yml's Linux
# native job runs it too.
#
#   1. .github/workflows/ios.yml parses (python3 + PyYAML, else ruby) and
#      keeps its shape: push + workflow_dispatch only (never a pull_request:
#      the runner may be self-hosted), concurrency, every job's runner from
#      vars.XBIN_IOS_RUNNER with "xcode-27" as the fallback (the expression
#      is evaluated here for an unset and a self-hosted value), no job
#      waiting on another, the cache wiring (ci-cache.sh's subdirs are the
#      ones the job's scripts build into; actions/cache only in actions
#      mode), current action majors, the artifact names and paths the
#      scripts write to, no secrets, every script it runs exists and is
#      executable; ci.yml keeps its test job, has the native job, and no
#      job of it can land on a self-hosted runner
#   2. actionlint on both workflows, when installed (or ACTIONLINT=…; with
#      CI_LOCAL_FETCH_ACTIONLINT=1 the pinned release is fetched, checked
#      against its SHA-256)
#   3. bash -n, then shellcheck (hack/check-sh.sh: a local binary, else the
#      pinned container image, else skipped with a note) on these scripts
#   4. ci-dry-test.sh and mac-dry-test.sh: every script here against fake
#      Apple and Mac tools
#   5. with CI_LOCAL_BASH32=1: both dry tests again under bash 3.2 — the
#      bash macOS runs them with — in the bash:3.2 container (docker or
#      podman; it installs python3, git and rsync, so it needs the network)
#   6. with swift on PATH: native/ios/UITests type-checked against stubs of
#      the XCUITest API (native/tools/uitest-stubcheck)
#   7. with XCODEGEN=/path/to/xcodegen (it builds on Linux: swift build in
#      a checkout of yonaskolb/XcodeGen): project.yml generates, with the
#      schemes CI runs (Xbin, XbinSnapshots, XbinUITests)
#
# Exit status: non-zero when any check fails.
set -euo pipefail

repo=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo"
wf=.github/workflows/ios.yml
ciwf=.github/workflows/ci.yml
scripts=native/ios/scripts
failed=""

step() { printf '\n== %s\n' "$*"; }
fail() {
  echo "FAIL: $*" >&2
  failed="$failed
  - $*"
}

# ---- 1. the workflow files -----------------------------------------------
step "workflows: parse and shape ($wf, $ciwf)"
if command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' 2>/dev/null; then
  python3 - "$wf" "$ciwf" "$scripts" <<'PY' || fail "workflow shape"
import json, os, re, sys, yaml

wf_path, ci_path, scripts = sys.argv[1], sys.argv[2], sys.argv[3]
errs = []
def check(cond, msg):
    if not cond:
        errs.append(msg)

# The current major of every action the iOS CI uses (gh api
# repos/<action>/releases/latest); a bump is fine, a step back is not.
MAJORS = {"actions/checkout": 7, "actions/upload-artifact": 7, "actions/cache": 6, "actions/setup-node": 7}
RUNNER = "${{ fromJSON(vars.XBIN_IOS_RUNNER || '\"xcode-27\"') }}"
SELF_HOSTED = '["self-hosted","macOS","xbin-mini"]'

def uses_major(step):
    m = re.match(r"^([^@]+)@v(\d+)$", str(step.get("uses", "")))
    return (m.group(1), int(m.group(2))) if m else (None, None)

def runs(step):
    return step.get("run") or ""

def eval_runner(expr, var):
    # The expression, evaluated as Actions does for this one shape:
    # fromJSON(vars.X || '<json>') — an unset variable is '' (falsy).
    m = re.match(r"^\$\{\{ fromJSON\(vars\.XBIN_IOS_RUNNER \|\| '(.*)'\) \}\}$", expr)
    if not m:
        return None
    return json.loads(var if var else m.group(1))

# ---------------------------------------------------------------- ios.yml
text = open(wf_path).read()
wf = yaml.safe_load(text)
# PyYAML reads the bare key `on` as the boolean True (YAML 1.1).
on = wf.get("on", wf.get(True)) or {}
check(set(on) <= {"push", "workflow_dispatch"}, "on: only push and workflow_dispatch (never pull_request*: a self-hosted runner must not run a fork's code) — has %s" % sorted(on))
push = on.get("push") or {}
check("master" in (push.get("branches-ignore") or []), "on.push.branches-ignore must list master (feature branches only)")
check("branches" not in push, "on.push must not also set branches")
check("tags" not in push, "on.push must not run on tags")
paths = push.get("paths") or []
for p in ("native/ios/**", "native/fixtures/**", "native/spec/**", ".github/workflows/ios.yml"):
    check(p in paths, "on.push.paths must include %s" % p)
check("workflow_dispatch" in on, "on must include workflow_dispatch")

conc = wf.get("concurrency") or {}
check("github.ref" in str(conc.get("group", "")), "concurrency.group must be per ref")
check(conc.get("cancel-in-progress") is True, "concurrency.cancel-in-progress must be true")
check((wf.get("permissions") or {}) == {"contents": "read"}, "permissions must be exactly contents: read")
check("secrets." not in text, "the workflow must not use secrets (no signing, no provisioning, nothing a self-hosted runner could leak)")
env = wf.get("env") or {}
check("vars.XBIN_CI_CACHE" in str(env.get("XBIN_CI_CACHE", "")), "env.XBIN_CI_CACHE comes from the repository variable (off = no cache)")

# The runner label: the expression, and what it yields.
check(eval_runner(RUNNER, "") == "xcode-27", "the runner expression's fallback must be \"xcode-27\"")
check(eval_runner(RUNNER, SELF_HOSTED) == ["self-hosted", "macOS", "xbin-mini"], "the self-hosted value must parse to its three labels")

jobs = wf.get("jobs") or {}
for name in ("packages", "app", "snapshots"):
    check(name in jobs, "job %s missing" % name)
# What each job's scripts build into (their -derivedDataPath
# "$XBIN_CI_DERIVED/<sub>") must be what its ci-cache.sh step caches.
def derived_subdirs(script):
    try:
        with open(script) as f:
            src = f.read()
    except OSError:
        return set()
    return set(re.findall(r'-derivedDataPath "\$XBIN_CI_DERIVED/([A-Za-z0-9_-]+)"', src))

for name, job in jobs.items():
    check(job.get("runs-on") == RUNNER, "job %s: runs-on must be %s" % (name, RUNNER))
    check(isinstance(job.get("timeout-minutes"), int), "job %s: needs timeout-minutes" % name)
    check("needs" not in job, "job %s: jobs stay independent (a failing app build must not stop the snapshots)" % name)
    steps = job.get("steps") or []
    check(bool(steps) and str(steps[0].get("uses", "")).startswith("actions/checkout@"), "job %s: first step checks out" % name)
    built, cache_step, cache_idx, restore_idx, xcodegen_idx = set(), None, None, None, None
    for i, st in enumerate(steps):
        action, major = uses_major(st)
        if st.get("uses"):
            check(action in MAJORS, "job %s: %s is not an action the iOS CI knows (add it to MAJORS)" % (name, st.get("uses")))
            if action in MAJORS:
                check(major >= MAJORS[action], "job %s: %s — the current major is v%d" % (name, st.get("uses"), MAJORS[action]))
        run = runs(st)
        check("brew install" not in run, "job %s: tools come from ci-xcodegen.sh, not an inline brew install" % name)
        for ref in re.findall(r"native/ios/scripts/[A-Za-z0-9_.-]+\.sh", run):
            check(os.path.isfile(ref), "job %s runs %s, which does not exist" % (name, ref))
            check(os.access(ref, os.X_OK), "job %s runs %s, which is not executable" % (name, ref))
            if ref.endswith("/ci-cache.sh"):
                cache_step, cache_idx = st, i
            elif ref.endswith("/ci-xcodegen.sh"):
                xcodegen_idx = i if xcodegen_idx is None else xcodegen_idx
            else:
                subs = derived_subdirs(ref)
                built |= subs
                if subs:
                    check(cache_idx is not None and cache_idx < i, "job %s: ci-cache.sh must run before %s" % (name, ref))
                if ref.endswith(("/ci-build-app.sh", "/ci-hosted-snapshots.sh", "/ci-uitests.sh")):
                    check(xcodegen_idx is not None and xcodegen_idx < i, "job %s: ci-xcodegen.sh must run before %s" % (name, ref))
        if action == "actions/cache":
            restore_idx = i
            w = st.get("with") or {}
            check(st.get("if") == "steps.cache.outputs.mode == 'actions'", "job %s: actions/cache only in actions mode (a self-hosted runner keeps its cache on disk)" % name)
            check(w.get("path") == "${{ steps.cache.outputs.paths }}" and w.get("key") == "${{ steps.cache.outputs.key }}"
                  and w.get("restore-keys") == "${{ steps.cache.outputs.restore }}", "job %s: actions/cache takes path/key/restore-keys from ci-cache.sh" % name)
    if built:
        check(cache_step is not None and cache_step.get("id") == "cache", "job %s: a ci-cache.sh step with id: cache" % name)
        check(restore_idx is not None and cache_idx is not None and cache_idx < restore_idx, "job %s: actions/cache right after ci-cache.sh" % name)
        if cache_step is not None:
            m = re.search(r"ci-cache\.sh\s+(\S+)((?:\s+[A-Za-z0-9_-]+)+)", runs(cache_step))
            args = set(m.group(2).split()) if m else set()
            check(m is not None and m.group(1) == name, "job %s: ci-cache.sh's first argument is the job name" % name)
            check(args == built, "job %s: ci-cache.sh caches %s but the job builds into %s" % (name, sorted(args), sorted(built)))

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
    check("${{ runner.temp }}/xbin-ci/uitests-build.log" in p, "xcresult-app must upload $RUNNER_TEMP/xbin-ci/uitests-build.log")
if "xcresult-snapshots" in names:
    p = names["xcresult-snapshots"][2]
    check("${{ runner.temp }}/xbin-ci/snapshots-test.xcresult" in p, "xcresult-snapshots must upload $RUNNER_TEMP/xbin-ci/snapshots-test.xcresult")
snapdir = None
for st in (jobs.get("snapshots") or {}).get("steps", []):
    run = runs(st)
    if "scripts/ci-snapshots.sh" in run:
        snapdir = (st.get("env") or {}).get("TEST_RUNNER_SNAPSHOT_DIR")
    if "scripts/ci-hosted-snapshots.sh" in run:
        hosted = (st.get("env") or {}).get("TEST_RUNNER_SNAPSHOT_DIR")
        check(hosted == "${{ runner.temp }}/snapshots/hosted", "the hosted snapshots step sets TEST_RUNNER_SNAPSHOT_DIR=${{ runner.temp }}/snapshots/hosted (inside the uploaded snapshots dir)")
check(snapdir == "${{ runner.temp }}/snapshots", "the snapshots step sets TEST_RUNNER_SNAPSHOT_DIR=${{ runner.temp }}/snapshots")
if "snapshots" in names and snapdir:
    check(names["snapshots"][2].startswith(snapdir + "/"), "the snapshots artifact uploads from TEST_RUNNER_SNAPSHOT_DIR")
app_runs = "\n".join(runs(s) for s in (jobs.get("app") or {}).get("steps", []))
check("scripts/ci-uitests.sh" in app_runs, "the app job builds the UI tests (ci-uitests.sh)")
check("XBIN_E2E" not in text, "ios.yml never runs the UI tests against an xbind (no XBIN_E2E_*: that is mac-remote.sh e2e)")

# The documented self-hosted value is the one evaluated above.
with open("native/AGENTS.md") as f:
    agents = f.read()
check(SELF_HOSTED in agents, "native/AGENTS.md must give the self-hosted XBIN_IOS_RUNNER value %s" % SELF_HOSTED)

# ----------------------------------------------------------------- ci.yml
with open(ci_path) as f:
    ctext = f.read()
ci = yaml.safe_load(ctext)
cjobs = ci.get("jobs") or {}
check("test" in cjobs, "ci.yml: the test job stays")
check("XBIN_IOS_RUNNER" not in ctext and "self-hosted" not in json.dumps([j.get("runs-on") for j in cjobs.values()]),
      "ci.yml runs for pull requests: none of its jobs may run on a self-hosted runner")
nat = cjobs.get("native") or {}
check(str(nat.get("runs-on", "")).startswith("ubuntu-"), "ci.yml: the native job runs on a GitHub-hosted Ubuntu runner")
nruns = "\n".join(runs(s) for s in nat.get("steps", []))
for want in ("native/ios/scripts/ci-linux-swift.sh", "make swift-test", "make native-check", "native/ios/scripts/ci-local-check.sh"):
    check(want in nruns, "ci.yml native job runs %s" % want)
for st in nat.get("steps", []):
    action, major = uses_major(st)
    if action in MAJORS:
        check(major >= MAJORS[action], "ci.yml native job: %s — the current major is v%d" % (st.get("uses"), MAJORS[action]))
    if action == "actions/cache":
        check("ci-linux-swift.sh" in str((st.get("with") or {}).get("key", "")), "ci.yml native job: the swiftly cache is keyed on ci-linux-swift.sh (its pins)")

if errs:
    for e in errs:
        print("  - " + e, file=sys.stderr)
    sys.exit(1)
print("ok: ios.yml %d jobs, %d artifacts (%s); runner xcode-27 | self-hosted; ci.yml %d jobs" % (
    len(jobs), len(names), ", ".join(sorted(n for n in names if n)), len(cjobs)))
PY
elif command -v ruby >/dev/null 2>&1; then
  echo "(no PyYAML: parse only, with ruby)"
  for f in "$wf" "$ciwf"; do
    ruby -ryaml -e 'YAML.safe_load(File.read(ARGV[0])); puts "ok: #{ARGV[0]} parses"' "$f" || fail "workflow YAML ($f)"
  done
else
  fail "cannot parse YAML: need python3 with PyYAML (python3-yaml) or ruby"
fi

# ---- 2. actionlint -------------------------------------------------------
step "actionlint"
AL_VERSION=1.7.12
AL_SHA256_amd64=8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8
AL_SHA256_arm64=325e971b6ba9bfa504672e29be93c24981eeb1c07576d730e9f7c8805afff0c6
al=${ACTIONLINT:-$(command -v actionlint 2>/dev/null || true)}
altmp=""
if [ -z "$al" ] && [ "${CI_LOCAL_FETCH_ACTIONLINT:-0}" = 1 ]; then
  alarch="" alsum=""
  case $(uname -m) in
  x86_64) alarch=amd64 alsum=$AL_SHA256_amd64 ;;
  aarch64 | arm64) alarch=arm64 alsum=$AL_SHA256_arm64 ;;
  esac
  if [ "$(uname -s)" = Linux ] && [ -n "$alarch" ]; then
    altmp=$(mktemp -d "${TMPDIR:-/tmp}/actionlint.XXXXXX")
    if curl -fsSL --retry 3 -o "$altmp/al.tgz" "https://github.com/rhysd/actionlint/releases/download/v$AL_VERSION/actionlint_${AL_VERSION}_linux_$alarch.tar.gz" &&
      [ "$(sha256sum "$altmp/al.tgz" | cut -d' ' -f1)" = "$alsum" ] &&
      tar -xzf "$altmp/al.tgz" -C "$altmp" actionlint; then
      al=$altmp/actionlint
    else
      fail "could not fetch actionlint $AL_VERSION (download or SHA-256)"
    fi
  fi
fi
if [ -n "$al" ] && [ -x "$al" ]; then
  cfg=$(mktemp "${TMPDIR:-/tmp}/actionlint.XXXXXX")
  # xcode-27 is a GitHub-hosted larger runner's label, xbin-mini the Mac
  # mini's; actionlint knows neither.
  printf 'self-hosted-runner:\n  labels: [xcode-27, xbin-mini]\n' >"$cfg"
  if "$al" -config-file "$cfg" "$wf" "$ciwf"; then echo "ok ($("$al" --version | head -n 1))"; else fail "actionlint $wf $ciwf"; fi
  rm -f "$cfg"
else
  echo "skipped: no actionlint (go install github.com/rhysd/actionlint/cmd/actionlint@latest, ACTIONLINT=…, or CI_LOCAL_FETCH_ACTIONLINT=1)"
fi
[ -z "$altmp" ] || rm -rf "$altmp"

# ---- 3. shell ------------------------------------------------------------
step "bash -n"
for f in "$scripts"/*.sh; do
  bash -n "$f" || fail "bash -n $f"
done
echo "ok"
step "shellcheck"
./hack/check-sh.sh "$scripts"/*.sh || fail "shellcheck $scripts"

# ---- 4. behaviour --------------------------------------------------------
for t in ci-dry-test.sh mac-dry-test.sh; do
  [ -f "$scripts/$t" ] || continue
  step "$t"
  "$scripts/$t" || fail "$t"
done

if [ "${CI_LOCAL_BASH32:-0}" = 1 ]; then
  step "the dry tests under bash 3.2"
  engine=""
  for e in podman docker; do
    if command -v "$e" >/dev/null 2>&1; then engine=$e; break; fi
  done
  if [ -z "$engine" ]; then
    fail "CI_LOCAL_BASH32=1 needs docker or podman"
  else
    "$engine" run --rm -v "$repo:/w:ro" -w /w docker.io/library/bash:3.2 \
      sh -c 'apk add --no-cache python3 git rsync >/dev/null && bash native/ios/scripts/ci-dry-test.sh && bash native/ios/scripts/mac-dry-test.sh' ||
      fail "the dry tests under bash 3.2"
  fi
fi

step "UI tests against the XCUITest stubs"
if command -v swift >/dev/null 2>&1; then
  native/tools/uitest-stubcheck/run.sh || fail "native/tools/uitest-stubcheck"
else
  echo "skipped: no swift on PATH (native/AGENTS.md §3)"
fi

step "xcodegen generate (project.yml)"
if [ -n "${XCODEGEN:-}" ]; then
  gen=$(mktemp -d "${TMPDIR:-/tmp}/xcodegen-check.XXXXXX")
  # native/ios without SwiftPM's build trees: xcodegen only reads the sources.
  (cd native/ios && tar --exclude=.build --exclude='*.xcodeproj' -cf - .) | (cd "$gen" && tar -xf -)
  if (cd "$gen" && "$XCODEGEN" generate --spec project.yml >/dev/null); then
    for scheme in Xbin XbinSnapshots XbinUITests; do
      [ -f "$gen/Xbin.xcodeproj/xcshareddata/xcschemes/$scheme.xcscheme" ] || fail "project.yml: no shared scheme $scheme"
    done
    echo "ok: $(ls "$gen/Xbin.xcodeproj/xcshareddata/xcschemes" | tr '\n' ' ')"
  else
    fail "xcodegen generate --spec native/ios/project.yml"
  fi
  rm -rf "$gen"
else
  echo "skipped: XCODEGEN=/path/to/xcodegen to generate the project here"
fi

if [ -n "$failed" ]; then
  printf '\nci-local-check: FAILED%s\n' "$failed" >&2
  exit 1
fi
printf '\nci-local-check: ok\n'
