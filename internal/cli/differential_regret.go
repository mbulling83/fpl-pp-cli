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

type diffRegretRow struct {
	PlayerID    int     `json:"player_id"`
	Name        string  `json:"name"`
	Team        string  `json:"team"`
	Position    string  `json:"position"`
	NowCost     float64 `json:"now_cost"`
	Ownership   float64 `json:"selected_by_pct"`
	TotalPts    float64 `json:"total_points_in_window"`
	GWsAnalyzed int     `json:"gws_analyzed"`
	AvgPts      float64 `json:"avg_pts"`
}

func newDifferentialRegretCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var maxCost float64
	var gwWindow int
	var maxOwnership float64

	cmd := &cobra.Command{
		Use:   "differential-regret <entry-id>",
		Short: "High-scoring players you never owned over a gameweek window",
		Long: `Shows players outside your squad who scored heavily over the last N gameweeks.
Filter by price and ownership threshold to find the differentials that hurt most.`,
		Example: `  fpl-pp-cli differential-regret 12345
  fpl-pp-cli differential-regret 12345 --max-cost 65 --gw-window 6
  fpl-pp-cli differential-regret 12345 --max-ownership 15 --json`,
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

			// Bootstrap for player info
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
			posMap := map[int]string{1: "GKP", 2: "DEF", 3: "MID", 4: "FWD"}

			// Current GW
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

			// All squad players across window
			neverOwned := make(map[int]bool)
			for _, el := range elements {
				if id, ok := el["id"].(float64); ok {
					neverOwned[int(id)] = true
				}
			}
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
						neverOwned[int(eid)] = false
					}
				}
			}

			// GW points from live data
			playerWindowPts := make(map[int][]float64)
			liveRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT event_id, data FROM live`)
			if err == nil {
				defer liveRows.Close()
				for liveRows.Next() {
					var gwStr string
					var raw sqliteJSON
					if err := liveRows.Scan(&gwStr, &raw); err != nil {
						continue
					}
					gw := 0
					fmt.Sscanf(gwStr, "%d", &gw)
					if gw < startGW || gw > currentGW {
						continue
					}
					var liveData map[string]json.RawMessage
					if err := json.Unmarshal(raw.v, &liveData); err != nil {
						continue
					}
					var elemsRaw []map[string]any
					if err := json.Unmarshal(liveData["elements"], &elemsRaw); err != nil {
						continue
					}
					for _, el := range elemsRaw {
						if eid, ok := el["id"].(float64); ok {
							if !neverOwned[int(eid)] {
								continue
							}
							if stats, ok := el["stats"].(map[string]any); ok {
								if pts, ok := stats["total_points"].(float64); ok {
									playerWindowPts[int(eid)] = append(playerWindowPts[int(eid)], pts)
								}
							}
						}
					}
				}
			}

			var result []diffRegretRow
			for _, el := range elements {
				eid, _ := el["id"].(float64)
				id := int(eid)
				if !neverOwned[id] {
					continue
				}
				cost, _ := el["now_cost"].(float64)
				if maxCost > 0 && cost/10 > maxCost {
					continue
				}
				ownStr, _ := el["selected_by_percent"].(string)
				own := 0.0
				fmt.Sscanf(ownStr, "%f", &own)
				if maxOwnership > 0 && own > maxOwnership {
					continue
				}
				pts := playerWindowPts[id]
				if len(pts) == 0 {
					continue
				}
				var totalPts float64
				for _, p := range pts {
					totalPts += p
				}
				name, _ := el["web_name"].(string)
				teamID, _ := el["team"].(float64)
				pos, _ := el["element_type"].(float64)
				result = append(result, diffRegretRow{
					PlayerID:    id,
					Name:        name,
					Team:        teamByID[int(teamID)],
					Position:    posMap[int(pos)],
					NowCost:     cost / 10,
					Ownership:   own,
					TotalPts:    totalPts,
					GWsAnalyzed: len(pts),
					AvgPts:      totalPts / float64(len(pts)),
				})
			}

			sort.Slice(result, func(i, j int) bool {
				return result[i].TotalPts > result[j].TotalPts
			})

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "Players you missed (last %d GWs, never in your squad)\n\n", gwWindow)
			fmt.Fprintln(tw, "PLAYER\tTEAM\tPOS\tCOST\tOWN%\tTOTAL_PTS\tAVG_PTS")
			for _, r := range result {
				fmt.Fprintf(tw, "%s\t%s\t%s\t£%.1f\t%.1f%%\t%.0f\t%.1f\n",
					r.Name, r.Team, r.Position, r.NowCost, r.Ownership, r.TotalPts, r.AvgPts)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "Maximum player cost (e.g. 65 = £6.5m)")
	cmd.Flags().IntVar(&gwWindow, "gw-window", 6, "Gameweek window to analyze")
	cmd.Flags().Float64Var(&maxOwnership, "max-ownership", 0, "Maximum ownership % to include (0 = no filter)")
	return cmd
}
