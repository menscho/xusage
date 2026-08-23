// Package gemini adapts Gemini CLI chat logs under $GEMINI_DATA_DIR (default
// ~/.gemini/tmp): per-chat .json / .jsonl documents.
//
// Discovery mirrors upstream adapters/gemini/src/paths.rs: the env override
// wins over the home default; both .json and .jsonl files are collected.
// Parsing mirrors parser.rs pragmatically: records carry a tokens/stats block
// with input/output/cached/thoughts/total counters (several key spellings are
// accepted). Cached tokens overlap the prompt, so input is reduced by the
// cached portion; thoughts fold into output; a lone total lands in output or
// ExtraTotalTokens.
package gemini
