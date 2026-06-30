#!/usr/bin/env bash
# autofix-test.sh — backend-only API test harness for the autofix app.
#
# Server: MITOS_HTTP_PORT (default 7682). Start with `just serve` or `just dev`.
#
# The run endpoint is SYNCHRONOUS — on long agent runs the HTTP connection may
# drop, but the run keeps going in the background (durable event log). So we use
# fire-and-poll: note the newest run dir, fire the POST, then poll the new run
# until its event log shows completion, then print the RESULT artifact.
#
# Usage:
#   ./autofix-test.sh check                 # instant: load + schema validation (NO LLM)
#   ./autofix-test.sh review [diff_cmd]     # real code-review (opencode, ~1-3 min, costs tokens)
#   ./autofix-test.sh scan-health           # codebase health scan (opencode)
#   ./autofix-test.sh poll <app_run_id>     # re-poll an in-flight run
#   ./autofix-test.sh result [wl_id]        # cat the newest RESULT artifact

set -euo pipefail
PORT="${MITOS_HTTP_PORT:-7682}"
BASE="http://127.0.0.1:${PORT}"
RUNS="beam/.runs"

require() { command -v "$1" >/dev/null || { echo "missing: $1"; exit 1; }; }
require curl; require jq

# ─── check: instant validation, no LLM ──────────────────────────────────────────
do_check() {
  echo "▶ health"
  curl -sf "${BASE}/healthz" >/dev/null && echo "  ok" || { echo "  server down — start with: just serve"; exit 1; }

  echo "▶ GET /apps"
  curl -sf "${BASE}/apps" | jq -r '.apps[] | "  - \(.name // .id)"'

  echo "▶ GET /apps/autofix  (manifest summary)"
  curl -sf "${BASE}/apps/autofix" | jq -r '
    .app | "  app_id:     \(.app_id)",
           "  capabilities: \(.capabilities|length)",
           "  pipelines:    \(.pipelines|length)",
           "  entrypoints:  \([.entrypoints[].id] | join(", "))"'

  echo "▶ schema validation (POST review with no diff_command -> must 422)"
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    "${BASE}/apps/autofix/entrypoints/review/runs" -H 'content-type: application/json' -d '{}')
  [ "$code" = "422" ] && echo "  HTTP 422 ✓ (inputs.schema enforced)" \
                       || echo "  HTTP ${code} ✗ (expected 422)"

  echo "✓ check passed"
}

# newest run-id helper. NOTE: `ls -t GLOB` lists a matched dir's CONTENTS,
# not the dir itself — use `ls -dt` so we get the dir paths.
newest_app_id() { ls -dt ${RUNS}/app-*/ 2>/dev/null | head -1 | xargs -r basename; }

# ─── fire-and-poll ──────────────────────────────────────────────────────────────
# The POST is synchronous and the HTTP connection often drops on long agent runs,
# but the run is durable: it keeps executing in the background and the capability
# writes a RESULT artifact to .runs/wl-*/runtime/workspace/RESULT. So we fire the
# POST, detect the new app-* run, then poll for the RESULT artifact (the reliable
# completion signal — the app-run status flips to "interrupted" when HTTP drops,
# which is NOT the run's real state).
fire_and_poll() {
  local entry="$1" body="$2" label="$3"
  local before; before=$(newest_app_id)

  echo "▶ POST /apps/autofix/entrypoints/${entry}/runs  (${label})"
  ( curl -s --max-time 900 -X POST "${BASE}/apps/autofix/entrypoints/${entry}/runs" \
      -H 'content-type: application/json' -d "$body" >/tmp/autofix-resp.json 2>/dev/null || true ) &

  # wait for the new app-* run dir to appear
  local app_id=""
  for _ in $(seq 1 30); do
    sleep 1
    app_id=$(newest_app_id)
    [ -n "$app_id" ] && [ "$app_id" != "$before" ] && break
    app_id=""
  done
  [ -n "$app_id" ] || { echo "  no new run dir appeared"; exit 1; }
  echo "  app_run_id: ${app_id}"
  poll "$app_id"
}

# Poll until the harness writes RESULT (reliable) or the app-run finishes cleanly.
# We do NOT treat "interrupted" as terminal — it just means the HTTP caller went
# away; the underlying opencode subprocess keeps running.
poll() {
  local app_id="$1"
  echo "▶ polling for RESULT artifact (opencode runs in background; this takes 1-5 min)..."
  local result=""
  for _ in $(seq 1 60); do   # 60 x 10s = 10 min max
    result=$(find ${RUNS} -path "*/runtime/workspace/RESULT" -newermt "-10 minutes" 2>/dev/null \
              | xargs -r ls -t 2>/dev/null | head -1)
    [ -n "$result" ] && [ -f "$result" ] && break
    # fallback: a clean terminal status also counts
    local s; s=$(curl -sf "${BASE}/runs/${app_id}" 2>/dev/null | jq -r '.status' 2>/dev/null || echo "?")
    case "$s" in done|failed|cancelled) break ;; esac
    printf '.'; sleep 10
  done
  echo

  echo "▶ app-run trace:"
  curl -sf "${BASE}/runs/${app_id}" 2>/dev/null | jq -r \
    '.events[] | select(.type|test("task_|capability_")) | "  \(.type)  \(.payload.summary // .payload.status // "")"' 2>/dev/null | tail -8

  do_result
}

# Print the newest RESULT artifact (what the capability wrote to its workspace).
do_result() {
  local f
  f=$(find ${RUNS} -path "*runtime/workspace/RESULT" 2>/dev/null | xargs -r ls -t 2>/dev/null | head -1)
  if [ -n "$f" ] && [ -f "$f" ]; then
    echo "▶ RESULT artifact: ${f}"
    jq '.' "$f" 2>/dev/null || cat "$f"
  else
    echo "  (no RESULT artifact found — run may still be harvesting, retry: $0 result)"
  fi
}

# ─── entrypoints ────────────────────────────────────────────────────────────────
do_review() {
  local diff="${1:-git -c color.ui=never log --oneline -5}"
  echo ">>> REVIEW  diff='${diff}'"
  fire_and_poll review \
    "{\"inputs\":{\"diff_command\":\"${diff}\"},\"parameters\":{\"severity_floor\":\"MAJOR\"}}" \
    "code-review"
}

do_scan() {
  echo ">>> SCAN-HEALTH"
  fire_and_poll scan-health '{"inputs":{}}' "meta-codebase-health"
}

case "${1:-check}" in
  check)    do_check ;;
  review)   do_review "${2:-}" ;;
  scan-health) do_scan ;;
  poll)     poll "${2:?usage: poll <app_run_id>}" ;;
  result)   do_result ;;
  *) echo "usage: $0 {check|review [diff_cmd]|scan-health|poll <id>|result}"; exit 2 ;;
esac
