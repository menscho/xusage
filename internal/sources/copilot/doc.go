// Package copilot adapts GitHub Copilot's OpenTelemetry file-exporter logs:
// JSONL spans under ~/.copilot/otel (or $COPILOT_OTEL_FILE_EXPORTER_PATH).
//
// Discovery mirrors upstream adapters/copilot/src/paths.rs. Parsing mirrors
// parser.rs pragmatically: each record carries an attributes object keyed
// "gen_ai.usage.*" for tokens, "gen_ai.response.model"/"gen_ai.request.model"
// for the model, and conversation/response ids for the session. Reasoning
// tokens fold into output; a lone total lands in ExtraTotalTokens.
package copilot
