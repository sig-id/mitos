# Mitos Studio — Go + HTMX. No frontend build step.
default: dev

dev: fmt check build

fmt:
    gofmt -w cmd internal

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
