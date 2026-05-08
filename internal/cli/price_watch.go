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

type priceWatchRow struct {
	PlayerID        int     `json:"player_id"`
	Name            string  `json:"name"`
	Team            string  `json:"team"`
	Position        string  `json:"position"`
	NowCost         float64 `json:"now_cost"`
	CostChangeEvent float64 `json:"cost_change_event"`
	CostChangeStart float64 `json:"cost_change_start"`
	TransfersIn     int     `json:"transfers_in_event"`
	TransfersOut    int     `json:"transfers_out_event"`
	NetTransfers    int     `json:"net_transfers_event"`
	InSquad         bool    `json:"in_your_squad"`
	RiskLevel       string  `json:"risk_level"`
}

func newPriceWatchCmd(flags *rootFlags) *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "price-watch <entry-id>",
		Short: "Monitor price change risk for your squad and top risers/fallers",
		Long: `Cross-references your current squad with player transfer velocity to flag
players at risk of price drops and risers you may want to target.`,
		Example: `  fpl-pp-cli price-watch 12345
  fpl-pp-cli price-watch 12345 --json`,
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

			// Bootstrap
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

			// Current squad
			squadIDs := make(map[int]bool)
			var raw sqliteJSON
			// Get most recent picks
			picksRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT data FROM entry_event WHERE entry_id=? ORDER BY CAST(event_id AS INTEGER) DESC LIMIT 1`,
				entryID)
			if err == nil {
				defer picksRows.Close()
				if picksRows.Next() {
					if err := picksRows.Scan(&raw); err == nil {
						var ev map[string]json.RawMessage
						if err := json.Unmarshal(raw.v, &ev); err == nil {
							var picks []map[string]any
							if err := json.Unmarshal(ev["picks"], &picks); err == nil {
								for _, p := range picks {
									if eid, ok := p["element"].(float64); ok {
										squadIDs[int(eid)] = true
									}
								}
							}
						}
					}
				}
			}

			var result []priceWatchRow
			for _, el := range elements {
				eid, _ := el["id"].(float64)
				netTransfers := 0
				tIn, _ := el["transfers_in_event"].(float64)
				tOut, _ := el["transfers_out_event"].(float64)
				netTransfers = int(tIn) - int(tOut)
				costChangeEvent, _ := el["cost_change_event"].(float64)
				costChangeStart, _ := el["cost_change_start"].(float64)

				// Only include meaningful movers + squad players
				inSquad := squadIDs[int(eid)]
				if !inSquad && costChangeEvent == 0 && tIn < 10000 && tOut < 10000 {
					continue
				}

				name, _ := el["web_name"].(string)
				teamID, _ := el["team"].(float64)
				pos, _ := el["element_type"].(float64)
				cost, _ := el["now_cost"].(float64)

				risk := "LOW"
				if inSquad && tOut > tIn*2 {
					risk = "HIGH DROP"
				} else if !inSquad && tIn > tOut*2 {
					risk = "RISING"
				} else if inSquad && costChangeEvent < 0 {
					risk = "DROPPED"
				} else if costChangeEvent > 0 {
					risk = "ROSE"
				}

				result = append(result, priceWatchRow{
					PlayerID:        int(eid),
					Name:            name,
					Team:            teamByID[int(teamID)],
					Position:        posMap[int(pos)],
					NowCost:         cost / 10,
					CostChangeEvent: costChangeEvent / 10,
					CostChangeStart: costChangeStart / 10,
					TransfersIn:     int(tIn),
					TransfersOut:    int(tOut),
					NetTransfers:    netTransfers,
					InSquad:         inSquad,
					RiskLevel:       risk,
				})
			}

			sort.Slice(result, func(i, j int) bool {
				// Squad players first, then by net transfers descending
				if result[i].InSquad != result[j].InSquad {
					return result[i].InSquad
				}
				return result[i].NetTransfers > result[j].NetTransfers
			})

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PLAYER\tTEAM\tPOS\tCOST\tCHG_GW\tCHG_START\tNET_TX\tIN_SQUAD\tRISK")
			for _, r := range result {
				inSquadStr := ""
				if r.InSquad {
					inSquadStr = "✓"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t£%.1f\t%+.1f\t%+.1f\t%+d\t%s\t%s\n",
					r.Name, r.Team, r.Position, r.NowCost, r.CostChangeEvent, r.CostChangeStart,
					r.NetTransfers, inSquadStr, r.RiskLevel)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}
