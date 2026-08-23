// Package fixtures generates deterministic synthetic usage data for tests.
//
// Every generator is a pure function of its options: identical inputs always
// produce byte-identical output (identifiers are derived from Seed, never
// from randomness or wall-clock time), so tests can assert exact contents and
// corpora stay stable across runs and platforms.
//
// The shapes mirror real Claude Code JSONL logs and Codex rollout files
// closely enough to exercise the adapters, dedup rules, pricing tiers, and
// aggregation paths end to end. WriteCorpus materializes full on-disk trees
// laid out like real agent homes:
//
//	dir/claude/projects/<project>/<session>-d<day>[-f<file>].jsonl  → CLAUDE_CONFIG_DIR=<dir>/claude
//	dir/codex/sessions/<session>.jsonl                              → CODEX_HOME=<dir>/codex
//	dir/codex/archived_sessions/<session>.jsonl
//
// The package depends only on the standard library and internal/model.
package fixtures
