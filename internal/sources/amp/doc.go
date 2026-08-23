// Package amp adapts Amp's local thread data: per-thread JSON files under
// $AMP_DATA_DIR/threads (default ~/.local/share/amp/threads).
//
// Discovery mirrors upstream adapters/amp/src/paths.rs: the env override wins
// entirely over the home default; entries are comma-separated and must exist.
// Parsing mirrors parser.rs: a thread carries an id, a messages array, and an
// optional usage_ledger whose events take precedence over per-message usage.
package amp
