#!/bin/sh
# hack/agent-smoke.sh — do the agent CLIs in the base rootfs speak ACP?
# Runs each one inside the built image (docker), feeds it an `initialize`
# and expects `"protocolVersion":1` back on stdout — the stage-0 go/no-go
# for agent sessions (D74). No keys needed: initialize precedes auth.
#
#   hack/agent-smoke.sh [image]        # default: $XBIN_ROOTFS_TAG or xbin-rootfs:dev
set -eu
TAG="${1:-${XBIN_ROOTFS_TAG:-xbin-rootfs:dev}}"
DOCKER="${DOCKER:-docker}"
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true},"clientInfo":{"name":"xbin-smoke","version":"0"}}}'
fails=0
for cmd in "opencode acp" "claude-agent-acp" "codex-acp" "gemini --acp"; do
  name=${cmd%% *}
  # a login-less HOME, IS_SANDBOX as the terminal sandbox sets it, 20 s to
  # answer; stdin stays open after the frame (codex-acp exits on EOF before
  # it flushes, the way xbind's pipe never closes early)
  if out=$("$DOCKER" run --rm -i -e HOME=/tmp/h -e IS_SANDBOX=1 "$TAG" sh -c "mkdir -p /tmp/h; (printf '%s\n' '$INIT'; sleep 20) | timeout 20 $cmd 2>/dev/null | head -1"); then
    case "$out" in
      *'"protocolVersion":1'*) echo "  ✓ $name";;
      *) echo "  ✗ $name: unexpected answer: ${out:-<nothing>}"; fails=$((fails + 1));;
    esac
  else
    echo "  ✗ $name: did not run"; fails=$((fails + 1))
  fi
done
[ "$fails" -eq 0 ] && echo "agent-smoke: 4 ACP agents answer initialize" || { echo "agent-smoke: $fails failed"; exit 1; }
