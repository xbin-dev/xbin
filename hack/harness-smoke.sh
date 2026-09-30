#!/usr/bin/env bash
# hack/harness-smoke.sh — do the agent template's coding agents work?
# (D-harness; builtin-templates/agent/API.md §Coding agents "Testing coding
# agents").
#
# Always (about a minute, no sandbox, no network after the first run): the
# ACP client and the scripted fake adapter (sdk/acp, sdk/acp/acptest —
# hack/fakeacp's engine), fakesandbox's stdio and terminal relays, and the
# agent template's harness engine, pipe, catalog and relays against
# fakesandbox (fsb) with acptest as the adapter — the tile's backend built
# as a workspace builds it (hack/tile-check.sh).
#
# HARNESS_SMOKE_LIVE=1 then runs the live check, test/isolated
# TestHarnessLive: an isolated xbind with owner auth, coding-sandbox on its
# runtime, the agent template bound to it, and the rootfs's real adapters
# (claude-agent-acp, codex-acp, gemini --acp, opencode acp) each through
# initialize and session/new to an answer or its sign-in, plus the fake
# adapter for a whole turn (terminal sign-in through the agent's relay, a
# permission, the stdio pipe, a mid-turn redeploy). It logs what each real
# adapter did ("adapter <id>: …"). HARNESS_SMOKE_VM=1 adds VM sandboxes
# (TestHarnessLiveVM; KVM, or XBIN_VM_ACCEL=emulate). It needs user
# namespaces, the base rootfs (make rootfs, or XBIN_ROOTFS), bin/'s helpers
# (fuse-overlayfs; for VMs make vm-assets) and, on a dev box, the Bash tool's
# sandbox off. HARNESS_LIVE_ONLY=claude,codex narrows the real adapters.
#
#   hack/harness-smoke.sh
#   HARNESS_SMOKE_LIVE=1 hack/harness-smoke.sh
#   HARNESS_SMOKE_LIVE=1 HARNESS_SMOKE_VM=1 hack/harness-smoke.sh
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"

step() { printf '\n== harness-smoke: %s\n' "$*"; }

step "sdk/acp and acptest (the client, the fake adapter)"
(cd sdk && go test -count=1 ./acp/...)

step "fakesandbox (fsb): stdio sockets, consumer terminals"
go test -count=1 ./hack/fakesandbox/

step "the agent template's engine, pipe, catalog and relays on fsb + acptest"
TILE_TEST_FLAGS="-count=1 -run ${HARNESS_SMOKE_RUN:-Harness|Terminal}" ./hack/tile-check.sh agent

if [ "${HARNESS_SMOKE_LIVE:-}" != 1 ]; then
  printf '\nharness-smoke: ok (HARNESS_SMOKE_LIVE=1 also drives the real adapters live)\n'
  exit 0
fi

tests='^TestHarnessLive$'
[ "${HARNESS_SMOKE_VM:-}" = 1 ] && tests='^TestHarnessLive(VM)?$'
step "live: $tests (test/isolated, an isolated xbind with coding-sandbox and the agent)"
log=$(mktemp)
trap 'rm -f "$log"' EXIT
set +e
go test -tags=integration -count=1 -v -timeout 60m -run "$tests" ./test/isolated/ 2>&1 | tee "$log"
rc=${PIPESTATUS[0]}
set -e
step "what the real adapters did"
grep -E 'adapter [a-z]+( through the agent)?: ' "$log" | sed 's/^ *//' || echo "(none ran)"
[ "$rc" -eq 0 ] && printf '\nharness-smoke: ok, live included\n' || { printf '\nharness-smoke: the live check failed (rc %s)\n' "$rc"; exit 1; }
