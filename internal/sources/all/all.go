// Package all wires every built-in source adapter into the registry.
//
// Each adapter registers itself via init(); blank-import this package to link
// every source into the binary. The integrator maintains the import list.
package all

import (
	_ "xusage/internal/sources/amp"
	_ "xusage/internal/sources/augment"
	_ "xusage/internal/sources/claude"
	_ "xusage/internal/sources/cline"
	_ "xusage/internal/sources/codebuff"
	_ "xusage/internal/sources/codex"
	_ "xusage/internal/sources/copilot"
	_ "xusage/internal/sources/cursor"
	_ "xusage/internal/sources/devin"
	_ "xusage/internal/sources/droid"
	_ "xusage/internal/sources/gemini"
	_ "xusage/internal/sources/goose"
	_ "xusage/internal/sources/grok"
	_ "xusage/internal/sources/hermes"
	_ "xusage/internal/sources/kilo"
	_ "xusage/internal/sources/kimi"
	_ "xusage/internal/sources/openclaw"
	_ "xusage/internal/sources/opencode"
	_ "xusage/internal/sources/pi"
	_ "xusage/internal/sources/qwen"
	_ "xusage/internal/sources/roocode"
	_ "xusage/internal/sources/trae"
	_ "xusage/internal/sources/zed"
)
