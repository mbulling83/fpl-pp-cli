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

type captainAuditRow struct {
	GW              int     `json:"gw"`
	Captain         string  `json:"captain"`
	CaptainPoints   float64 `json:"captain_points"`
	OptimalCaptain  string  `json:"optimal_captain"`
	OptimalPoints   float64 `json:"optimal_points"`
	Gain            float64 `json:"gain"`
	RunningActual   float64 `json:"running_actual_total"`
	RunningOptimal  float64 `json:"running_optimal_total"`
	RunningDiff     float64 `json:"running_diff"`
}

func newCaptainAuditCmd(flags *rootFlags) *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "captain-audit <entry-id>",
		Short: "Captain counterfactual: what if you'd always captained your top scorer?",
		Long: `For every gameweek, compare the points your captain actually scored (×2)
against the best-scoring player in your squad that week. Shows cumulative
points gap across the season.`,
		Example:     "  fpl-pp-cli captain-audit 12345\n  fpl-pp-cli captain-audit 12345 --json",
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

			// Load bootstrap for player names
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
			playerByID := make(map[int]string, len(elements))
			for _, e := range elements {
				if id, ok := e["id"].(float64); ok {
					if name, ok := e["web_name"].(string); ok {
						playerByID[int(id)] = name
					}
				}
			}

			// Load live data per GW to get actual GW points per player
			rows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT event_id, data FROM live ORDER BY CAST(event_id AS INTEGER)`)
			if err != nil {
				return fmt.Errorf("querying live data: %w", err)
			}
			defer rows.Close()
			gwLive := make(map[int]map[int]float64) // gw -> element_id -> total_points
			for rows.Next() {
				var gwStr string
				var raw sqliteJSON
				if err := rows.Scan(&gwStr, &raw); err != nil {
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
				gw := 0
				fmt.Sscanf(gwStr, "%d", &gw)
				if gw == 0 {
					continue
				}
				gwLive[gw] = make(map[int]float64)
				for _, el := range elemsRaw {
					if eid, ok := el["id"].(float64); ok {
						if stats, ok := el["stats"].(map[string]any); ok {
							if pts, ok := stats["total_points"].(float64); ok {
								gwLive[gw][int(eid)] = pts
							}
						}
					}
				}
			}

			// Load all entry_event picks for this entry
			// id format: "<entryID>:<gw>" stored by 'entry sync'
			picksRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT id, data FROM entry_event WHERE entry_id=? ORDER BY id`,
				entryID)
			if err != nil {
				return fmt.Errorf("querying picks: %w", err)
			}
			defer picksRows.Close()

			type gwPick struct {
				GW    int
				Picks []map[string]any
			}
			var allPicks []gwPick
			for picksRows.Next() {
				var rowID string
				var raw sqliteJSON
				if err := picksRows.Scan(&rowID, &raw); err != nil {
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
				// Parse gw from id "<entryID>:<gw>"
				gwStr := rowID
				if idx := len(entryID) + 1; idx < len(rowID) {
					gwStr = rowID[idx:]
				}
				gw := 0
				fmt.Sscanf(gwStr, "%d", &gw)
				if gw > 0 {
					allPicks = append(allPicks, gwPick{gw, picks})
				}
			}

			sort.Slice(allPicks, func(i, j int) bool { return allPicks[i].GW < allPicks[j].GW })

			var result []captainAuditRow
			var runActual, runOptimal float64
			for _, gp := range allPicks {
				live, ok := gwLive[gp.GW]
				if !ok {
					continue
				}
				var captainID int
				var captainPts float64
				var bestID int
				var bestPts float64
				for _, pick := range gp.Picks {
					pos := int(pick["position"].(float64))
					if pos > 11 {
						continue // bench
					}
					eid := int(pick["element"].(float64))
					isCap, _ := pick["is_captain"].(bool)
					pts := live[eid]
					if isCap {
						captainID = eid
						captainPts = pts * 2 // captain multiplier
					}
					if pts > bestPts {
						bestPts = pts
						bestID = eid
					}
				}
				optPts := bestPts * 2
				gain := optPts - captainPts
				runActual += captainPts
				runOptimal += optPts

				capName := playerByID[captainID]
				if capName == "" {
					capName = fmt.Sprintf("id_%d", captainID)
				}
				optName := playerByID[bestID]
				if optName == "" {
					optName = fmt.Sprintf("id_%d", bestID)
				}
				result = append(result, captainAuditRow{
					GW:             gp.GW,
					Captain:        capName,
					CaptainPoints:  captainPts,
					OptimalCaptain: optName,
					OptimalPoints:  optPts,
					Gain:           gain,
					RunningActual:  runActual,
					RunningOptimal: runOptimal,
					RunningDiff:    runOptimal - runActual,
				})
			}

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GW\tCAPTAIN\tPTS\tOPTIMAL\tOPT_PTS\tGAIN\tRUN_DIFF")
			for _, r := range result {
				fmt.Fprintf(tw, "%d\t%s\t%.0f\t%s\t%.0f\t%+.0f\t%+.0f\n",
					r.GW, r.Captain, r.CaptainPoints, r.OptimalCaptain, r.OptimalPoints, r.Gain, r.RunningDiff)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}
