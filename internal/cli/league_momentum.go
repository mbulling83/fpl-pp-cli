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

type momentumRow struct {
	EntryID     string  `json:"entry_id"`
	EntryName   string  `json:"entry_name"`
	Manager     string  `json:"manager"`
	AvgPtsLast  float64 `json:"avg_pts_last_n_gws"`
	TotalPts    int     `json:"total_pts"`
	Rank        int     `json:"current_rank"`
	RankChange  int     `json:"rank_change_last_n_gws"`
	MomentumIdx float64 `json:"momentum_index"`
}

func newLeagueMomentumCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var gwWindow int

	cmd := &cobra.Command{
		Use:   "league-momentum <league-id>",
		Short: "Mini-league momentum: who is rising or falling in the last N gameweeks",
		Long: `Ranks all managers in a classic league by recent form — average points over the
last N gameweeks. Requires synced classic league standings and entry history.`,
		Example: `  fpl-pp-cli league-momentum 314
  fpl-pp-cli league-momentum 314 --gws 5
  fpl-pp-cli league-momentum 314 --json`,
		Annotations: map[string]string{"mcp:read-only": "true"},
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			leagueID := args[0]
			if dbPath == "" {
				dbPath = defaultDBPath("fpl-pp-cli")
			}
			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening local database: %w\nRun 'fpl-pp-cli sync' first.", err)
			}
			defer db.Close()

			// Load league standings
			lsRaw, err := db.Get("leagues_classic_standings", leagueID)
			if err != nil {
				return fmt.Errorf("league %s not found locally. Run 'fpl-pp-cli sync' first: %w", leagueID, err)
			}
			var ls map[string]json.RawMessage
			if err := json.Unmarshal(lsRaw, &ls); err != nil {
				return fmt.Errorf("parsing league standings: %w", err)
			}
			var standingsData map[string]json.RawMessage
			if err := json.Unmarshal(ls["standings"], &standingsData); err != nil {
				return fmt.Errorf("parsing standings wrapper: %w", err)
			}
			var standings []map[string]any
			if err := json.Unmarshal(standingsData["results"], &standings); err != nil {
				return fmt.Errorf("parsing results: %w", err)
			}

			// Find current GW for window calc
			bsRaw, err := db.Get("bootstrap-static", "bootstrap-static")
			if err != nil {
				return fmt.Errorf("bootstrap_static not found: %w", err)
			}
			var bsMap map[string]json.RawMessage
			if err := json.Unmarshal(bsRaw, &bsMap); err != nil {
				return fmt.Errorf("parsing bootstrap: %w", err)
			}
			var events []map[string]any
			if err := json.Unmarshal(bsMap["events"], &events); err != nil {
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

			var result []momentumRow
			for _, s := range standings {
				entryID := fmt.Sprintf("%.0f", s["entry"].(float64))
				entryName, _ := s["entry_name"].(string)
				manager := fmt.Sprintf("%s %s", s["player_name"], "")
				if fn, ok := s["player_name"].(string); ok {
					manager = fn
				}
				rank := int(s["rank"].(float64))
				totalPts := int(s["total"].(float64))

				// Load entry history from domain table
				var histRaw sqliteJSON
				if err := db.DB().QueryRowContext(cmd.Context(),
					`SELECT data FROM history WHERE id=?`, entryID,
				).Scan(&histRaw); err != nil {
					continue
				}
				var hist map[string]json.RawMessage
				if err := json.Unmarshal(histRaw.v, &hist); err != nil {
					continue
				}
				var current []map[string]any
				if err := json.Unmarshal(hist["current"], &current); err != nil {
					continue
				}

				var recentPts []float64
				rankStart, rankEnd := 0, rank
				for _, gw := range current {
					gwNum, _ := gw["event"].(float64)
					pts, _ := gw["points"].(float64)
					gwRank, _ := gw["overall_rank"].(float64)
					if int(gwNum) >= startGW {
						recentPts = append(recentPts, pts)
					}
					if int(gwNum) == startGW {
						rankStart = int(gwRank)
					}
					if int(gwNum) == currentGW {
						rankEnd = int(gwRank)
					}
				}

				var avgPts float64
				for _, p := range recentPts {
					avgPts += p
				}
				if len(recentPts) > 0 {
					avgPts /= float64(len(recentPts))
				}

				rankChange := rankStart - rankEnd // positive = rising

				result = append(result, momentumRow{
					EntryID:     entryID,
					EntryName:   entryName,
					Manager:     manager,
					AvgPtsLast:  avgPts,
					TotalPts:    totalPts,
					Rank:        rank,
					RankChange:  rankChange,
					MomentumIdx: avgPts + float64(rankChange)/100,
				})
			}

			sort.Slice(result, func(i, j int) bool {
				return result[i].MomentumIdx > result[j].MomentumIdx
			})

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "Momentum over last %d GWs — League %s\n", gwWindow, leagueID)
			fmt.Fprintln(tw, "\nMANAGER\tTEAM\tRANK\tAVG_PTS\tRANK_CHG\tMOMENTUM")
			for _, r := range result {
				rankChg := fmt.Sprintf("%+d", r.RankChange)
				fmt.Fprintf(tw, "%s\t%s\t%d\t%.1f\t%s\t%.2f\n",
					r.Manager, r.EntryName, r.Rank, r.AvgPtsLast, rankChg, r.MomentumIdx)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().IntVar(&gwWindow, "gws", 5, "Number of recent gameweeks for momentum")
	return cmd
}
