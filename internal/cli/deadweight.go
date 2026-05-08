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

type deadweightRow struct {
	PlayerID   int     `json:"player_id"`
	Name       string  `json:"name"`
	Team       string  `json:"team"`
	GWsOwned   int     `json:"gws_owned"`
	AvgPts     float64 `json:"avg_pts_while_owned"`
	ExpectedG  float64 `json:"expected_goals"`
	ActualG    float64 `json:"actual_goals"`
	XGDelta    float64 `json:"xg_delta"`
	Score      float64 `json:"deadweight_score"`
}

func newDeadweightCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var gwWindow int

	cmd := &cobra.Command{
		Use:   "deadweight <entry-id>",
		Short: "Detect underperforming squad players vs their xG over a window",
		Long: `Shows players in your squad who have significantly underperformed their expected goals
over a recent gameweek window. Higher deadweight score = bigger underperformer.`,
		Example: `  fpl-pp-cli deadweight 12345
  fpl-pp-cli deadweight 12345 --gw-window 8
  fpl-pp-cli deadweight 12345 --json`,
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

			// Bootstrap for player names + xG
			bsRaw, err := db.Get("bootstrap-static", "bootstrap-static")
			if err != nil {
				return fmt.Errorf("bootstrap_static not found. Run 'fpl-pp-cli sync' first: %w", err)
			}
			var bs map[string]json.RawMessage
			if err := json.Unmarshal(bsRaw, &bs); err != nil {
				return fmt.Errorf("parsing bootstrap: %w", err)
			}
			var elements []map[string]any
			if err := json.Unmarshal(bs["elements"], &elements); err != nil {
				return fmt.Errorf("parsing elements: %w", err)
			}
			var teams []map[string]any
			if err := json.Unmarshal(bs["teams"], &teams); err != nil {
				return fmt.Errorf("parsing teams: %w", err)
			}
			teamByID := make(map[int]string, len(teams))
			for _, t := range teams {
				if id, ok := t["id"].(float64); ok {
					if name, ok := t["short_name"].(string); ok {
						teamByID[int(id)] = name
					}
				}
			}
			playerByID := make(map[int]map[string]any, len(elements))
			for _, e := range elements {
				if id, ok := e["id"].(float64); ok {
					playerByID[int(id)] = e
				}
			}

			// Find current GW
			var events []map[string]any
			if err := json.Unmarshal(bs["events"], &events); err != nil {
				return fmt.Errorf("parsing events: %w", err)
			}
			currentGW := 0
			for _, ev := range events {
				if fin, ok := ev["finished"].(bool); ok && fin {
					if id, ok := ev["id"].(float64); ok && int(id) > currentGW {
						currentGW = int(id)
					}
				}
			}
			startGW := currentGW - gwWindow + 1
			if startGW < 1 {
				startGW = 1
			}

			// Get unique squad players across all GWs in window
			squadIDs := make(map[int]int) // element_id -> count of GWs owned
			for gw := startGW; gw <= currentGW; gw++ {
				gwStr := fmt.Sprintf("%d", gw)
				var raw sqliteJSON
				err := db.DB().QueryRowContext(cmd.Context(),
					`SELECT data FROM entry_event WHERE id=?`,
					entryID+":"+gwStr).Scan(&raw)
				if err != nil {
					continue
				}
				var ev map[string]json.RawMessage
				if err := json.Unmarshal(raw.v, &ev); err != nil {
					continue
				}
				var picks []map[string]any
				if err := json.Unmarshal(ev["picks"], &picks); err != nil {
					continue
				}
				for _, p := range picks {
					if eid, ok := p["element"].(float64); ok {
						squadIDs[int(eid)]++
					}
				}
			}

			// Get per-GW points from element-summary
			type gwStat struct {
				points float64
				xg     float64
				goals  float64
			}
			playerGWStats := make(map[int][]gwStat)
			esRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT id, data FROM resources WHERE resource_type='element_summary'`)
			if err == nil {
				defer esRows.Close()
				for esRows.Next() {
					var rid string
					var raw sqliteJSON
					if err := esRows.Scan(&rid, &raw); err != nil {
						continue
					}
					var summary map[string]json.RawMessage
					if err := json.Unmarshal(raw.v, &summary); err != nil {
						continue
					}
					var history []map[string]any
					if err := json.Unmarshal(summary["history"], &history); err != nil {
						continue
					}
					for _, h := range history {
						eid, _ := h["element"].(float64)
						gw, _ := h["round"].(float64)
						if int(gw) < startGW || int(gw) > currentGW {
							continue
						}
						if _, owned := squadIDs[int(eid)]; !owned {
							continue
						}
						pts, _ := h["total_points"].(float64)
						xgStr, _ := h["expected_goals"].(string)
						xg := 0.0
						fmt.Sscanf(xgStr, "%f", &xg)
						goals, _ := h["goals_scored"].(float64)
						playerGWStats[int(eid)] = append(playerGWStats[int(eid)], gwStat{pts, xg, goals})
					}
				}
			}

			var result []deadweightRow
			for eid := range squadIDs {
				stats := playerGWStats[eid]
				if len(stats) == 0 {
					continue
				}
				var totalPts, totalXG, totalGoals float64
				for _, s := range stats {
					totalPts += s.points
					totalXG += s.xg
					totalGoals += s.goals
				}
				avgPts := totalPts / float64(len(stats))
				xgDelta := totalGoals - totalXG
				score := (totalXG - totalGoals) + (totalXG*2 - avgPts) // higher = more deadweight
				el := playerByID[eid]
				name, _ := el["web_name"].(string)
				teamID, _ := el["team"].(float64)
				result = append(result, deadweightRow{
					PlayerID:  eid,
					Name:      name,
					Team:      teamByID[int(teamID)],
					GWsOwned:  len(stats),
					AvgPts:    avgPts,
					ExpectedG: totalXG,
					ActualG:   totalGoals,
					XGDelta:   xgDelta,
					Score:     score,
				})
			}

			sort.Slice(result, func(i, j int) bool {
				return result[i].Score > result[j].Score
			})

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PLAYER\tTEAM\tGWs\tAVG_PTS\txG\tACTUAL_G\txG_DELTA\tDEADWT_SCORE")
			for _, r := range result {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%.1f\t%.2f\t%.0f\t%+.2f\t%.2f\n",
					r.Name, r.Team, r.GWsOwned, r.AvgPts, r.ExpectedG, r.ActualG, r.XGDelta, r.Score)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().IntVar(&gwWindow, "gw-window", 6, "Number of recent gameweeks to analyze")
	return cmd
}
