package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/mbulling83/fpl-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

type replayGWRow struct {
	GW          int     `json:"gw"`
	SquadPts    float64 `json:"squad_total_pts"`
	StartingXI  float64 `json:"starting_xi_pts"`
	BenchPts    float64 `json:"bench_pts"`
	Captain     string  `json:"captain"`
	CaptainPts  float64 `json:"captain_pts"`
}

type replaySummary struct {
	SquadIDs       []int         `json:"squad_ids"`
	CaptainStrategy string       `json:"captain_strategy"`
	TotalPts       float64       `json:"total_points"`
	GWAvgPts       float64       `json:"gw_avg_pts"`
	GWs            []replayGWRow `json:"gameweeks"`
}

func newReplayCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var squadStr string
	var captainStrategy string

	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Simulate a hypothetical squad's full season using stored GW data",
		Long: `Given a comma-separated list of player element IDs, simulate how that squad
would have scored across all completed gameweeks. Captain strategies:
  top-scorer  — captain the player with most GW points (hindsight optimal)
  first       — captain the first player in your list (fixed captain)`,
		Example: `  fpl-pp-cli replay --squad 233,302,427,14,350
  fpl-pp-cli replay --squad 233,302,427 --captain-strategy top-scorer
  fpl-pp-cli replay --squad 233,302,427 --json`,
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if dbPath == "" {
				dbPath = defaultDBPath("fpl-pp-cli")
			}
			if squadStr == "" {
				return fmt.Errorf("--squad is required: comma-separated player element IDs")
			}

			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening local database: %w\nRun 'fpl-pp-cli sync' first.", err)
			}
			defer db.Close()

			// Parse squad IDs
			var squadIDs []int
			for _, s := range strings.Split(squadStr, ",") {
				s = strings.TrimSpace(s)
				id, err := strconv.Atoi(s)
				if err != nil {
					return fmt.Errorf("invalid player ID %q: %w", s, err)
				}
				squadIDs = append(squadIDs, id)
			}

			// Bootstrap for player names
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
			var events []map[string]any
			if err := json.Unmarshal(bs["events"], &events); err != nil {
				return fmt.Errorf("parsing events: %w", err)
			}
			playerByID := make(map[int]string, len(elements))
			for _, e := range elements {
				if id, ok := e["id"].(float64); ok {
					if name, ok := e["web_name"].(string); ok {
						playerByID[int(id)] = name
					}
				}
			}

			// Collect finished GWs
			var finishedGWs []int
			for _, ev := range events {
				if fin, ok := ev["finished"].(bool); ok && fin {
					if id, ok := ev["id"].(float64); ok {
						finishedGWs = append(finishedGWs, int(id))
					}
				}
			}
			sort.Ints(finishedGWs)

			// Load live data for each GW
			gwLive := make(map[int]map[int]float64)
			liveRows, err := db.DB().QueryContext(cmd.Context(), `SELECT event_id, data FROM live`)
			if err == nil {
				defer liveRows.Close()
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
			}

			var gwRows []replayGWRow
			var totalPts float64
			for _, gw := range finishedGWs {
				live, ok := gwLive[gw]
				if !ok {
					continue
				}

				// Determine starting XI (top 11 by points if >11 players, else all)
				type playerScore struct {
					ID   int
					Pts  float64
					Name string
				}
				var scores []playerScore
				for _, id := range squadIDs {
					scores = append(scores, playerScore{id, live[id], playerByID[id]})
				}
				sort.Slice(scores, func(i, j int) bool { return scores[i].Pts > scores[j].Pts })

				startSize := len(scores)
				if startSize > 11 {
					startSize = 11
				}

				var xi, bench []playerScore
				xi = scores[:startSize]
				if len(scores) > startSize {
					bench = scores[startSize:]
				}

				var xiPts, benchPts float64
				for _, p := range xi {
					xiPts += p.Pts
				}
				for _, p := range bench {
					benchPts += p.Pts
				}

				// Captain pick
				capName := ""
				capPts := 0.0
				if len(xi) > 0 {
					switch captainStrategy {
					case "top-scorer":
						capName = xi[0].Name
						capPts = xi[0].Pts
						xiPts += capPts // captain bonus
					default: // "first"
						for _, sc := range xi {
							if sc.ID == squadIDs[0] {
								capName = sc.Name
								capPts = sc.Pts
								xiPts += capPts
								break
							}
						}
						if capName == "" {
							capName = xi[0].Name
							capPts = xi[0].Pts
							xiPts += capPts
						}
					}
				}

				gwRows = append(gwRows, replayGWRow{
					GW:         gw,
					SquadPts:   xiPts + benchPts,
					StartingXI: xiPts,
					BenchPts:   benchPts,
					Captain:    capName,
					CaptainPts: capPts,
				})
				totalPts += xiPts
			}

			avgPts := 0.0
			if len(gwRows) > 0 {
				avgPts = totalPts / float64(len(gwRows))
			}
			summary := replaySummary{
				SquadIDs:        squadIDs,
				CaptainStrategy: captainStrategy,
				TotalPts:        totalPts,
				GWAvgPts:        avgPts,
				GWs:             gwRows,
			}

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(summary)
			}

			fmt.Printf("Replay: %d players, captain=%s\n", len(squadIDs), captainStrategy)
			fmt.Printf("Total pts: %.0f  |  GW avg: %.1f  |  GWs simulated: %d\n\n",
				totalPts, avgPts, len(gwRows))
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GW\tXI_PTS\tBENCH_PTS\tCAPTAIN\tCAP_PTS")
			for _, r := range gwRows {
				fmt.Fprintf(tw, "%d\t%.0f\t%.0f\t%s\t%.0f\n",
					r.GW, r.StartingXI, r.BenchPts, r.Captain, r.CaptainPts)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().StringVar(&squadStr, "squad", "", "Comma-separated player element IDs")
	cmd.Flags().StringVar(&captainStrategy, "captain-strategy", "top-scorer", "Captain selection: top-scorer or first")
	return cmd
}
