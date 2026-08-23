package fixtures

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CorpusOpts controls the shape of the generated multi-provider corpus.
// Zero-value fields select deterministic defaults; Seed drives every choice,
// so identical options always materialize byte-identical trees.
type CorpusOpts struct {
	Seed        int64 // default 42
	Projects    int   // Claude projects (default 3)
	Sessions    int   // sessions per project (default 2)
	Days        int   // days of history (default 5)
	FilesPerDay int   // session files per project/day (default 1)

	MessagesMin int       // usage lines per file lower bound (default 3)
	MessagesMax int       // upper bound (default 6)
	Models      []string  // model rotation (defaults: sonnet/opus/haiku 4)
	Now         time.Time // reference "today" (default 2025-06-10T18:00Z)
	WithErrors  bool      // include malformed/null/synthetic/no-usage noise lines
}

// CorpusInfo summarizes a written corpus. Entries counts well-formed
// usage-bearing lines (Claude lines with a usage block, Codex token_count
// events); malformed noise and usage-less filler lines are excluded.
type CorpusInfo struct {
	Files     int
	Entries   int
	Providers []string
	Root      string
}

func (o *CorpusOpts) fill() {
	if o.Seed == 0 {
		o.Seed = 42
	}
	if o.Projects == 0 {
		o.Projects = 3
	}
	if o.Sessions == 0 {
		o.Sessions = 2
	}
	if o.Days == 0 {
		o.Days = 5
	}
	if o.FilesPerDay == 0 {
		o.FilesPerDay = 1
	}
	if o.MessagesMin == 0 {
		o.MessagesMin = 3
	}
	if o.MessagesMax < o.MessagesMin {
		o.MessagesMax = o.MessagesMin + 2
	}
	if o.Now.IsZero() {
		o.Now = time.Date(2025, 6, 10, 18, 0, 0, 0, time.UTC)
	}
	if len(o.Models) == 0 {
		o.Models = []string{
			"claude-sonnet-4-20250514",
			"claude-opus-4-20250514",
			"claude-haiku-4-20250514",
		}
	}
}

// WriteCorpus materializes a deterministic multi-provider usage corpus under
// dir and returns summary info. The layout mirrors real agent homes, so tests
// can point CLAUDE_CONFIG_DIR at dir/claude and CODEX_HOME at dir/codex:
//
//	dir/claude/projects/<project>/<session>-d<day>[-f<file>].jsonl
//	dir/codex/sessions/<session>.jsonl
//	dir/codex/archived_sessions/<session>.jsonl
//
// Beyond happy-path traffic the corpus sprinkles in (all seeded): duplicate
// message IDs replayed across files, sidechain replays of an earlier message,
// long-context entries above the 200k tier, fast/standard speed variants,
// costUSD present and absent, and — with WithErrors — malformed lines,
// explicit-null fields, "<synthetic>" models, and usage-less assistant lines.
func WriteCorpus(dir string, opts CorpusOpts) (CorpusInfo, error) {
	opts.fill()
	rng := rand.New(rand.NewPCG(uint64(opts.Seed), uint64(opts.Seed)))
	info := CorpusInfo{Root: dir}

	claudeProjects := filepath.Join(dir, "claude", "projects")
	prevMsgID, prevReqID := "", "" // first message of the previous file, for cross-file replays

	for p := range opts.Projects {
		for s := range opts.Sessions {
			session := fmt.Sprintf("proj-%02d-sess-%02d", p, s)
			for d := opts.Days - 1; d >= 0; d-- { // oldest day first
				day := opts.Now.AddDate(0, 0, -d)
				for f := range opts.FilesPerDay {
					name := session
					if opts.Days > 1 {
						name += fmt.Sprintf("-d%02d", d)
					}
					if opts.FilesPerDay > 1 {
						name += fmt.Sprintf("-f%d", f)
					}

					var sb strings.Builder
					if opts.WithErrors && rng.IntN(6) == 0 {
						sb.WriteString(ClaudeLine(ClaudeLineOpts{Malformed: true}))
					}

					lines := opts.MessagesMin + rng.IntN(opts.MessagesMax-opts.MessagesMin+1)
					// Working hours only, deterministic minute offset per file.
					base := time.Date(day.Year(), day.Month(), day.Day(),
						9+rng.IntN(9), rng.IntN(60), 0, 0, time.UTC)
					firstID := fmt.Sprintf("msg_%s_0", name)
					firstReq := fmt.Sprintf("req_%s_0", name)

					for m := range lines {
						in := uint64(500 + rng.IntN(5000))
						if rng.IntN(12) == 0 {
							in = 250_000 // long-context tier
						}
						out := uint64(100 + rng.IntN(1500))
						cc := uint64(rng.IntN(2000))
						cr := uint64(rng.IntN(30000))
						line := ClaudeLineOpts{
							Timestamp: base.Add(time.Duration(m) * 3 * time.Minute),
							Version:   "1.0.14",
							ID:        fmt.Sprintf("msg_%s_%d", name, m),
							RequestID: fmt.Sprintf("req_%s_%d", name, m),
							SessionID: session,
							Model:     opts.Models[rng.IntN(len(opts.Models))],
							Input:     in,
							Output:    out,
							Cache5m:   cc,
							CacheRead: cr,
						}
						switch rng.IntN(10) {
						case 0:
							line.SpeedFast = true
						case 1:
							line.SpeedStandard = true
						}
						if rng.IntN(5) > 0 { // costUSD absent ~20% of the time
							line.CostUSD = MustCost(claudeCost(in, out, cc, cr))
						}
						if opts.WithErrors {
							switch {
							case m == 1 && rng.IntN(10) == 0:
								line.NullField = "cwd" // rejected by strict parsers
							case m == 2 && rng.IntN(10) == 0:
								line.Synthetic = true // skipped by parsers
							case m == 3 && rng.IntN(10) == 0:
								line.NoUsage = true // plain assistant line, not counted
							}
						}
						sb.WriteString(ClaudeLine(line))
						info.Entries++
					}

					// Sidechain replay: re-message the file's first entry under a
					// fresh request ID with heavier cache reads, as /btw logs do.
					if opts.WithErrors && rng.IntN(8) == 0 {
						sb.WriteString(ClaudeLine(ClaudeLineOpts{
							Timestamp: base.Add(time.Duration(lines)*3*time.Minute + time.Minute),
							Version:   "1.0.14",
							ID:        firstID,
							RequestID: "req-sidechain-" + name,
							SessionID: session,
							Input:     900,
							Output:    120,
							CacheRead: 50_000,
							Sidechain: true,
							CostUSD:   MustCost(0.01),
						}))
						info.Entries++
					}
					// Cross-file duplicate: replay the previous file's first
					// message ID + request ID with different token counts.
					if prevMsgID != "" && rng.IntN(6) == 0 {
						sb.WriteString(ClaudeLine(ClaudeLineOpts{
							Timestamp: base.Add(time.Duration(lines)*3*time.Minute + 2*time.Minute),
							Version:   "1.0.14",
							ID:        prevMsgID,
							RequestID: prevReqID,
							SessionID: session,
							Input:     4000,
							Output:    800,
							CacheRead: 12_000,
							CostUSD:   MustCost(0.05),
						}))
						info.Entries++
					}

					filePath := filepath.Join(claudeProjects, sessionDir(p), name+".jsonl")
					if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
						return info, err
					}
					if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
						return info, err
					}
					info.Files++
					prevMsgID, prevReqID = firstID, firstReq
				}
			}
		}
	}

	// Codex rollouts: one live session plus one archived rollout. Token counts
	// are cumulative total_token_usage snapshots, as real rollouts record them.
	for c := range 2 {
		root := filepath.Join(dir, "codex", "sessions")
		if c == 1 {
			root = filepath.Join(dir, "codex", "archived_sessions")
		}
		session := fmt.Sprintf("codex-sess-%02d", c)
		start := time.Date(opts.Now.Year(), opts.Now.Month(), opts.Now.Day(),
			8+c*6, 15, 0, 0, time.UTC)
		cwd := "/home/dev/" + session + "-repo"

		var cb strings.Builder
		cb.WriteString(SessionMeta(start, session, cwd))
		cb.WriteString(CodexLine(CodexLineOpts{
			Timestamp: start.Add(time.Minute),
			LineType:  "turn_context",
			Model:     "gpt-5.2",
		}))
		turns := 2 + rng.IntN(3)
		var totIn, totCached, totOut, totReasoning uint64
		for t := range turns {
			totIn += uint64(1000 + rng.IntN(8000))
			totCached += uint64(rng.IntN(4000))
			totOut += uint64(200 + rng.IntN(900))
			totReasoning += uint64(rng.IntN(300))
			line := CodexLineOpts{
				Timestamp: start.Add(2*time.Minute + time.Duration(t)*4*time.Minute),
				Input:     totIn,
				Cached:    totCached,
				Output:    totOut,
				Reasoning: totReasoning,
				Total:     totIn + totCached + totOut + totReasoning,
				Model:     "gpt-5.2",
			}
			if c == 1 && t == turns-1 { // archive tail reports only a grand total
				line.TotalOnly = true
				line.Total += 777
			}
			cb.WriteString(CodexLine(line))
			info.Entries++
		}
		filePath := filepath.Join(root, session+".jsonl")
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			return info, err
		}
		if err := os.WriteFile(filePath, []byte(cb.String()), 0o644); err != nil {
			return info, err
		}
		info.Files++
	}

	info.Providers = append(info.Providers, "claude", "codex")
	sort.Strings(info.Providers)
	return info, nil
}

// claudeCost approximates a plausible reported cost for the given buckets.
func claudeCost(in, out, cc, cr uint64) float64 {
	return 0.000003*float64(in) + 0.000015*float64(out) +
		0.00000375*float64(cc) + 0.0000003*float64(cr)
}

// sessionDir maps a project index onto its directory segment. Keeping the
// mapping trivial (one directory per project) mirrors real layouts closely
// enough for discovery tests while staying readable.
func sessionDir(project int) string {
	return fmt.Sprintf("proj-%02d", project)
}
