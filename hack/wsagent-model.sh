#!/usr/bin/env bash
# hack/wsagent-model.sh — regenerate the workspace agent's short map of xbin
# (builtin-templates/agent/_backend/ws_model.md) from the docs. Run it after
# changing a passage the map quotes (hack/wsagent-model/main.go lists them);
# `make test` fails until the file is what the docs give.
#
#   hack/wsagent-model.sh           # write the file
#   hack/wsagent-model.sh --check   # only compare
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "${1:-}" = "--check" ]; then
  exec go run ./hack/wsagent-model -check
fi
exec go run ./hack/wsagent-model
