// Package trae adapts Trae usage data to the common source registry.
//
// Trae exposes usage through its API and a local cache. The cache is a set of
// JSON/JSONL documents holding per-request usage records; each record carries
// a model, a timestamp, and a token block (input/output/cache-read/cache-
// creation). This adapter reads that cache — $TRAE_DATA_DIR or, by default,
// ~/.cache/trae (the XDG cache location on Linux). Missing or non-directory
// entries are dropped silently; absence is normal. See docs/NOTES.md.
//
// The cache schema is not publicly documented, so parsing is best-effort: it
// accepts the shapes we can observe (flat records, a records/items array, or
// newline-delimited JSON) and maps whatever token keys exist onto the shared
// model. Records without a usable token block are skipped, never a reason to
// fail the file.
package trae
