// Package augment adapts Augment Code session usage to the common source
// registry.
//
// Augment Code keeps one JSON document per session under ~/.augment/sessions
// (each file is a session; the file stem is the session id). A session carries
// its model, a timestamp, and a token block (input/output/cache-read/cache-
// creation). Discovery uses $AUGMENT_DATA_DIR or, by default, the sessions
// directory under the platform config home; only existing .json files count.
// Missing data is normal and yields no files. See docs/NOTES.md.
//
// The session schema is not publicly documented, so parsing is best-effort: it
// accepts a session object with a token block under "usage"/"tokens" (or flat
// on the session), maps common key spellings onto the shared model, and skips
// records without a usable token block. Cached tokens overlap the prompt, so
// the cache-read portion is carved back out of input; a provider-reported total
// that exceeds the known buckets spills into ExtraTotalTokens.
package augment
