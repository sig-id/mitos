# Mitos – Go + HTMX. No frontend build step.
default: dev

dev: fmt check build

# Format Go, normalize dashes, and canonicalize comment separators.
fmt:
    gofmt -w cmd internal
    just emrep
    just comment-clean

# Replace em dashes with en dashes in tracked source and documentation files.
emrep:
    #!/usr/bin/env bash
    set -euo pipefail
    git ls-files -z | while IFS= read -r -d '' path; do
        [[ -e "$path" ]] || continue
        case "$path" in
            internal/web/static/htmx.min.js|internal/web/static/xterm.js|internal/web/static/xterm-fit.js|internal/web/static/xterm.css)
                # Preserve vendored third-party assets.
                continue
                ;;
            *.c|*.cpp|*.h|*.hpp|*.cmake|*.mk|*.bzl|*.py|*.ipynb|*.js|*.jsx|*.ts|*.svelte|*.css|*.htm|*.html|*.htmx|*.xhtml|*.go|*.java|*.hs|*.fut|*.sol|*.move|*.mo|*.rs|*.zig|*.sh|*.tf|*.lua|*.yml|*.json|*.proto|*.gql|*.sql|*.txt|*.md|*.tla)
                perl -0pi -e 's/\xE2\x80\x94/\xE2\x80\x93/g' "$path"
                ;;
        esac
    done

# Normalize comment banners in tracked Go, Rust, and JavaScript/TypeScript sources.
comment-clean:
    #!/usr/bin/env bash
    set -euo pipefail
    mapfile -d '' -t files < <(
        git ls-files -z -- '*.go' '*.rs' '*.ts' '*.tsx' '*.js' '*.jsx' \
            ':!internal/web/static/htmx.min.js' ':!internal/web/static/xterm.js' ':!internal/web/static/xterm-fit.js' \
            | while IFS= read -r -d '' path; do
                [[ -f "$path" ]] && printf '%s\0' "$path"
            done \
            | xargs -0 -r rg -l -0 '^\s*//\s*(?:=+\s*$|[-=─]{2,}.*[-=─]{2,}\s*$)' -- || true
    )
    if ((${#files[@]})); then
        perl -0pi -e 's{^(\s*// )=+\h*\n\1([^\n]+?)\h*\n\1=+\h*$}{$1."======== ".$2." ========"}gme; s{^(\s*// )[-=─]{2,}\h*(.*?)\h*[-=─]{2,}\h*$}{$1."======== ".$2." ========"}gme' "${files[@]}"
    fi

check:
    go vet ./...
    go test -race ./...

test *args:
    go test {{args}} ./...

compile: build

build:
    go build -o bin/mitos ./cmd/mitos

serve port="4646": build
    ./bin/mitos serve --addr 127.0.0.1:{{port}} --apps ./examples

stop data=".mitos": build
    ./bin/mitos stop --data {{quote(data)}}

# Labelled example history, in its own data directory.
demo port="4646": build
    ./bin/mitos serve --addr 127.0.0.1:{{port}} --apps ./examples --data .mitos-demo --demo

clean:
    go clean ./...
