# AGENTS.md — xusage

Guidance for AI agents (and humans) working in this repository.

## What this is

xusage parses local coding-agent usage logs, prices tokens, aggregates them,
and serves a local web dashboard. Go 1.27, **standard library only** — no
third-party Go dependencies, no cgo, no node_modules. The dashboard is plain
HTML/CSS/JS under `internal/web/static/`, embedded with `go:embed`.

The binding engineering spec is `docs/SPEC.md`. Read it before changing
architecture-level behavior (data model, API shape, dedup/block semantics,
design system).

## Repository layout

```
cmd/xusage/            CLI entry point: flags, banner, graceful shutdown
internal/model/        shared data types (Entry, Row, Block, Mode)
internal/sources/      one adapter package per coding agent
  source.go            registry (Source{Discover, Parse}, Register/All/Names)
  all/                 blank-import hub wiring every adapter (integrator-owned)
  adapter/             shared adapter helpers (fs walking, timestamps, ptrs)
  jsonx/               shared JSONL/JSON parsing helpers
  sourcesutil/         shared env/dir helpers used by qwen/gemini-style adapters
  <name>/              paths.go (discovery), parser.go, adapter.go (init), tests
internal/pricing/      LiteLLM snapshot lookup, aliases, cost math
internal/engine/       store (snapshot + incremental refresh), dedup,
                       aggregate (daily/…/summary), blocks (5h windows),
                       vendors.go (VendorOf families, /api/providers rollups,
                       model details with trend7)
internal/server/       HTTP API handlers + middleware; handlers_pulse.go
                       (/api/pulse, extended /api/live), handlers_vendors.go
                       (/api/providers, /api/models detail)
internal/web/          go:embed + static dashboard (index.html, css, js views);
                       13 views in four zones per SPEC §13.1 — js/ also has
                       view_leaderboard.js, view_clis.js, view_providers.js
internal/fixtures/     seeded deterministic test-data generators
docs/                  SPEC.md (binding), NOTES.md (append-only coordination)
```

## Build & test

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./internal/engine ./internal/server   # needs CGO_ENABLED=1 + a C
                                                    # toolchain (no gcc here;
                                                    # plain go test still green)
gofmt -w <files>                                    # format everything you touch
```

Tests are table-driven, use `t.TempDir()`, never touch the network, and never
read real user data. Missing data directories are normal (return `nil, nil`),
never errors.

## Ownership rules

Baseline contract files have **frozen exported signatures** — you may add
types, functions, and files, never rename or remove existing exports:

- `internal/model/usage.go`
- `internal/sources/source.go`
- `internal/pricing/pricing.go` (add files like `aliases.go`, tests)
- `internal/engine/{store,aggregate,blocks,dedup}.go`
- `internal/web/embed.go`

`docs/SPEC.md` and `go.mod` are frozen. `internal/sources/all/all.go` is
integrator-only. The redesigned dashboard surface (`internal/web/static/**`,
plus `internal/engine/vendors.go`, `internal/server/handlers_pulse.go`,
`internal/server/handlers_vendors.go`) is owned per the SPEC §13.6 map; its
JS contracts are frozen too — `XU` and `XUChart` keep their §9.1 signatures
and only gain the §13 additions (`fmt.big`, `pill`, `pulse`/`onPulse`,
`sparkline`/`rankbar`/`gauge`). Cross-agent issues go to `docs/NOTES.md` as
one `- [agent] note` line — append only.

## How to add a source adapter

1. Create `internal/sources/<name>/` with:
   - `paths.go` — discovery. Mirror upstream ccusage
     (`rust/adapters/<name>/src/paths.rs`): env-var override wins entirely
     over the home-relative default; comma-separated env values; only
     existing directories count. Missing data ⇒ `([]string(nil), nil)`.
   - `parser.go` — map whatever token kinds exist onto `model.Entry`:
     a lone grand total → `ExtraTotalTokens`; provider-reported cost →
     `ReportedCost` (`*float64`); leave `Provider` empty (the engine stamps it).
   - `adapter.go` — `init()` calling `sources.Register(sources.Source{
     Name: "<name>", Discover: ..., Parse: ...})`.
   - `<name>_test.go` — inline JSONL fixtures in `t.TempDir()`: happy path,
     malformed lines skipped, empty/missing dir ⇒ `nil, nil`.
2. Add `_ "xusage/internal/sources/<name>"` to `internal/sources/all/all.go`
   and to the `want` list in `internal/sources/all/all_test.go`.
3. `go build ./... && go vet ./internal/sources/<name> && go test
   ./internal/sources/<name>` must pass.

Shared helpers live in `internal/sources/adapter` and `internal/sources/jsonx`;
prefer them over re-rolling fs walking and timestamp parsing.

## Conventions

- Idiomatic Go; package doc comments; English comments.
- Comments must describe current behavior — sweep them when behavior changes.
- Dashboard JS is dependency-free ES5-ish; views register via
  `XU.registerView(name, {mount, unmount})` and must call chart cleanups on
  unmount. `node --check <file>` must pass for every static JS file.
- Dashboard layout, zones, pill attribution, and the `/api/pulse` poller
  follow SPEC §13 (binding): four sidebar zones (Intelligence / Attribution /
  Time Series / System), fixed semantic token colors, `fmt.big` compact
  numbers everywhere tokens display big, expandable exact-bucket subtables.
  Read §13 before touching dashboard files.
- Keep diffs minimal: no opportunistic refactors or reformatting of files you
  do not own.
