# mitos — dev commands (`just` with no args runs `dev`).
# All mix/escript recipes operate in beam/; the harness binary builds there too.

default: dev

# Bootstrap the dev environment: fetch deps, compile, test, build the escript.
# Idempotent — run it after `git pull` or whenever you want a green baseline.
dev:
    cd beam && \
    printf ":: deps\n"   && mix deps.get && \
    printf ":: compile\n" && mix compile && \
    printf ":: test\n"    && mix test && \
    printf ":: escript\n" && mix escript.build && \
    printf "\n✓ dev ready — ./mitos (try: just serve)\n"

# Strict CI-style check: warnings-as-errors compile + full suite.
check:
    cd beam && mix compile --warnings-as-errors && mix test

# Compile the Elixir app (warnings-as-errors).
compile:
    cd beam && mix compile --warnings-as-errors

# Run the test suite (pass extra args: `just test test/mitos/run_test.exs`).
test *args:
    cd beam && mix test {{args}}

# Build the ./mitos escript.
build:
    cd beam && mix escript.build

# Serve a mitos app on the control plane (default: the security-audit demo).
# `--dev` lets pi read the operator's ~/.pi so runs boot locally (T1 dev;
# production uses the broker/MITM edge). Override ports to run alongside another
# instance: `just serve 4756 4757`.
serve http_port="4646" broker_port="4647" app="security-audit": build
    cd beam && ./mitos serve --dev --app apps/{{app}} --http-port {{http_port}} --broker-port {{broker_port}}

# Clean build artifacts.
clean:
    cd beam && mix clean
