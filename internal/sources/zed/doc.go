// Package zed adapts Zed agent-thread usage data to the common source
// registry.
//
// Zed stores its Agent Panel threads in a SQLite database (threads.db) under
// ~/.local/share/zed/threads. Pure-Go xusage cannot link a SQLite driver
// (stdlib only, no cgo), so this adapter consumes the same thread data exported
// to newline-delimited JSON: one JSON object per turn, shaped like a thread
// record's payload. See docs/NOTES.md.
//
// Discovery mirrors upstream paths(): the ZED_DATA_DIR override (comma-separated)
// wins over the home default; only existing directories count. Parsing mirrors
// the Claude-shape sources pragmatically: each turn carries a model, a usage
// block (input/output/cache-read/cache-creation tokens), and an optional
// reported cost. Cached tokens are treated as a subset of the prompt, so the
// cache-read portion is carved back out of input.
package zed
