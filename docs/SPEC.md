# xusage — Engineering Spec (binding for all agents)

xusage is a ccusage clone in pure Go: it parses local coding-agent usage logs, prices
tokens, aggregates them, and serves a fast, dark, well-designed local web dashboard.
All aggregation/API logic is Go; the dashboard is hand-written vanilla HTML/CSS/JS
embedded in the binary (no node_modules, no CDN, no build step).

Upstream references (read-only, fetch via FetchURL when needed):
- Overview/features: https://raw.githubusercontent.com/ccusage/ccusage/main/apps/ccusage/README.md
- Core types:        https://raw.githubusercontent.com/ccusage/ccusage/main/rust/crates/ccusage-core/src/types.rs
- Cost math:         https://raw.githubusercontent.com/ccusage/ccusage/main/rust/crates/ccusage-core/src/cost.rs
- Aggregation:       https://raw.githubusercontent.com/ccusage/ccusage/main/rust/crates/ccusage-core/src/summary.rs
- Blocks:            https://raw.githubusercontent.com/ccusage/ccusage/main/rust/crates/ccusage/src/blocks.rs
- Adapters:          https://raw.githubusercontent.com/ccusage/ccusage/main/rust/adapters/<name>/src/{paths.rs,parser.rs,loader.rs}
  (names: amp claude codebuff codex common copilot droid gemini goose grok hermes kilo kimi openclaw opencode pi qwen)

## 0. Ground rules (every agent)

- Go 1.27, **stdlib only**. No third-party Go deps, no cgo. Dashboard assets are plain
  files under `internal/web/static/` embedded with `go:embed`.
- Work only inside your owned files/dirs (ownership table below). Never edit `go.mod`,
  other agents' packages, or this spec. If something outside your scope is broken or
  missing, append one line to `docs/NOTES.md`: `- [<agent-name>] <note>` and move on.
- Exported signatures in the baseline contract files (`internal/model/usage.go`,
  `internal/sources/source.go`, `internal/pricing/pricing.go`, `internal/engine/*.go`)
  are **frozen**: you may add types/functions/files, never rename/remove existing ones.
- Idiomatic Go, English comments, package doc comments, table-driven tests.
  `gofmt -w` everything you write.
- Verify before reporting: `go vet ./<your pkgs>` and `go test ./<your pkgs>` pass.
  If a *foreign* package fails to compile (another agent mid-work), do not fix it;
  note it in NOTES.md and verify your own files with `go build ./<your pkg>`.
- Windows host, Git Bash shell. Use `os.UserHomeDir()` / `filepath` everywhere; tests
  use `t.TempDir()`. Data dirs may not exist — absence is normal (detected=false), never an error.
- Report format at end of your run: files written, how verified, deviations, NOTES entries.

## 1. Repo layout & ownership

```
go.mod                          (frozen, exists)
docs/SPEC.md                    (this file, frozen)
docs/NOTES.md                   (append-only coordination log)
cmd/xusage/main.go              cmd-main
internal/model/usage.go         (baseline exists; model behavior tests -> agg-owner covers usage)
internal/sources/source.go      (frozen baseline)
internal/sources/all/all.go     INTEGRATOR ONLY (import hub wiring all adapters)
internal/sources/<name>/        adapter-<name>   (16 adapters, one dir each)
internal/pricing/               pricing (baseline exists; harden + tests)
internal/engine/store.go        loader (baseline exists; perf + incremental + tests)
internal/engine/aggregate.go    agg (baseline exists; full features + tests)
internal/engine/blocks.go       blocks (baseline exists; faithful port + tests)
internal/server/                server (HTTP, API handlers, middleware, tests)
internal/web/embed.go           server (go:embed of static)
internal/web/static/index.html  web-core
internal/web/static/css/style.css          web-core
internal/web/static/js/app.js              web-core
internal/web/static/js/charts.js           charts
internal/web/static/js/view_overview.js    view-overview
internal/web/static/js/view_daily.js       view-daily
internal/web/static/js/view_weekly.js      view-weekly-monthly
internal/web/static/js/view_monthly.js     view-weekly-monthly
internal/web/static/js/view_hourly.js      view-hourly
internal/web/static/js/view_models.js      view-models
internal/web/static/js/view_sessions.js    view-sessions-projects
internal/web/static/js/view_projects.js    view-sessions-projects
internal/web/static/js/view_blocks.js      view-blocks
internal/web/static/js/view_live.js        view-live
internal/fixtures/              fixtures (deterministic synthetic data generators)
README.md                       docs (integrator/docs phase)
AGENTS.md                       docs phase
```

Placeholder `static/index.html|css/style.css|js/app.js` stubs exist so `go:embed`
compiles; their owners overwrite them completely.

## 2. Core data model (`internal/model`, frozen)

See `internal/model/usage.go`. Summary:
- `Entry` — one usage event: ID, RequestID, Timestamp (UTC), Model/RawModel, Provider,
  Project, SessionID, Version, InputTokens, OutputTokens, CacheCreationTokens (5m or
  undifferentiated), CacheCreation1hTokens, CacheReadTokens, ExtraTotalTokens (provider
  reported a total but not the split), CostUSD (resolved), ReportedCost (*float64),
  Messages, IsSidechain, IsAPIError, UsageLimitReset (*time.Time).
- `ModelBreakdown`, `Row` (generic aggregate row: Key + token counts + Cost + Messages +
  Sessions + Models []ModelBreakdown + First/LastActivity), `Block`, `BurnRate`, `Projection`.
- Dates/weeks/hours are computed at aggregation time from Timestamp + timezone — never stored.

## 3. Sources (`internal/sources`)

```go
type Source struct {
    Name     string                                  // "claude", "codex", ...
    Discover func() ([]string, error)                // absolute .jsonl/.json file paths
    Parse    func(path string) ([]model.Entry, error)
}
func Register(s Source)   // called from adapter package init()
func All() []Source       // sorted by Name
```

Each adapter lives in `internal/sources/<name>/` (package `<name>`), registers via
`init()`, and is wired into the build by `internal/sources/all/all.go` (blank imports)
— that hub file is written by the INTEGRATOR, nobody else.

### 3.1 Shared parsing rules (Claude-shape sources)

- Only lines containing `"usage":{` are candidates (fast substring prefilter).
- Reject lines where any of these fields is explicitly `null`:
  id, cwd, model, speed, costUSD, version, sessionId, requestId, isApiErrorMessage,
  cache_read_input_tokens, cache_creation_input_tokens.
- Reject if `version` present and not semver-prefix (`N.N.N`), or sessionId/requestId/
  message.id/model present but empty-string.
- `message.model == "<synthetic>"` → skip entry.
- `usage.speed == "fast"` → display Model gets suffix `-fast`; `speed:"standard"` ignored.
- `usage.cache_creation: {ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}` when
  present splits CacheCreationTokens(5m)/CacheCreation1hTokens; otherwise the flat
  `cache_creation_input_tokens` counts as 5m.
- `costUSD` (when present) → ReportedCost. Dedup happens in the loader, adapters emit
  every valid entry.
- Project/session come from the path layout (see per-source notes).

### 3.2 Per-source discovery (MUST verify against upstream paths.rs/parser.rs)

- **claude**: `$CLAUDE_CONFIG_DIR` or `~/.claude`; `projects/<project>/**/<file>.jsonl`;
  project = first segment under `projects/`; session = file stem (or immediate parent
  dir when file is `chat.jsonl`/nested subagent file).
- **codex**: `CODEX_HOME` or `~/.codex`; `sessions/**/*.jsonl` + `archived_sessions/**`.
  Rollout records: `session_meta` (cwd → project base name, model), `response_item`,
  `event_msg` with `payload.type=="token_count"` carrying
  `info.total_token_usage {input_tokens, cached_input_tokens, output_tokens,
  reasoning_output_tokens, total_tokens}`. Collapse consecutive token_count events
  (keep the latest per stretch — upstream replay/aggregate semantics); cached→CacheRead,
  reasoning folds into OutputTokens, when only `total_tokens` known → ExtraTotalTokens.
- **opencode**: `OPENCODE_DATA_DIR` or `~/.local/share/opencode`; storage message/part
  records with `modelID`, `providerID`, `tokens {input,output,reasoning,cache:{read,write}}`,
  `cost`, `time.created`. Project/session from storage layout.
- **amp / droid / codebuff / hermes / pi / goose / openclaw / kilo / kimi / qwen /
  copilot / gemini / grok**: fetch the upstream `paths.rs` + `parser.rs` for your source
  and mirror discovery + field mapping pragmatically. Env-var overrides win over home-relative
  defaults. If a legacy format is a binary DB impractical for stdlib, implement the
  JSONL/JSON subset and record the limitation in NOTES.md.
- Non-Claude mapping: whatever token kinds exist map onto Entry fields; a lone total goes
  to ExtraTotalTokens; provider-reported cost → ReportedCost; Provider = source name;
  Model = as logged (no aliasing in adapters).

## 4. Loader & dedup (`internal/engine/store.go`)

- `Store` holds an immutable `Snapshot` behind `atomic.Pointer` — readers never lock,
  API handlers stay instant. `Refresh()` rescans; a polling loop (2s) mtimes files and
  reparses only changed/new ones, then swaps the snapshot atomically.
- Parallel parse: worker pool sized `runtime.GOMAXPROCS(0)`, per-file isolation (a bad
  file yields zero entries, never a failed load).
- Dedup (port of upstream claude rules, keyed by Provider+ID+RequestID):
  duplicate = same message ID + request ID; ALSO treat a sidechain entry whose message ID
  matches a non-sidechain entry (any request ID) as a duplicate replay. Preference when
  replacing: non-sidechain over sidechain, then higher total token count, then presence
  of fast speed. Entries without an ID are kept as-is.
- Snapshot also records per-provider Detected flags and per-file mtimes.

## 5. Cost & pricing (`internal/pricing`)

- Embedded snapshot: `snapshot/litellm_pricing.json` (LiteLLM schema:
  `input_cost_per_token`, `output_cost_per_token`, `cache_creation_input_token_cost`
  (default input×1.25), `cache_read_input_token_cost` (default input×0.1),
  `*_above_200k_tokens` variants, `max_input_tokens`).
- Lookup order: exact key → separator-normalized (`.`/`@`→`-`, lowercased) → strip a
  trailing 8-digit date suffix and retry → longest-prefix match on normalized
  `key+"-"` boundary. Optional `XUSAGE_MODEL_ALIASES` env (JSON map or `a=b,cd=ef` list)
  resolves before lookup; `-fast` variants inherit the base alias.
- Modes: `display` = ReportedCost or 0; `auto` = ReportedCost if present else computed;
  `calculate` = always computed. Computed:
  `in*ri + out*ro + cc5*rcc + cc1h*(rin*2.0) + cr*rcr`, each bucket through
  `tiered(tokens, base, above)`: marginal split at the 200k default threshold when an
  `above` rate exists. Whole result × fastMultiplier (1.0 baseline) when speed==fast.
- Zero-token entries cost 0. Unknown model → 0 cost (UI flags "missing pricing").

## 6. Aggregation (`internal/engine/aggregate.go`) — all take `(entries, Opts)`

`Opts{TZ, Since, Until (YYYY-MM-DD inclusive), Last (n recent periods), Project,
Session, Model, Provider, Mode}`. Rows always carry per-model `Models []ModelBreakdown`.

- `Daily` key `2006-01-02`; `Weekly` key = Monday-start week date; `Monthly` key `2006-01`;
  `Hourly` key `2006-01-02 15` (24×N grid for heatmaps); `BySession` (sorted by
  LastActivity desc, includes First/LastActivity + duration data); `ByProject`;
  `ByModel` (global per-model totals + share); `Summary` (grand totals, first/last date,
  day count, session count, model count, per-provider split).
- Rows sort ascending by key (sessions/projects/models by cost desc). Filters apply
  before grouping; `Last` truncates to the n most recent groups after grouping.

## 7. Blocks (`internal/engine/blocks.go`)

Port of upstream `identify_session_blocks`: 5-hour windows; a block starts at the
floor-to-hour of its first entry; a new block opens when an entry is >5h after the
block START or >5h after the previous entry (the latter inserts an explicit gap block).
Active block = last block whose `start+5h` is in the future. Burn rate =
tokens+cost over elapsed minutes of the active block; projection extrapolates tokens
and cost to the block end. `UsageLimitReset` timestamps surface on the active block.

## 8. HTTP API (`internal/server`) — all JSON camelCase

Bind `127.0.0.1` only. Uniform query params on every analytics endpoint:
`since, until, last, project, session, model, provider, mode=auto|display|calculate,
tz=IANA` (default Local). Empty dataset → zero-valued 200s, never errors.

| Route | Purpose |
|---|---|
| `GET /api/meta` | version, generatedAt, tz, providers `[{name,detected,fileCount}]` |
| `GET /api/summary` | grand totals + firstDate/lastDate/days/sessions/models + byProvider |
| `GET /api/daily` | `Row[]` keyed by date |
| `GET /api/weekly` | `Row[]` keyed by week start |
| `GET /api/monthly` | `Row[]` keyed by month |
| `GET /api/hourly` | `Row[]` keyed `YYYY-MM-DD HH` |
| `GET /api/models` | per-model totals, share, firstSeen/lastSeen |
| `GET /api/sessions` | per-session rows (limit param, default 200) |
| `GET /api/projects` | per-project rows |
| `GET /api/blocks` | blocks (`limit`, `active=true` filters), latest first, incl. gap/active/burn/projection |
| `GET /api/live` | cheap poll payload: updatedAt, today {tokens,cost,messages}, activeBlock summary |
| `POST /api/refresh` | force rescan; returns new meta |

Errors: `{"error":"..."}` + proper status. Handlers read `store.Get()` only — no locking,
no reparse per request. `internal/web/embed.go` exposes `web.Handler()` serving
`static/` with gzip-if-accepted, immutable cache headers for hashed-less assets
(short max-age), and `index.html` fallback for `/`.

## 9. Dashboard (`internal/web/static`) — design language (binding)

Look: precise, dense, calm. NOT flashy. No gradients, no glow, no emoji, no purple.

- Palette (CSS custom properties): bg `#0B0E14`, panel `#12161F`, panel-2 `#171C27`,
  border `#232A38`, text `#E7EAF0`, muted `#8B93A7`, faint `#5B6373`,
  accent `#5EEAD4` (teal, used sparingly: active nav, focus, key series),
  up `#4ADE80`, down `#F87171`, warn `#FBBF24`.
- Chart categorical order: `#5EEAD4 #7DA2FF #F0ABFC #FBD38D #86EFAC #93C5FD #F9A8D4 #A5B4FC`.
- Type: system stack (`Inter, -apple-system, "Segoe UI", Roboto, sans-serif`),
  `font-variant-numeric: tabular-nums` on all numbers. 13px tables, 12px labels,
  26–30px semibold KPI figures. Uppercase 11px letter-spaced section labels.
- Space: 4px scale; cards radius 8px, controls 6px; 1px borders, flat shadows none.
- Layout: fixed left sidebar (200px, icons+labels: Overview, Daily, Weekly, Monthly,
  Hourly, Models, Sessions, Projects, Blocks, Live), topbar with global controls
  (range select: 7d/30d/90d/YTD/all/custom since-until, tz select, cost mode select,
  refresh button + last-updated stamp), content column max-width 1400px.
- Components (style.css): `.card`, `.kpi`, `.grid`, `.table` (sticky header, hover row,
  right-aligned numerics, expandable model-breakdown subrows), `.badge`, `.btn`,
  `.select`, `.input`, `.tooltip`, `.skeleton` (shimmer), `.empty` state, `.delta` (▲▼ colored).
- Behavior: hash router (`#/daily` etc.), each view = `XU.registerView(name,{mount,unmount})`;
  fetch → skeleton → render; re-render on state change; keyboard focus styles; responsive
  to 900px (sidebar collapses to icon rail).

### 9.1 Frozen JS APIs

`app.js` exposes global `XU`:
```js
XU.fetch(path)                      // GET, parses JSON, throws {status,message}
XU.q(extra)                         // querystring from XU.state + extra
XU.state                            // {range, since, until, tz, mode, project, model, provider}
XU.setState(patch); XU.onState(fn)  // triggers re-render of mounted view
XU.fmt.tokens(n)  XU.fmt.cost(n)  XU.fmt.num(n)  XU.fmt.pct(n)
XU.fmt.date('YYYY-MM-DD')  XU.fmt.datetime(iso)  XU.fmt.duration(sec)
XU.colors.categorical               // array above
XU.registerView(name, {mount(el), unmount()})
```

`charts.js` exposes global `XUChart` (pure SVG, no deps, ResizeObserver redraw,
shared tooltip div, nice-number axes):
```js
XUChart.line(el,{series:[{name,color,points:[{x,y}]}],xType:'time'|'index',yFormat,height,stacked})
XUChart.bars(el,{groups:[{label,values:[{name,value,color}]}],stacked,yFormat,height})
XUChart.donut(el,{slices:[{name,value,color}],center:{label,value}})
XUChart.heatmap(el,{rowLabels,colLabels,cells:[[value]],yFormat,colorScale})
XUChart.hbar(el,{bars:[{label,value,color}],yFormat})
```
All renderers return a cleanup function. Views must call it on unmount.

### 9.2 Views

- **Overview**: KPI cards (total cost, tokens, sessions, avg/day, today cost+delta vs
  yesterday), 30-day cost+tokens dual line, provider donut, top-8 models hbar, recent
  blocks strip. Uses `/api/summary /api/daily /api/models /api/blocks /api/live`.
- **Daily/Weekly/Monthly**: stacked token bars per model + cost line toggle, table with
  per-row model breakdown expansion, CSV download button (client-side blob).
- **Hourly**: heatmap (day rows × 24 cols, cost scale) + hourly profile line (avg by hour).
- **Models**: hbar ranking with cost/tokens toggle, donut share, per-model detail card
  (daily sparkline, first/last seen, token split).
- **Sessions/Projects**: sortable tables (cost, tokens, duration, activity), search box,
  click-through filter application.
- **Blocks**: horizontal timeline of blocks (gap-hatched), active block card with burn
  rate, projection, progress-to-5h meter, limit-reset warning.
- **Live**: auto-poll `/api/live` every 5s (pause on hidden tab), today counters,
  active block burn gauge, last-10-messages ticker from `/api/sessions?limit=10`.

## 10. Performance targets

Cold load of 50MB JSONL ≤ 2.5s (parallel parse). Warm API p99 ≤ 25ms (in-memory
aggregation). Polling refresh reparse touches only changed files. Page interactions
feel instant: skeletons ≤ 1 frame when data is cached; no layout thrash.

## 11. Testing & fixtures

Every Go package ships table-driven tests using inline JSONL or `internal/fixtures`
generators (seeded, deterministic, cover: dup IDs, sidechain replays, null-field
rejection, cache_creation 5m/1h split, costUSD present/absent, fast speed, >200k
long-context, empty/missing dirs). Server tests use `httptest`. No network in tests.

## 12. Waves

1. **Build wave** (33 agents, parallel): adapters ×16, pricing, loader, agg, blocks,
   server, web-core, charts, fixtures, cmd-main, views ×8.
2. **Integrate wave**: integrator wires `sources/all`, makes `go build ./... &&
   go vet ./... && go test ./...` green, resolves NOTES.md.
3. **QA wave**: API conformance, UI/E2E smoke, correctness cross-check, performance,
   docs/README/AGENTS.md.

## 13. Dashboard redesign (binding for the redesign wave)

High-density data presentation, seamless sidebar navigation, instant model attribution,
ultra-clean minimalist dark aesthetic.

### 13.0 "GSAP" skin (binding as of the gsap redesign pass — supersedes Carbon)

The dashboard skin is the GSAP chalkboard system: a near-black stage, warm cream
ink, one green brand accent, and color-coded category labels. Tokens:

- Surfaces: canvas `#0e100f` (Just Black), nested-panel/footer `#191919` (Off Black),
  hairline borders/dividers `#42433d` (Surface 25), muted text `#7c7c6f` (Surface 50).
- Ink: **warm cream `#fffce1` (Surface Cream)** is the primary text + outlined-button
  border + nav link color. Never use pure #fff or #000.
- Brand accent: **green `#0ae448`** (Shockingly Green) with gradient stop `#abff84`
  (Light Green) — links, active states, brand mark, the sole chromatic CTA stroke.
- Category colors (taxonomy, map to model families / provider types, never reused):
  green `#0ae448` = brand, orange `#ff8709`, pink `#fec5fb`, lilac `#9d95ff`, blue `#00bae2`.
- Type: **Mori** (substitute Inter / Inter Tight / Söhne — the humanist warmth is
  load-bearing, geometric fallbacks read colder but are acceptable), weights 400/600.
  Display scale: 224px (lh 0.9, tracking -4.48px) hero → 101 / 66 / 44 / 34 / 23 / 19 / 16 / 14.
  Mono (JetBrains Mono) for data/badges/labels/clock. Tabular-nums on all figures.
- Buttons: **outlined cream pills** — transparent fill, 1px `#fffce1` border, 100px radius,
  `#fffce1` text, Inter 18px 600, 15px×24px padding. NO filled solid CTAs except the
  primary green-gradient-stroke pill (border `#0ae448`→`#abff84` along 114deg).
- Signature: topbar = wordmark + cream nav links (16px Inter 400, underline on hover);
  page sections introduced by **curly-bracket annotations** `{ Section }` in 16–19px
  Inter 400 cream; cards = 8px radius + 24px padding + `#42433d` hairline dividers
  (no box-shadow — depth via surface-step shifts + gradient washes only); footer on
  `#191919` with cream multi-column links; `#tip` tooltip + `#toast` bottom-center.
- Chart bars: green `#0ae448` rounded-top rects on `#42433d` hairline gridlines, mono
  cream axis text, live bucket pulse (`barlive`), 2px `#191919` zero stub.
- GSAP `font-mori` token references resolve to Inter; charts/legends use cream + category hues.

### 13.0.1 Instant load (binding)

Perceived-instant page loads, every navigation:

1. Server: static assets are gzip-compressed and cache-friendly; API responses
   stay no-store. Handlers read the atomic snapshot (already true).
2. Client boot: `app.js` fires ALL view-data fetches for the default view in
   parallel immediately (no waterfall), then mounts the view.
3. Stale-while-revalidate hydration: every view's fetched payload is cached in
   `localStorage` keyed by view+querystring. On mount, if a cached payload
   exists it renders SYNCHRONOUSLY from cache first, then refetches and diffs —
   reload shows data instantly, then updates. Cache entries expire after 5min;
   payloads >1MB skip caching.
4. View switch re-render uses cached payloads when present (no skeleton flash);
   skeletons appear only when neither cache nor in-flight data exists.
5. The pulse poller continues feeding sidebar/topbar without blocking render.

### 13.0.2 Prior §13 contract

Pill attribution, expandable exact-bucket subtables, vendor chips/filters,
per-model trend sparklines, CSV export hooks, sortable tables with client-side
search, drill-down filters (project/session/model/provider/vendor), blocks
burn/projection/limit-reset surfaces, live poll hardening (hidden-tab pause,
clearInterval, best-effort ticker), and the `/api/pulse` `/api/providers`
`/api/models?trend=1` backend all REMAIN — the reference design lacks them and
they must be carried into the new components. Ranges: 1d | 3d | 7d | 30d | 90d |
All | Custom. Navigation consolidates Hourly/Daily/Weekly/Monthly into one
**Time Series** view with a granularity segment (hourly/daily/weekly/monthly)
driving `/api/{hourly,daily,weekly,monthly}`.

### 13.1 Sidebar — four zones with live badges

- **Intelligence**: Overview (#/overview), Leaderboard (#/leaderboard), Models
  (#/models). Zone badge: total token volume (compact "1.8B"); Overview row shows a
  pulsing dot when /api/pulse activeRun=true.
- **Attribution**: CLIs (#/clis), Providers (#/providers), Projects (#/projects),
  Sessions (#/sessions). Zone shows up to three brand chips for detected CLIs
  (letter-mark + name from /api/pulse clis).
- **Time Series**: Hourly, Daily, Weekly, Monthly. Each row embeds a mini 7-day
  sparkline (tokens) from /api/pulse last7.
- **System**: Blocks (#/blocks), Live (#/live). Live row carries a green streaming
  status dot while pulse polls succeed (<30s stale).

### 13.2 Top bar

Range buttons 7d/30d/90d/YTD/All/Custom (custom reveals since+until date inputs);
cost-mode select auto/reported/calculate; tz select Local/UTC; live pulse banner
"updated HH:MM:SS · <latest provider>" fed by /api/pulse latest; manual rescan button
(POST /api/refresh then re-poll).

### 13.3 KPI top tier (Overview)

Card 1 all-time token volume in compact notation (fmt.big: 1.8B/12.4M/987k) with
"sessions · days · messages" sub-line. Card 2 ranked model leaderboard top-5:
horizontal proportional bars + % share (from /api/models share). Card 3 token balance
gauge: single stacked bar input/output/cacheRead/cacheCreation with legend chips.
Card 4 cost overview: total tracked cost + amber "Unpriced models" flag when any
missingPricing.

### 13.4 Attribution matrix conventions

- Pill attribution tags everywhere models appear: `.pill.pill-cli` for the calling
  CLI (entry provider) and `.pill.pill-model` for the model — visually distinct.
- Row click expands an in-line subtable: exact input/output/cache-read/cache-create
  tokens, cost, and a weekly trend sparkline column.
- Chart bars carry hover tooltips with exact timestamped token counts (XUChart.bars
  values[] accept optional `tip` string overriding default tooltip content).

### 13.5 New backend surfaces (already implemented)

- `GET /api/providers` → VendorRow[] {key,label,tokens...,cost,messages,sessions,
  share,models[]} sorted by cost desc. Vendor derivation: engine.VendorOf(model)
  prefix families → Anthropic/OpenAI/Google/DeepSeek/Qwen/Moonshot/Zhipu/xAI/
  LongCat/Mistral/Meta/Cohere/MiniMax/… else Other.
- `/api/models` now returns ModelDetail[]: adds vendor, share, missingPricing,
  trend7[7]{date,tokens,cost} (zero-filled, snapshot-local today) when `?trend=1`.
  Filters gain `vendor=`.
- `GET /api/pulse` → {generatedAt,tokens,cost,messages,sessions,days,activeRun,
  clis[{name,detected,fileCount}],last7[7]{date,tokens,cost},latest{provider,model,at}}.
- `/api/live` gains `latest{provider,model,at}`.

### 13.6 View ownership map (redesign)

web-shell: index.html + css/style.css · app-core: js/app.js · charts-ext: js/charts.js
· view-overview: view_overview.js · view-leaderboard: view_leaderboard.js (new) ·
view-clis: view_clis.js (new) · view-providers: view_providers.js (new) ·
views-timeseries: hourly/daily/weekly/monthly .js · views-attrib: models/sessions/
projects .js · views-system: blocks/live .js. All rewrite their files completely;
frozen XU/XUChart API stays compatible, additions allowed per §13 contracts.
