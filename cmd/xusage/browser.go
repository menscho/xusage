package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// openBrowser launches the default browser at url, using the
// platform-appropriate stdlib command. Failures are reported but never fatal:
// the dashboard is still reachable by hand.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[xusage] could not open browser: %v\n", err)
	}
}

// startupSummary mirrors the subset of /api/summary we display at startup.
type startupSummary struct {
	TotalTokens uint64  `json:"totalTokens"`
	Sessions    int64   `json:"sessions"`
	Days        int64   `json:"days"`
	ModelCount  int64   `json:"modelCount"`
	Cost        float64 `json:"cost"`
}

// printStartup writes a compact, nicely formatted launch banner: the app title
// appears immediately, then all-time usage is fetched and rendered once the
// engine has loaded its first snapshot (OnReady fires before that load).
func printStartup(url string) {
	const (
		accent = "\033[38;5;75m" // soft blue, matches the dashboard accent
		dim    = "\033[38;5;245m"
		bold   = "\033[1m"
		reset  = "\033[0m"
	)

	fmt.Printf("\n  %s%s xusage %s%s\n", accent, bold, version, reset)

	go func() {
		s := fetchSummary(url)

		fmt.Printf("  %s──────────────────────────────────────%s\n", dim, reset)
		fmt.Printf("  %s%-16s%s  %s%s%s\n", dim, "All-time tokens", reset, bold, humanTokens(s.TotalTokens), reset)
		if s.TotalTokens > 0 || s.Days > 0 {
			fmt.Printf("  %s%-16s%s  %s%s%s\n", dim, "Sessions", reset, reset, commas(s.Sessions), reset)
			fmt.Printf("  %s%-16s%s  %s%s%s\n", dim, "Days tracked", reset, reset, commas(s.Days), reset)
			fmt.Printf("  %s%-16s%s  %s%s%s\n", dim, "Models", reset, reset, strconv.FormatInt(s.ModelCount, 10), reset)
			fmt.Printf("  %s%-16s%s  %s$%.2f%s\n", dim, "Total cost", reset, reset, s.Cost, reset)
		}
		fmt.Printf("  %s──────────────────────────────────────%s\n", dim, reset)
		fmt.Printf("  %s%-16s%s  %s%s%s\n", dim, "Dashboard", reset, accent, url, reset)
		fmt.Printf("  %sCtrl+C to stop%s\n\n", dim, reset)
	}()
}

// fetchSummary polls /api/summary (all-time by default) until the engine's
// first load populates totals, or a few seconds elapse, whichever comes first.
func fetchSummary(base string) startupSummary {
	var s startupSummary
	client := &http.Client{Timeout: 800 * time.Millisecond}
	for i := 0; i < 30; i++ {
		resp, err := client.Get(base + "/api/summary")
		if err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&s)
			resp.Body.Close()
			if s.TotalTokens > 0 || i >= 15 {
				return s
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return s
}

// humanTokens compresses a token count into a short, human-readable string
// (e.g. 7.80B, 2.74M, 142.80K) for compact display.
func humanTokens(n uint64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.2fT", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.2fK", float64(n)/1e3)
	default:
		return strconv.FormatUint(n, 10)
	}
}

// commas formats an integer with thousands separators.
func commas(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
