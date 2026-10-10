not gonna work on this anymore, discontinued, but still works.

# xusage

> A local usage dashboard and cost tracker for coding agents — a fast,
> single-binary, [ccusage](https://github.com/ccusage/ccusage)-inspired
> alternative written in pure Go.

![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-green)
![Stdlib](https://img.shields.io/badge/stdlib%20only-4a9eff)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey)

`xusage` scans the log files that Claude Code, Codex, OpenCode, Kimi, Qwen Code,
Gemini CLI, Copilot, Grok, and a dozen other coding agents write to disk, prices
every token against an embedded LiteLLM rate snapshot, aggregates the result, and
serves a fast dark web UI — all from one static binary with **zero runtime
dependencies**.

- **No npm, no node_modules, no CDN, no build step.** The dashboard is
  hand-written vanilla HTML/CSS/JS embedded with `go:embed`.
- **Standard library only.** No third-party Go modules, no cgo.
- **Instant.** The engine parses files once, deduplicates, prices, and swaps an
  immutable snapshot behind an atomic pointer — the API never locks and never
  reparses on steady state.
- **Local-first.** Binds to loopback only (`127.0.0.1`); nothing leaves your
  machine.

## Install & run

With a Go 1.27+ toolchain:

```sh
go run ./cmd/xusage
```

Or build a single binary and run it:

```sh
go build -o xusage ./cmd/xusage
./xusage            # Windows: xusage.exe
```

Then open **http://127.0.0.1:8787**.

On startup the terminal prints an all-time summary and the dashboard URL:

```
  xusage dev

  ──────────────────────────────
  All-time tokens   4.04B
  Sessions          28
  Days tracked      4
  Models            12
  Total cost        $0.00
  ──────────────────────────────
  Dashboard         http://127.0.0.1:8787
  Ctrl+C to stop
```

## CLI flags

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `127.0.0.1:8787` | HTTP bind address (non-loopback binds are refused) |
| `-poll` | `2s` | Background rescan interval; changed files are reparsed incrementally |
| `-open` | `false` | Open the dashboard in the default browser on start |
| `-version` | `false` | Print the version and exit |

`Ctrl+C` shuts the server down gracefully.

## The dashboard

A single dark screen, served locally, with three views:

- **Overview** — all-time token volume, a range selector (last 24h / 3d / 7d /
  30d / 90d / All), a day-by-day drill-down showing every model's input, cached
  input, output and total tokens, and an all-time per-model breakdown table.
- **Leaderboard** — models ranked by token share, with proportional usage bars.
- **Share** — pick a time range (24h / 3d / 7d / 30d / 90d / All) and xusage
  renders a polished summary card (total tokens, agent runs, average tokens per
  run, cost, date range) over the bundled template, ready to **download as PNG**
  or **copy to your clipboard**.

Semantic token colors are fixed across charts, gauges, and tables:

| Token type | Color |
|---|---|
| Input | blue `#7DA2FF` |
| Output | mint `#5EEAD4` |
| Cache Read | amber `#FBD38D` |
| Cache Creation | orchid `#F0ABFC` |

> The full aggregation surface — daily / weekly / monthly / hourly, per-model,
> per-provider, per-session, per-project, billing blocks, and a live pulse — is
> exposed through the HTTP API below and is available for building your own
> views or tooling.

## Data sources

Every adapter mirrors upstream ccusage discovery: the environment variable wins
entirely over the home-relative default, comma-separated values are supported
where upstream supports them, and a missing data directory simply reports the
source as "not detected" — never an error.

| Source | Data directory | Env override | Parser |
|---|---|---|---|
| claude | `~/.claude/projects/**/*.jsonl` (also XDG config home) | `CLAUDE_CONFIG_DIR` | full JSONL transcripts |
| codex | `~/.codex/sessions/**` + `archived_sessions/**` | `CODEX_HOME` | full JSONL rollouts |
| opencode | `~/.local/share/opencode/storage/message/**/*.json` | `OPENCODE_DATA_DIR` | JSON storage records (legacy SQLite DB not read) |
| amp | `~/.local/share/amp/threads/*.json` | `AMP_DATA_DIR` | full thread files |
| droid | `~/.factory/sessions/**/*.settings.json` | `DROID_SESSIONS_DIR` | full settings files |
| codebuff | `~/.config/{manicode,*}/projects/*/chats/chat-messages.json` | `CODEBUFF_DATA_DIR` | full chat files |
| hermes | `~/.hermes/state.db` | `HERMES_HOME` | native read-only SQLite reader |
| pi | `~/.pi/agent/sessions/**/*.jsonl` | `PI_AGENT_DIR` | full session logs |
| goose | `~/.local/share/goose/data/sessions/` (+ platform variants) | `GOOSE_PATH_ROOT` | JSONL/JSON exports; native `sessions.db` is detected but not read |
| openclaw | `~/.openclaw`, `~/.clawdbot`, `~/.moltbot`, `~/.moldbot` session logs | `OPENCLAW_DIR` | full `.jsonl` incl. archived `.deleted.`/`.reset.` forms |
| kilo | `~/.local/share/kilo/` | `KILO_DATA_DIR` | JSONL subset of the `kilo.db` message schema (SQLite not read) |
| kimi | `~/.kimi` / `~/.kimi-code` `sessions/**/wire.jsonl` | `KIMI_DATA_DIR` | full wire logs |
| qwen | `~/.qwen/projects/*/chats/*.jsonl` | `QWEN_DATA_DIR` | full chat logs |
| copilot | `~/.copilot/otel/**/*.jsonl` | `COPILOT_OTEL_FILE_EXPORTER_PATH` (adds one exact exporter file) | full OTel JSONL |
| gemini | `~/.gemini/tmp/**/*.{json,jsonl}` | `GEMINI_DATA_DIR` | full session/stats files |
| grok | `~/.grok/sessions/**/updates.jsonl` (+ `summary.json`) | `GROK_HOME` | full session logs |

Where a source stores data in SQLite (opencode legacy, goose, kilo), xusage
parses the JSONL/JSON subset of the same record shapes instead of linking a
database driver; hermes ships a minimal built-in read-only SQLite reader. Native
database files may still be reported as "detected" without contributing entries.

Model names can be remapped before pricing with `XUSAGE_MODEL_ALIASES`
(a JSON object or a `from=to,from=to` list); `-fast` suffixed variants inherit
the base alias.

## HTTP API

All endpoints return camelCase JSON and accept `since`, `until`, `last`,
`project`, `session`, `model`, `provider`, `vendor`, `mode`, and `tz` query
parameters:

```
GET /api/meta      version, generatedAt, tz, detected providers
GET /api/summary   grand totals, firstDate/lastDate/days/sessions/models, byProvider
GET /api/daily     rows keyed YYYY-MM-DD
GET /api/weekly    rows keyed by week-start date (Monday)
GET /api/monthly   rows keyed YYYY-MM
GET /api/hourly    rows keyed "YYYY-MM-DD HH"
GET /api/models    per-model rows with share and first/last activity
                   (?trend=1 adds a zero-filled 7-day trend7 per model)
GET /api/providers vendor-family rollups (Anthropic, OpenAI, DeepSeek, …),
                   cost-descending, each with its per-model breakdown
GET /api/sessions  per-session rows (?limit=N, default 200)
GET /api/projects  per-project rows
GET /api/blocks    billing windows (?limit=N&active=true)
GET /api/live      cheap poll payload for the Live view; includes the latest
                   entry as {provider,model,at}
POST /api/refresh  force rescan, returns new meta
```

`/api/models` rows carry `key`, `vendor`, token buckets, `cost`, `messages`,
`sessions`, `share`, `missingPricing`, `firstActivity`/`lastActivity`, and — with
`?trend=1` — `trend7[7]{date,tokens,cost}` ending today. `/api/providers` rows
carry `key`/`label`, token buckets, `cost`, `messages`, `sessions`, `share`, and
a cost-sorted `models[]` breakdown. Vendor families are derived from model-name
prefixes (Anthropic, OpenAI, Google, DeepSeek, Qwen, Moonshot, Zhipu, xAI,
LongCat, Mistral, Meta, Cohere, MiniMax, Amazon, Baidu, Tencent, ByteDance, …);
anything unrecognized buckets as `Other`.

Empty data yields zero-valued `200`s, never errors. Errors are
`{"error":"..."}` with a proper status code.

## How it works

```
sources/<name>  ──discover──▶ .jsonl/.json/db files
      │                             │
      └──────────parse──────────────┘   →  []model.Entry
                    │
             engine: dedup → sort → price   (atomic snapshot swap)
                    │
     aggregate: daily/weekly/monthly/hourly/sessions/projects/models
     blocks: 5-hour billing windows, burn rate, projection
                    │
        server: JSON API + embedded dashboard
```

- **Load** discovers every registered source's data directory (env-var overrides
  win over home defaults), parses files in parallel with per-file error
  isolation, deduplicates replayed messages, resolves all three cost variants
  (reported / computed / auto) once, and swaps an immutable snapshot behind an
  atomic pointer — API handlers never lock and never reparse.
- **Refresh** re-stats every known file and reuses the per-file parse cache when
  neither mtime nor size changed, so a steady-state poll parses nothing.

## Development

```sh
go build ./...     # compile everything
go vet ./...       # static checks
go test ./...     # unit tests (table-driven, hermetic, no network)
gofmt -w <files>   # format everything you touch
```

`go test -race ./internal/engine ./internal/server` additionally needs
`CGO_ENABLED=1` and a C toolchain (gcc/clang); on machines without one the
regular test run above still covers concurrency with dedicated smoke tests.

See `AGENTS.md` for repository layout and contribution conventions, and
`docs/SPEC.md` for the binding engineering specification.

## Comparison with ccusage

Same mental model (scan local agent logs, price tokens, aggregate), different
runtime: ccusage is TypeScript/Rust with npm distribution; xusage is one Go
binary with zero runtime dependencies. Dedup rules, block semantics, and pricing
fallbacks are ported faithfully from ccusage's Rust core; where a source stores
data in SQLite, xusage parses the JSON subset or a minimal read-only reader
instead of requiring a database library.

## License

MIT — the full license text ships in [LICENSE](LICENSE).
This project is inspired by [ccusage](https://github.com/ccusage/ccusage),
which is also MIT-licensed.
