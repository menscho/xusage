# xusage vs. the competition — gap analysis

A comparison of xusage against two leading open-source token trackers:
[ccusage](https://github.com/ccusage/ccusage) (16 agents, CLI, Rust/TS) and
[tokscale](https://github.com/junhoyeo/tokscale) (40+ agents, TUI+dashboard+Rust).
Source: their READMEs and feature lists as of 2026-08.

---

## At a glance

| Dimension | xusage | ccusage | tokscale |
|---|---|---|---|
| Interface | local web dashboard | CLI (table/JSON) | TUI (Ratatui) + web dashboard |
| Sources | 16 | 16 | 40+ |
| Engine | Go, stdlib | Rust (+TS CLI) | Rust native + TS |
| Hosting | offline-only, local | offline + npx | offline + **cloud sync / social** |
| Pricing | LiteLLM snapshot (embedded) | LiteLLM snapshot (Nix-pinned) | **Live** LiteLLM + 1h disk cache + fallbacks |
| Deployment | `./xusage.exe` (single binary) | `npx ccusage` | `bunx tokscale` / Docker |

---

## What ccusage does better / differently

1. **Terminal-native reporting.** All output is built for the CLI — colorful tables,
   auto-compact mode for narrow terminals, `--compact` for screenshots, machine-readable
   `--json`, and a `--statusline` for Claude Code status-bar hooks. xusage has no CLI at all.
2. **Instance / project grouping.** `ccusage claude daily --instances` groups usage by
   project/instance and `--project <name>` filters to one. xusage has projects as a flat
   table but no per-instance rollup.
3. **Explicit `--all` unified report** + `--sections daily,monthly,session` custom report
   composition in a single invocation.
4. **Config file defaults** with IDE autocomplete + validation — set a default mode/timezone
   once, not per-flag.
5. **`--no-cost` mode** for cost-free environments; hides cost columns cleanly.
6. **Nix-pinned, reproducible builds** and a scheduled auto-PR that refreshes the locked
   LiteLLM pricing snapshot.
7. **Source-scoped one-shot reports** — `ccusage claude daily --speed fast` is a single
   command; every source has a focused subcommand.

## What tokscale does better / differently (the bigger gap)

1. **Massively broader source support — 40+ agents.** IDE integrations xusage lacks entirely:
   Cursor, Cline, Roo Code, Kilo (IDE + CLI), Devin (CLI + desktop), Zed, Trae, Warp/Oz,
   Augment Code, Cherry Studio, Jcode, MiMo Code, fx, and many more. xusage covers 16.
2. **Interactive TUI.** A full Ratatui terminal UI: 8 tabs (Overview / Models / Daily /
   Hourly / Stats / Agents / Usage / Minutely), keyboard+mouse navigation, 16 color themes,
   real-time filtering/sorting, live refresh, JSON export from inside the TUI.
3. **Task-attributed reports.** An LLM-powered pass that summarizes each session into a title
   + category, then clusters sessions into high-level tasks (“Kiro Auth”, “System Config”),
   showing where tokens *actually went*. Backends: Apple FM (on-device, macOS), Claude, Codex,
   Gemini, Kiro, MiniMax.
4. **Social / leaderboard platform.** `tokscale login` + `tokscale submit` pushes usage to a
   public leaderboard with profiles, GitHub auth, QR token sharing, `autosubmit` via
   launchd/systemd/cron/scheduler, and a `tokscale.dev` site.
5. **Live pricing with fallbacks.** Real-time LiteLLM fetch + 1h disk cache + automatic
   OpenRouter fallback + hardcoded Cursor model pricing for unreleased models. xusage embeds
   a *snapshot* that goes stale.
6. **Workspace-level cost attribution.** `--group-by workspace,model` (with `--merge-worktrees`)
   attributes cost to the directory an agent ran in, resolving dash-mangled Claude slugs back
   to real paths.
7. **Subscription quota tracking.** `tokscale usage` pulls live session/weekly quota + reset
   time from Claude, Codex, Copilot, Grok, Kimi, MiniMax, Amp, Z.ai, Sakana — vendor-reported.
8. **Custom pricing overrides** (`custom-pricing.json`) + a 7-step pricing resolver (exact →
   alias → tier-strip → version-normalize → provider-prefix → Cursor → fuzzy).
9. **Per-session cost attribution** via `--group-by session,model` (joins cost back to a specific
   agent-CLI session) — built for multi-agent runners.
10. **Native Rust core** for ~10× parse speed, shipped as a prebuilt module.

## What xusage has that they don't (our strengths)

- **Local web dashboard with live polling** — ccusage is CLI-only; tokscale’s web view is
  read-only contribution graphs. xusage has full interactive charts, sortable tables, sidebar
  nav, dark theme, and instant navigation.
- **Engine / primitives / sessions / blocks** — richer domain model: per-model trend sparklines,
  5-hour billing blocks with burn rate + projection, attribution pills, session drill-down,
  leaderboard with 7-day trends.
- **Zero external runtime** — single static Go binary, no node/bun/npm install, no network.
  Works fully offline.
- **Mid-term optimization** — the engine is ~92% leaner than baseline (1250MB → ~70MB RSS).

---

## What xusage definitely needs (priority order)

### P0 — table-stakes competitors ship on day one
- [ ] **CLI / TUI interface.** Both competitors are terminal-first; xusage is web-only. At minimum
  a `xusage report daily|weekly|monthly|session|blocks|statusline` CLI with table + `--json` output
  and a `--compact` mode. A Ratatui TUI (like tokscale) would be the high-ceiling version.
- [ ] **More sources.** 16 → 40+. High-value gaps: **Cursor, Cline, Roo Code, Kilo (IDE), Devin,
  Zed, Trae, Copilot IDE, Augment Code, Cherry Studio, Jcode, MiMo Code, Goose, OpenClaw** — most
  of these are actively used and trackable. tokscale proves the breadth is the differentiator.
- [ ] **Live pricing.** Replace the embedded stale LiteLLM snapshot with live fetch + disk cache +
  fallbacks (OpenRouter, Cursor overrides), exactly like tokscale. Stale pricing silently mis-costs.

### P1 — high-value differentiators
- [ ] **Task-attributed reports.** An LLM-powered pass (Apple FM / Claude / Codex / Gemini backends)
  that summarizes each session into a task title and clusters them — shows *where tokens actually
  went*, not just which model. This is tokscale’s signature feature.
- [ ] **Workspace / project cost grouping.** `--group-by workspace,model` + `--merge-worktrees`,
  resolving per-agent path slugs back to real directories, so you can answer “what did this repo
  cost me?”
- [ ] **Subscription quota tracking.** Pull live session/weekly quotas + reset times from
  Claude/Copilot/Grok/Kimi/MiniMax/Amp/Z.ai — vendor-reported, so “85% left, resets in 2h15m”
  is always accurate.
- [ ] **Custom pricing overrides** (`custom-pricing.json`) + a multi-step resolver (alias →
  tier-strip → version-normalize → provider-prefix → fuzzy) so unreleased models get priced.
- [ ] **Session-level cost attribution** (`--group-by session,model`) for multi-agent runners.

### P2 — reach goals
- [ ] **Social / leaderboard platform.** Login + autosubmit (launchd/systemd/cron) + public
  profiles + GitHub auth. tokscale’s community moat; would require a hosted backend.
- [ ] **Nix / reproducible builds** and auto-pricing-update PRs (ccusage’s supply-chain hygiene).
- [ **Status-line / hooks integration** for Claude Code status bar (ccusage’s `--statusline`).

---

## The honest bottom line

xusage has the **richest dashboard and engine model** of the three, but it’s held back on
**interface** (web-only — no CLI/TUI), **breadth** (16 vs 40+ sources), and **freshness**
(static pricing snapshot). The two highest-leverage, fully-achievable moves are:

1. **Add a CLI/TUI + more sources** — brings xusage to parity with ccusage’s day-one experience
   and tokscale’s breadth.
2. **Live pricing** — a localized, offline-safe version of tokscale’s pricing resolver.

Task attribution and the social platform are the “wow” features but require an LLM backend or
hosted service; worth doing once CLI + sources + live pricing are in place.

Start with CLI/TUI + sources + live pricing — that closes the structural gap with zero quality risk.
