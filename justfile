# Mitos development workflows.
#
#   just dev          build engine + sidecar, start the control plane with IEx
#   just poc          build everything, serve the poc run viewer headless
#   just demo-agent   start a claude PTY agent (server must be running)
#   just steer …      steer a running session semantically over HTTP

port := env_var_or_default("MITOS_HTTP_PORT", "7682")

default:
    @just --list

# Build the Zig engine + mitos-term sidecar.
build:
    zig build

# Run both test suites (Zig + Elixir, integration included).
test: build
    zig build test --summary all
    cd beam && mix test --include integration

# Sessions, PubSub, and steering are reachable from the attached IEx shell:
# Mitos.PubSub.subscribe("agent-123"); flush()
#
# Build everything and start the BEAM control plane (HTTP + WS + IEx).
dev: build
    #!/usr/bin/env bash
    set -euo pipefail
    cd beam
    [ -d deps ] || mix deps.get
    echo "────────────────────────────────────────────────────────────────"
    echo " mitos dev — http://localhost:{{port}}"
    echo "   run viewer : http://localhost:{{port}}/   (runs, approvals, workloads; WS at /ws/<session-id>)"
    echo "   start agent: just demo-agent"
    echo "   steer      : just steer <session-id> approve"
    echo "                just steer <session-id> prompt 'do option 2 instead'"
    echo "   traces     : beam/.runs/<session-id>/events.jsonl"
    echo "────────────────────────────────────────────────────────────────"
    MITOS_HTTP_PORT={{port}} exec iex -S mix

# Build everything and serve the proof-of-concept run viewer (poc/) on the
# headless control plane.
poc: build
    #!/usr/bin/env bash
    set -euo pipefail
    cd beam
    [ -d deps ] || mix deps.get
    echo "────────────────────────────────────────────────────────────────"
    echo " mitos poc viewer — http://localhost:{{port}}/"
    echo "   runs / evidence : http://localhost:{{port}}/#/runs"
    echo "   launch a run    : http://localhost:{{port}}/#/new"
    echo "   workloads       : http://localhost:{{port}}/#/workloads"
    echo "────────────────────────────────────────────────────────────────"
    MITOS_HTTP_PORT={{port}} exec mix run --no-halt

# Headless variant of `dev` (no IEx shell), e.g. for running under a watcher.
serve: build
    #!/usr/bin/env bash
    set -euo pipefail
    cd beam
    [ -d deps ] || mix deps.get
    MITOS_HTTP_PORT={{port}} exec mix run --no-halt

# Defaults to a scratch cwd: session-log discovery keys on the agent's cwd,
# so demoing from a directory with other live claude sessions is ambiguous.
#
# Start a claude agent on the pty transport and print its session id.
demo-agent cwd="/tmp/mitos-demo" task="Look around this repo and propose one refactor. Ask me before making changes.":
    @mkdir -p {{cwd}}
    @curl -s -XPOST "localhost:{{port}}/agents" -H 'content-type: application/json' \
      -d '{"provider":"claude","task":"{{task}}","cwd":"{{cwd}}","transport_order":["pty"]}'
    @echo

#   just steer agent-123 approve            (also: deny, interrupt)
#   just steer agent-123 prompt "now run the tests"
#   just steer agent-123 answer "use port 8080"
#   just steer agent-123 select_option "" 2
#
# Steer a running session semantically.
steer session interaction text="" option="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -n "{{option}}" ]; then
        body='{"interaction":"{{interaction}}","option":{{option}}}'
    elif [ -n "{{text}}" ] && command -v jq >/dev/null; then
        body=$(jq -cn --arg i "{{interaction}}" --arg t "{{text}}" '{interaction: $i, text: $t}')
    elif [ -n "{{text}}" ]; then
        body='{"interaction":"{{interaction}}","text":"{{text}}"}'
    else
        body='{"interaction":"{{interaction}}"}'
    fi
    curl -s -XPOST "localhost:{{port}}/sessions/{{session}}/steer" \
        -H 'content-type: application/json' -d "$body"
    echo
