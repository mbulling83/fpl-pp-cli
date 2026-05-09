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
			elementTypeByID := make(map[int]int, len(elements))
			for _, e := range elements {
				if id, ok := e["id"].(float64); ok {
					if name, ok := e["web_name"].(string); ok {
						playerByID[int(id)] = name
					}
					if et, ok := e["element_type"].(float64); ok {
						elementTypeByID[int(id)] = int(et)
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
				var raw sqliteJSON
				if err := liveRows.Scan(&gwStr, &raw); err != nil {
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

			// Load picks (id format: "<entryID>:<gw>")
			picksRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT id, data FROM entry_event WHERE entry_id=? ORDER BY id`,
				entryID)
			if err != nil {
				return fmt.Errorf("querying picks: %w", err)
			}
			defer picksRows.Close()

			var result []benchRegretRow
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
				gwStr := rowID
				if idx := len(entryID) + 1; idx < len(rowID) {
					gwStr = rowID[idx:]
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

				// Separate starters and bench by position type.
				// Bench GK (pos 12) only competes with the starting GK.
				// Outfield bench (pos 13-15) are paired in priority order against
				// the worst outfield starters in points order — this prevents each
				// bench player from being compared against the same floor.
				var starterGKPts float64
				var outfieldStarterPts []float64
				var benchGKID int
				var benchGKPts float64
				var benchGKPos int
				type benchSlot struct{ ID, Pos int; Pts float64 }
				var benchOutfield []benchSlot

				for _, pick := range picks {
					eid := int(pick["element"].(float64))
					pos := int(pick["position"].(float64))
					pts := live[eid]
					isGK := elementTypeByID[eid] == 1
					if pos <= 11 {
						if isGK {
							starterGKPts = pts
						} else {
							outfieldStarterPts = append(outfieldStarterPts, pts)
						}
					} else {
						if isGK || pos == 12 {
							benchGKID, benchGKPts, benchGKPos = eid, pts, pos
						} else {
							benchOutfield = append(benchOutfield, benchSlot{eid, pos, pts})
						}
					}
				}

				// Bench GK vs starting GK
				if benchGKID != 0 && benchGKPts > starterGKPts {
					name := playerByID[benchGKID]
					if name == "" {
						name = fmt.Sprintf("id_%d", benchGKID)
					}
					result = append(result, benchRegretRow{
						GW: gw, BenchPlayer: name, Position: benchGKPos,
						BenchPoints: benchGKPts, StartersMin: starterGKPts,
						Regret: benchGKPts - starterGKPts,
					})
				}

				// Outfield bench: pair in bench-priority order vs worst starters in order
				sort.Float64s(outfieldStarterPts) // ascending: worst first
				sort.Slice(benchOutfield, func(i, j int) bool {
					return benchOutfield[i].Pos < benchOutfield[j].Pos
				})
				for i, b := range benchOutfield {
					if i >= len(outfieldStarterPts) {
						break
					}
					pairedPts := outfieldStarterPts[i]
					if b.Pts > pairedPts {
						name := playerByID[b.ID]
						if name == "" {
							name = fmt.Sprintf("id_%d", b.ID)
						}
						result = append(result, benchRegretRow{
							GW: gw, BenchPlayer: name, Position: b.Pos,
							BenchPoints: b.Pts, StartersMin: pairedPts,
							Regret: b.Pts - pairedPts,
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
