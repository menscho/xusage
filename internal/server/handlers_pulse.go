// Package server — /api/pulse and extended /api/live payloads powering the
// redesigned sidebar badges and the live pulse banner.
package server

import (
	"net/http"
	"time"

	"xusage/internal/engine"
)

// pulsePayload is the single compact poll the sidebar and pulse banner use:
// all-time totals, active-run state, detected CLIs, a 7-day token series,
// and the most recent entry's attribution.
type pulsePayload struct {
	GeneratedAt string              `json:"generatedAt"`
	Tokens      uint64              `json:"tokens"`
	Cost        float64             `json:"cost"`
	Messages    int64               `json:"messages"`
	Sessions    int64               `json:"sessions"`
	Days        int64               `json:"days"`
	ActiveRun   bool                `json:"activeRun"`
	Clis        []cliBadge          `json:"clis"`
	Last7       []engine.TrendPoint `json:"last7"`
	Latest      *latestEntry        `json:"latest"`
}

type cliBadge struct {
	Name      string `json:"name"`
	Detected  bool   `json:"detected"`
	FileCount int    `json:"fileCount"`
}

type latestEntry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	At       string `json:"at"`
}

func latestOf(snap *engine.Snapshot) *latestEntry {
	if len(snap.Entries) == 0 {
		return nil
	}
	e := snap.Entries[len(snap.Entries)-1]
	return &latestEntry{
		Provider: e.Provider,
		Model:    e.Model,
		At:       e.Timestamp.UTC().Format(time.RFC3339Nano),
	}
}

func last7Series(snap *engine.Snapshot) []engine.TrendPoint {
	loc := time.Local
	today := time.Now().In(loc)
	days := make([]engine.TrendPoint, 7)
	idx := map[string]int{}
	for i := 0; i < 7; i++ {
		d := today.AddDate(0, 0, i-6)
		days[i].Date = d.Format("2006-01-02")
		idx[days[i].Date] = i
	}
	for i := range snap.Entries {
		e := &snap.Entries[i]
		if j, ok := idx[e.Timestamp.In(loc).Format("2006-01-02")]; ok {
			days[j].Tokens += e.TotalTokens()
			days[j].Cost += e.CostAuto
		}
	}
	return days
}

func buildPulse(snap *engine.Snapshot) pulsePayload {
	sum := snap.Summary(&engine.Opts{})
	active := snap.ActiveBlock(&engine.Opts{}, time.Now()) != nil
	clis := make([]cliBadge, 0, len(snap.Providers))
	for _, p := range snap.Providers {
		clis = append(clis, cliBadge{Name: p.Name, Detected: p.Detected, FileCount: p.FileCount})
	}
	return pulsePayload{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Tokens:      sum.TotalTokens,
		Cost:        sum.Cost,
		Messages:    sum.Messages,
		Sessions:    sum.Sessions,
		Days:        sum.Days,
		ActiveRun:   active,
		Clis:        clis,
		Last7:       last7Series(snap),
		Latest:      latestOf(snap),
	}
}

func handlePulse(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildPulse(engine.Get()))
}
