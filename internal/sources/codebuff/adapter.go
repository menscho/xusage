// Registration of the Codebuff source. The engine discovers files through
// DiscoverChatFiles and parses each one with ParseChatFile; see paths.go and
// parser.go.

package codebuff

import "xusage/internal/sources"

func init() {
	sources.Register(sources.Source{
		Name:     "codebuff",
		Discover: DiscoverChatFiles,
		Parse:    ParseChatFile,
	})
}
