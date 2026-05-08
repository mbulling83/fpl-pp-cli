package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"text/tabwriter"

	"github.com/mbulling83/fpl-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

type consistencyRow struct {
	PlayerID    int     `json:"player_id"`
	Name        string  `json:"name"`
	Team        string  `json:"team"`
	GWsPlayed   int     `json:"gws_played"`
	AvgPoints   float64 `json:"avg_points"`
	StdDev      float64 `json:"std_dev"`
	HaulRate    float64 `json:"haul_rate"`
	BlankRate   float64 `json:"blank_rate"`
	ConsistIdx  float64 `json:"consistency_index"`
}

func newConsistencyCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var entryID string
	var playerID string
	var minGWs int

	cmd := &cobra.Command{
		Use:   "consistency",
		Short: "Player consistency index: variance, haul rate, blank rate across the season",
		Long: `Computes consistency metrics for players using full season gameweek data.
Haul = 9+ points. Blank = 2 or fewer points. Consistency Index = avg_pts / (1 + std_dev).
Use --entry to limit to your squad, or --player for a single player.`,
		Example: `  fpl-pp-cli consistency
  fpl-pp-cli consistency --entry 12345
  fpl-pp-cli consistency --player 233
  fpl-pp-cli consistency --json | head -20`,
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if dbPath == "" {
				dbPath = defaultDBPath("fpl-pp-cli")
			}
			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening local database: %w\nRun 'fpl-pp-cli sync' first.", err)
			}
			defer db.Close()

			// Load bootstrap
			bsRaw, err := db.Get("bootstrap_static", "bootstrap_static")
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

			// If --entry, collect squad player IDs from all picks
			squadIDs := make(map[int]bool)
			if entryID != "" {
				picksRows, err := db.DB().QueryContext(cmd.Context(),
					`SELECT data FROM entry_event WHERE entry_id=?`, entryID)
				if err == nil {
					defer picksRows.Close()
					for picksRows.Next() {
						var raw json.RawMessage
						if err := picksRows.Scan(&raw); err != nil {
							continue
						}
						var ev map[string]json.RawMessage
						if err := json.Unmarshal(raw, &ev); err != nil {
							continue
						}
						var picks []map[string]any
						if err := json.Unmarshal(ev["picks"], &picks); err != nil {
							continue
						}
						for _, p := range picks {
							if eid, ok := p["element"].(float64); ok {
								squadIDs[int(eid)] = true
							}
						}
					}
				}
			}

			// Build GW history from element-summary resources
			type playerGW struct {
				points float64
			}
			playerHistory := make(map[int][]float64) // element_id -> []points per GW

			esRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT id, data FROM resources WHERE resource_type='element_summary'`)
			if err == nil {
				defer esRows.Close()
				for esRows.Next() {
					var rid string
					var raw json.RawMessage
					if err := esRows.Scan(&rid, &raw); err != nil {
						continue
					}
					var summary map[string]json.RawMessage
					if err := json.Unmarshal(raw, &summary); err != nil {
						continue
					}
					var history []map[string]any
					if err := json.Unmarshal(summary["history"], &history); err != nil {
						continue
					}
					for _, h := range history {
						eid, _ := h["element"].(float64)
						pts, _ := h["total_points"].(float64)
						playerHistory[int(eid)] = append(playerHistory[int(eid)], pts)
					}
				}
			}

			// Also pull from live data as fallback
			liveRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT event_id, data FROM live`)
			if err == nil {
				defer liveRows.Close()
				livePts := make(map[int]map[int]float64) // element -> gw -> pts
				for liveRows.Next() {
					var gwStr string
					var raw json.RawMessage
					if err := liveRows.Scan(&gwStr, &raw); err != nil {
						continue
					}
					var liveData map[string]json.RawMessage
					if err := json.Unmarshal(raw, &liveData); err != nil {
						continue
					}
					var elemsRaw []map[string]any
					if err := json.Unmarshal(liveData["elements"], &elemsRaw); err != nil {
						continue
					}
					gw := 0
					fmt.Sscanf(gwStr, "%d", &gw)
					for _, el := range elemsRaw {
						if eid, ok := el["id"].(float64); ok {
							if stats, ok := el["stats"].(map[string]any); ok {
								if pts, ok := stats["total_points"].(float64); ok {
									if _, ok := livePts[int(eid)]; !ok {
										livePts[int(eid)] = make(map[int]float64)
									}
									livePts[int(eid)][gw] = pts
								}
							}
						}
					}
				}
				for eid, gwMap := range livePts {
					if len(playerHistory[eid]) == 0 {
						for _, pts := range gwMap {
							playerHistory[eid] = append(playerHistory[eid], pts)
						}
					}
				}
			}

			stdDev := func(pts []float64) float64 {
				if len(pts) < 2 {
					return 0
				}
				var sum float64
				for _, p := range pts {
					sum += p
				}
				mean := sum / float64(len(pts))
				var variance float64
				for _, p := range pts {
					diff := p - mean
					variance += diff * diff
				}
				return math.Sqrt(variance / float64(len(pts)-1))
			}

			filterID := 0
			if playerID != "" {
				filterID, _ = strconv.Atoi(playerID)
			}

			var result []consistencyRow
			for _, el := range elements {
				eid, _ := el["id"].(float64)
				id := int(eid)
				if filterID > 0 && id != filterID {
					continue
				}
				if entryID != "" && !squadIDs[id] {
					continue
				}
				pts := playerHistory[id]
				if len(pts) < minGWs {
					continue
				}
				var sum float64
				hauls, blanks := 0, 0
				for _, p := range pts {
					sum += p
					if p >= 9 {
						hauls++
					}
					if p <= 2 {
						blanks++
					}
				}
				avg := sum / float64(len(pts))
				sd := stdDev(pts)
				consistIdx := avg / (1 + sd)
				teamID, _ := el["team"].(float64)
				name, _ := el["web_name"].(string)
				result = append(result, consistencyRow{
					PlayerID:   id,
					Name:       name,
					Team:       teamByID[int(teamID)],
					GWsPlayed:  len(pts),
					AvgPoints:  avg,
					StdDev:     sd,
					HaulRate:   float64(hauls) / float64(len(pts)),
					BlankRate:  float64(blanks) / float64(len(pts)),
					ConsistIdx: consistIdx,
				})
			}

			sort.Slice(result, func(i, j int) bool {
				return result[i].ConsistIdx > result[j].ConsistIdx
			})

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PLAYER\tTEAM\tGWs\tAVG\tSTD_DEV\tHAUL%\tBLANK%\tCONSIST_IDX")
			for _, r := range result {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%.1f\t%.1f\t%.0f%%\t%.0f%%\t%.2f\n",
					r.Name, r.Team, r.GWsPlayed, r.AvgPoints, r.StdDev,
					r.HaulRate*100, r.BlankRate*100, r.ConsistIdx)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().StringVar(&entryID, "entry", "", "Limit to squad members for this entry ID")
	cmd.Flags().StringVar(&playerID, "player", "", "Show single player by element ID")
	cmd.Flags().IntVar(&minGWs, "min-gws", 5, "Minimum gameweeks played to include")
	return cmd
}
