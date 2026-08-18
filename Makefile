VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GOVULNCHECK_VERSION ?= v1.7.0
GO_SECURITY_VERSION ?= go1.26.6
LDFLAGS := -s -w \
	-X github.com/qunqin24/polyglot/internal/version.Version=$(VERSION) \
	-X github.com/qunqin24/polyglot/internal/version.Commit=$(COMMIT)

.PHONY: all check build build-go web web-deps web-check web-dev test test-race compatibility-test lint vulncheck run dev docker clean catalog

all: build

## web: check and build the React bundle that go:embed picks up.
## `vite build` does not typecheck, so the gates run explicitly here.
web: web-deps web-check
	pnpm --dir web run build

web-deps:
	pnpm --dir web install --frozen-lockfile

## web-check: frontend regression tests + TypeScript strict mode + ESLint
web-check:
	pnpm --dir web test
	pnpm --dir web run typecheck
	pnpm --dir web run lint

## build: WebUI + static binary
build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/polyglot ./cmd/polyglot

## build-go: binary only, reusing whatever is already in web/dist
build-go:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/polyglot ./cmd/polyglot

## check: every gate — Go tests, official-SDK compatibility, vet, TypeScript,
## ESLint, production build
check: test compatibility-test lint
	pnpm --dir web run build

test:
	go test ./...

test-race:
	go test -race ./...

## compatibility-test: the official OpenAI, Anthropic and Google SDKs driving a
## real Polyglot binary over HTTP. Separate module, so these SDKs are test-only
## dependencies and never reach the production binary — see tests/compatibility.
## -count=1 defeats the test cache on purpose: these tests build and run the
## binary from the parent module, which the cache does not track, so a cached
## pass can hide a change to the very thing under test.
compatibility-test:
	cd tests/compatibility && go test -count=1 ./...

## lint: Go and TypeScript
lint: web-check
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...
	cd tests/compatibility && go vet ./...

## vulncheck: scan code paths using the pinned Go vulnerability analyzer.
vulncheck:
	GOTOOLCHAIN=$(GO_SECURITY_VERSION) go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

## run: start the server using the embedded UI
run: build-go
	DATA_DIR=./data ./bin/polyglot

## dev: Go API on :3000 proxying the UI to the Vite dev server on :5173.
## Run `make web-dev` in another terminal.
dev:
	POLYGLOT_DEV=true DATA_DIR=./data go run ./cmd/polyglot

## web-dev: Vite dev server with hot reload
web-dev:
	pnpm --dir web run dev

## catalog: refresh the embedded model price snapshot from models.dev.
## Commit the result — the binary ships with it so an offline gateway still
## has prices, and an operator can refresh it at runtime from the WebUI.
catalog:
	go run ./internal/pricing/gen

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t polyglot:$(VERSION) -t polyglot:latest .

clean:
	rm -rf bin web/node_modules
	find web/dist -mindepth 1 ! -name .gitkeep -delete
