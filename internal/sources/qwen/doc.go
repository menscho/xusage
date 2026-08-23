// Package qwen adapts Qwen Code chat logs: Gemini-style assistant records
// under $QWEN_DATA_DIR/projects/<project>/chats/*.jsonl (default
// ~/.qwen/projects/...).
//
// Discovery mirrors upstream adapters/qwen/src/paths.rs: the env override wins
// over the home default; only <root>/projects/<project>/chats/<file>.jsonl
// layouts are admitted. Parsing mirrors parser.rs: type=="assistant" records
// carry usageMetadata with prompt/candidates/thoughts/cached/total counters.
// Reasoning folds into output tokens; a grand total backfills missing buckets
// or spills into ExtraTotalTokens.
package qwen
