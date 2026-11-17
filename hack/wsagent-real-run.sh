#!/usr/bin/env bash
# hack/wsagent-real-run.sh — the workspace agent against a REAL model
# (plans/wsagent/real-model-run-2026-11-17.md): brings up a throwaway,
# isolated xbind whose LLM connector's upstream is the endpoint you name,
# runs the scenario list (builtin-templates/agent/_backend/testdata/
# ws-real-model-scenarios.md) and, with --bench N, the first-run journey N
# times (one prompt → the plan → Apply → a real coding agent builds the
# tool → the assistant rolls), and writes the graded table and the
# transcripts.
#
#   hack/wsagent-real-run.sh <base-url> <model> <key-file> [--out DIR]
#                            [--scenarios 4,5,7 | --no-scenarios] [--bench N]
#                            [--port P] [--quota TOKENS]
#
#   <base-url>  the upstream's OpenAI-compatible base URL (https://…/v1);
#               chat completions is enough — a Responses call for an
#               upstream without that API is translated (D244)
#   <model>     the model's id AT THE UPSTREAM, slash and all
#               ("deepseek/deepseek-v4.1-flash")
#   <key-file>  a file holding the key, one line, mode 600. The key is never
#               an argument and never in the environment: xbind reads the
#               file (the host file's keyFile); nothing this script writes
#               holds it, which it checks at the end.
#   --out       where the table (table.md, rows.jsonl), transcripts/ and
#               bench/run<N>/ go (default $TMPDIR/xbin-wsagent-real)
#   --port      the first of six ports on 127.0.0.1 (default 19530): the
#               scenarios' xbind on P, the journey's on P+13 and P+14
#   --quota     tokens per person and day in the connector (default
#               3000000): a loop can't run away with the endpoint's quota
#
# The grades are mechanical (which tools the model called, what reached
# xbind, a few words of its answer): read the transcript before quoting
# one. Only synthetic data is used, and the only remote host contacted is
# the endpoint's. Needs what `make integration` needs (user namespaces, the
# rootfs, gocryptfs, fuse-overlayfs: hack/dev-setup.sh --check), and for
# --bench a Playwright checkout (PLAYWRIGHT_DIR). Run it with the tool
# sandbox off. Costs: a full scenario run was ~6 M input tokens (mostly
# cached prompt) and ~150 k output; a journey ~1 M and ~20 k.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
usage() { sed -n '2,36p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-2}"; }
[[ $# -ge 3 ]] || usage
base=$1 model=$2 keyfile=$3
shift 3
out=${TMPDIR:-/tmp}/xbin-wsagent-real scenarios="" bench=0 port=19530 quota=3000000 run_scenarios=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) out=$2; shift 2 ;;
    --scenarios) scenarios=$2; shift 2 ;;
    --no-scenarios) run_scenarios=0; shift ;;
    --bench) bench=$2; shift 2 ;;
    --port) port=$2; shift 2 ;;
    --quota) quota=$2; shift 2 ;;
    -h | --help) usage 0 ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done
[[ -r "$keyfile" ]] || { echo "the key file $keyfile can't be read" >&2; exit 1; }
[[ "$base" == http* ]] || { echo "the base URL must be http(s)://…" >&2; exit 1; }
keyfile=$(cd "$(dirname "$keyfile")" && pwd)/$(basename "$keyfile")
mkdir -p "$out/transcripts" "$out/bench"
cd "$repo"

rc=0
if [[ $run_scenarios == 1 ]]; then
  echo "scenarios → $out/table.md (transcripts in $out/transcripts)"
  # the scenarios that restart xbind or apply plans boot workspaces of their own (-run matches them all)
  XBIN_REAL_LLM_BASE=$base XBIN_REAL_LLM_MODEL=$model XBIN_REAL_LLM_KEYFILE=$keyfile XBIN_REAL_OUT=$out \
    XBIN_REAL_ADDR=127.0.0.1:$port XBIN_REAL_QUOTA=$quota XBIN_REAL_SCENARIOS=$scenarios \
    go test -tags integration -count=1 -p 1 -timeout 180m -run '^TestWsRealModel' ./test/isolated/ > "$out/scenarios.log" 2>&1 || rc=$?
  grep -E 'scenario [0-9]+[ab]?:|^(ok|FAIL|---)' "$out/scenarios.log" | sed -E 's/^ +[a-z_]+\.go:[0-9]+: //' || true
  # scenarios 29 and 30, a web page that tries to direct the agent (30: by forging xbin's own reminder): xbind
  # reads public addresses only, so the page is the agent backend's fake and the model is the real one
  # (builtin-templates/agent/_backend/ws_real_test.go)
  pages=()
  [[ -z "$scenarios" || ",$scenarios," == *,29,* ]] && pages+=(TestWsRealWebPageInjection)
  [[ -z "$scenarios" || ",$scenarios," == *,30,* ]] && pages+=(TestWsRealForgedReminder)
  if (( ${#pages[@]} )); then
    XBIN_REAL_LLM_BASE=$base XBIN_REAL_LLM_MODEL=$model XBIN_REAL_LLM_KEYFILE=$keyfile XBIN_REAL_OUT=$out \
      TILE_TEST_FLAGS="-run ^($(IFS='|'; echo "${pages[*]}"))\$ -count=1 -v" ./hack/tile-check.sh agent > "$out/scenario-pages.log" 2>&1 || rc=$?
    grep -E '^(---|ok|FAIL)' "$out/scenario-pages.log" || true
  fi
  # the table again, with that row (the scenarios' own boot wrote it without)
  XBIN_REAL_LLM_BASE=$base XBIN_REAL_LLM_MODEL=$model XBIN_REAL_LLM_KEYFILE=$keyfile XBIN_REAL_OUT=$out \
    go test -tags integration -count=1 -run '^TestWsRealModelTable$' ./test/isolated/ > /dev/null 2>&1 || true
  echo "table: $out/table.md"
  [[ -f "$out/table.md" ]] && cat "$out/table.md"
fi

if [[ $bench -gt 0 ]]; then
  : "${PLAYWRIGHT_DIR:?--bench drives a browser: set PLAYWRIGHT_DIR (hack/dev-setup.sh)}"
  CGO_ENABLED=0 go build -o bin/xbind ./cmd/xbind
  CGO_ENABLED=0 go build -o bin/bx ./cmd/bx
  go build -o bin/fakeacp ./hack/fakeacp && go build -o bin/fakeopenai ./hack/fakeopenai
  env -u GOOS -u GOARCH CGO_ENABLED=0 go run ./cmd/xbind sealed-build --out bin/sealed --sdk sdk --os "" --arch "" > /dev/null
  hd=$out/harness
  for n in $(seq 1 "$bench"); do
    echo "journey $n of $bench → $out/bench/run$n"
    PORT=$((port + 10)) FAKEOPENAI_ADDR=127.0.0.1:$((port + 15)) HARNESS_DIR=$hd HARNESS_ISOLATE=1 \
      JOURNEY_REAL_BASE=$base JOURNEY_REAL_MODEL=$model JOURNEY_REAL_KEYFILE=$keyfile \
      ./hack/ui-harness/run.sh --shots journeyReal > "$out/bench/run$n.log" 2>&1 || rc=$?
    mkdir -p "$out/bench/run$n"
    cp -f "$hd"/out/journey-real* "$hd/out/shots.log" "$out/bench/run$n/" 2> /dev/null || true
    cp -rf "$hd/out/journey-shots" "$out/bench/run$n/" 2> /dev/null || true
    # the journey's workspace is not kept (and its host file names the key's path, not the key)
    ws=$hd/journey-real/ws
    if [[ -d "$ws" && "$ws" == "$out"/harness/journey-real/ws ]]; then
      { grep -F " $ws/" /proc/self/mounts || true; } | cut -d' ' -f2 | while read -r m; do fusermount3 -uz "$m" 2> /dev/null || true; done
      rm -rf -- "$ws"
    fi
    [[ -f "$out/bench/run$n/journey-real-summary.json" ]] && cat "$out/bench/run$n/journey-real-summary.json"
  done
fi

# nothing kept holds the key
if grep -rlF -- "$(head -c 12 "$keyfile")" "$out" > /dev/null 2>&1; then
  echo "WARNING: a file under $out holds the start of the key — find it (grep -rlF) and remove it before sharing anything" >&2
  rc=1
fi
echo "done: $out (exit $rc)"
exit $rc
