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

type benchRegretRow struct {
	GW          int     `json:"gw"`
	BenchPlayer string  `json:"bench_player"`
	Position    int     `json:"bench_position"`
	BenchPoints float64 `json:"bench_points"`
	StartersMin float64 `json:"lowest_starter_points"`
	Regret      float64 `json:"regret"`
}

func newBenchRegretCmd(flags *rootFlags) *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "bench-regret <entry-id>",
		Short: "Points left on the bench that should have started",
		Long: `For every gameweek, shows bench players who scored more than your lowest-scoring
starter. Simulates basic auto-sub rules (no injury/red-card awareness).`,
		Example:     "  fpl-pp-cli bench-regret 12345\n  fpl-pp-cli bench-regret 12345 --json",
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

			// Player names from bootstrap
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
			playerByID := make(map[int]string, len(elements))
			for _, e := range elements {
				if id, ok := e["id"].(float64); ok {
					if name, ok := e["web_name"].(string); ok {
						playerByID[int(id)] = name
					}
				}
			}

			// Load live data
			liveRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT event_id, data FROM live ORDER BY CAST(event_id AS INTEGER)`)
			if err != nil {
				return fmt.Errorf("querying live data: %w", err)
			}
			defer liveRows.Close()
			gwLive := make(map[int]map[int]float64)
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

			// Load picks
			picksRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT event_id, data FROM entry_event WHERE entry_id=? ORDER BY CAST(event_id AS INTEGER)`,
				entryID)
			if err != nil {
				return fmt.Errorf("querying picks: %w", err)
			}
			defer picksRows.Close()

			var result []benchRegretRow
			for picksRows.Next() {
				var gwStr string
				var raw json.RawMessage
				if err := picksRows.Scan(&gwStr, &raw); err != nil {
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
				gw := 0
				fmt.Sscanf(gwStr, "%d", &gw)
				if gw == 0 {
					continue
				}
				live, ok := gwLive[gw]
				if !ok {
					continue
				}

				var starterPts []float64
				var benchPts []struct {
					ID  int
					Pos int
					Pts float64
				}
				for _, pick := range picks {
					eid := int(pick["element"].(float64))
					pos := int(pick["position"].(float64))
					pts := live[eid]
					if pos <= 11 {
						starterPts = append(starterPts, pts)
					} else {
						benchPts = append(benchPts, struct {
							ID  int
							Pos int
							Pts float64
						}{eid, pos, pts})
					}
				}
				sort.Float64s(starterPts)
				minStarter := 0.0
				if len(starterPts) > 0 {
					minStarter = starterPts[0]
				}
				for _, b := range benchPts {
					if b.Pts > minStarter {
						name := playerByID[b.ID]
						if name == "" {
							name = fmt.Sprintf("id_%d", b.ID)
						}
						result = append(result, benchRegretRow{
							GW:          gw,
							BenchPlayer: name,
							Position:    b.Pos,
							BenchPoints: b.Pts,
							StartersMin: minStarter,
							Regret:      b.Pts - minStarter,
						})
					}
				}
			}

			sort.Slice(result, func(i, j int) bool {
				if result[i].GW != result[j].GW {
					return result[i].GW < result[j].GW
				}
				return result[i].Regret > result[j].Regret
			})

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			var totalRegret float64
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GW\tBENCH_PLAYER\tBENCH_POS\tBENCH_PTS\tMIN_STARTER\tREGRET")
			for _, r := range result {
				fmt.Fprintf(tw, "%d\t%s\t%d\t%.0f\t%.0f\t%+.0f\n",
					r.GW, r.BenchPlayer, r.Position, r.BenchPoints, r.StartersMin, r.Regret)
				totalRegret += r.Regret
			}
			fmt.Fprintf(tw, "\t\t\t\tTOTAL REGRET\t%+.0f\n", totalRegret)
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}
