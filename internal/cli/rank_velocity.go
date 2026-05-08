package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/mbulling83/fpl-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

type rankVelocityRow struct {
	GW          int    `json:"gw"`
	Points      int    `json:"points"`
	OverallRank int    `json:"overall_rank"`
	RankDelta   int    `json:"rank_delta"`
	IsStreak    bool   `json:"is_improving_streak"`
	PeakRank    int    `json:"season_peak_rank"`
}

type rankVelocitySummary struct {
	EntryID      string            `json:"entry_id"`
	CurrentRank  int               `json:"current_rank"`
	PeakRank     int               `json:"peak_rank"`
	PeakGW       int               `json:"peak_gw"`
	BestStreak   int               `json:"best_improving_streak_gws"`
	TotalPoints  int               `json:"total_points"`
	GWs          []rankVelocityRow `json:"gameweeks"`
}

func newRankVelocityCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var last int

	cmd := &cobra.Command{
		Use:   "rank-velocity <entry-id>",
		Short: "GW-by-GW overall rank movement, streaks, and peak vs current",
		Long: `Shows how your overall rank has moved each gameweek, highlights improving
and declining streaks, and compares current rank to season peak.`,
		Example: `  fpl-pp-cli rank-velocity 12345
  fpl-pp-cli rank-velocity 12345 --last 10
  fpl-pp-cli rank-velocity 12345 --json`,
		Annotations: map[string]string{"mcp:read-only": "true"},
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entryID := args[0]
			if dbPath == "" {
				dbPath = defaultDBPath("fpl-pp-cli")
			}
			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening local database: %w\nRun 'fpl-pp-cli sync' first.", err)
			}
			defer db.Close()

			histRaw, err := db.Get("history", entryID)
			if err != nil {
				return fmt.Errorf("history not found for entry %s. Run 'fpl-pp-cli sync' first: %w", entryID, err)
			}
			var hist map[string]json.RawMessage
			if err := json.Unmarshal(histRaw, &hist); err != nil {
				return fmt.Errorf("parsing history: %w", err)
			}
			var current []map[string]any
			if err := json.Unmarshal(hist["current"], &current); err != nil {
				return fmt.Errorf("parsing current history: %w", err)
			}

			sort.Slice(current, func(i, j int) bool {
				gi, _ := current[i]["event"].(float64)
				gj, _ := current[j]["event"].(float64)
				return gi < gj
			})

			peakRank := 0
			peakGW := 0
			var rows []rankVelocityRow
			var prevRank int
			var totalPts int
			for _, gw := range current {
				gwNum, _ := gw["event"].(float64)
				pts, _ := gw["points"].(float64)
				rank, _ := gw["overall_rank"].(float64)
				totalPts += int(pts)
				rankInt := int(rank)
				if peakRank == 0 || rankInt < peakRank {
					peakRank = rankInt
					peakGW = int(gwNum)
				}
				delta := 0
				if prevRank > 0 {
					delta = prevRank - rankInt // positive = rising (rank number went down)
				}
				rows = append(rows, rankVelocityRow{
					GW:          int(gwNum),
					Points:      int(pts),
					OverallRank: rankInt,
					RankDelta:   delta,
					PeakRank:    peakRank,
				})
				prevRank = rankInt
			}

			// Mark improving streaks
			bestStreak := 0
			streak := 0
			for i := range rows {
				if rows[i].RankDelta > 0 {
					streak++
					rows[i].IsStreak = true
					if streak > bestStreak {
						bestStreak = streak
					}
				} else {
					streak = 0
				}
			}

			if last > 0 && len(rows) > last {
				rows = rows[len(rows)-last:]
			}

			currentRank := 0
			if len(rows) > 0 {
				currentRank = rows[len(rows)-1].OverallRank
			}

			summary := rankVelocitySummary{
				EntryID:     entryID,
				CurrentRank: currentRank,
				PeakRank:    peakRank,
				PeakGW:      peakGW,
				BestStreak:  bestStreak,
				TotalPoints: totalPts,
				GWs:         rows,
			}

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(summary)
			}

			fmt.Printf("Entry %s — Rank Velocity\n", entryID)
			fmt.Printf("Current rank: %s  |  Peak: %s (GW%d)  |  Total pts: %d\n\n",
				formatRank(currentRank), formatRank(peakRank), peakGW, totalPts)

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GW\tPTS\tRANK\tCHANGE\tPEAK")
			for _, r := range rows {
				delta := fmt.Sprintf("%+d", r.RankDelta)
				if r.RankDelta > 0 {
					delta = "↑" + fmt.Sprintf("%d", r.RankDelta)
				} else if r.RankDelta < 0 {
					delta = "↓" + fmt.Sprintf("%d", -r.RankDelta)
				}
				fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\n",
					r.GW, r.Points, formatRank(r.OverallRank), delta, formatRank(r.PeakRank))
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().IntVar(&last, "last", 0, "Show only the last N gameweeks (0 = all)")
	return cmd
}

func formatRank(r int) string {
	if r == 0 {
		return "-"
	}
	s := fmt.Sprintf("%d", r)
	out := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return out
}
