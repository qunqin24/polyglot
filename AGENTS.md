# Repository Guidelines

## Project Structure & Module Organization

Polyglot is a Go gateway with an embedded React WebUI. `cmd/polyglot/` contains the executable entrypoint. Backend packages live under `internal/`: protocol-neutral types in `canonical/`, one codec per wire protocol in `protocol/`, request orchestration in `gateway/`, and HTTP/admin routes in `api/`. Keep SQLite queries in `internal/store/`. Ordered schema changes belong in `migrations/` as `NNNN_description.sql`; never edit an applied migration.

The React 19, TypeScript, Vite, and Tailwind frontend lives in `web/`. Pages are in `web/src/pages/`, reusable components in `web/src/components/`, and utilities and typed translation catalogs in `web/src/lib/`. Go unit and integration tests sit beside their packages as `*_test.go`; official-client tests form a separate Go module in `tests/compatibility/`.

## Build, Test, and Development Commands

- `make web-deps`: install the pinned pnpm dependencies.
- `make web-dev` and `make dev`: run Vite on `:5173` and the Go API on `:3000` in separate terminals.
- `make build`: build the WebUI and static binary at `bin/polyglot`.
- `make test`: run the root Go test suite.
- `make compatibility-test`: exercise the built server through official vendor SDKs.
- `make check`: run all tests, type checking, linting, vetting, and the production frontend build. Run this before submitting changes.

## Coding Style & Naming Conventions

Use idiomatic Go and run `gofmt`; package names are short and lowercase, exported identifiers use `PascalCase`, and errors should include context. All protocol conversions must pass through `internal/canonical`—never add direct vendor-to-vendor converters or silently discard unsupported fields.

TypeScript is strict and checked by ESLint 9. Use two-space indentation, `PascalCase` React components, `camelCase` functions, and lowercase page filenames. Do not suppress errors with `any`, `@ts-ignore`, or `eslint-disable`. Route every visible string through `t()` and update both English and Chinese catalogs.

## Testing Guidelines

Name Go tests `TestBehavior` in `*_test.go`. Add codec cases under `internal/protocol/<name>/`, wire behavior under `internal/api/`, and real HTTP SDK coverage under `tests/compatibility/`. Run `make test-race` after changing streaming, gateway, or usage-logging code. Migration changes must also be tested against an older database.

## Commit & Pull Request Guidelines

This checkout has no project commit history yet, so no convention is established. Use concise, imperative subjects (for example, `Add Gemini stream fidelity test`) and keep each commit focused. Pull requests should explain behavior and risk, list commands run, link relevant issues, and include screenshots for WebUI changes. Never commit API keys, `data/`, generated `web/dist/`, or local binaries.
