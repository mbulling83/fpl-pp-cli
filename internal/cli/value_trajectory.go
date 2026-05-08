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

type valueTrajectoryRow struct {
	GW          int     `json:"gw"`
	Value       float64 `json:"team_value"`
	Bank        float64 `json:"bank"`
	TotalValue  float64 `json:"total_value"`
	Chip        string  `json:"chip_played"`
	Transfers   int     `json:"transfers"`
	TransferHit int     `json:"transfer_cost"`
}

func newValueTrajectoryCmd(flags *rootFlags) *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "value-trajectory <entry-id>",
		Short: "Team value and bank trajectory across the season with transfer annotations",
		Long: `Shows how your team value and in-the-bank money evolved each gameweek.
Transfers and chips are annotated as inflection points on the value chart.`,
		Example: `  fpl-pp-cli value-trajectory 12345
  fpl-pp-cli value-trajectory 12345 --json`,
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

			var histRaw sqliteJSON
			if err = db.DB().QueryRowContext(cmd.Context(), `SELECT data FROM history WHERE id=?`, entryID).Scan(&histRaw); err != nil {
				return fmt.Errorf("history not found for entry %s. Run 'fpl-pp-cli entry sync %s' first: %w", entryID, entryID, err)
			}
			var hist map[string]json.RawMessage
			if err := json.Unmarshal(histRaw.v, &hist); err != nil {
				return fmt.Errorf("parsing history: %w", err)
			}
			var current []map[string]any
			if err := json.Unmarshal(hist["current"], &current); err != nil {
				return fmt.Errorf("parsing current history: %w", err)
			}
			var chips []map[string]any
			if err := json.Unmarshal(hist["chips"], &chips); err != nil {
				chips = nil
			}

			chipByGW := make(map[int]string)
			for _, c := range chips {
				gw, _ := c["event"].(float64)
				name, _ := c["name"].(string)
				chipByGW[int(gw)] = name
			}

			sort.Slice(current, func(i, j int) bool {
				gi, _ := current[i]["event"].(float64)
				gj, _ := current[j]["event"].(float64)
				return gi < gj
			})

			var result []valueTrajectoryRow
			for _, gw := range current {
				gwNum, _ := gw["event"].(float64)
				value, _ := gw["value"].(float64)
				bank, _ := gw["bank"].(float64)
				transfers, _ := gw["event_transfers"].(float64)
				transferCost, _ := gw["event_transfers_cost"].(float64)
				result = append(result, valueTrajectoryRow{
					GW:          int(gwNum),
					Value:       value / 10,
					Bank:        bank / 10,
					TotalValue:  (value + bank) / 10,
					Chip:        chipByGW[int(gwNum)],
					Transfers:   int(transfers),
					TransferHit: int(transferCost),
				})
			}

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GW\tTEAM_VAL\tBANK\tTOTAL\tTX\tHIT\tCHIP")
			for _, r := range result {
				chipStr := ""
				if r.Chip != "" {
					chipStr = r.Chip
				}
				txStr := fmt.Sprintf("%d", r.Transfers)
				hitStr := ""
				if r.TransferHit > 0 {
					hitStr = fmt.Sprintf("-%d", r.TransferHit)
				}
				fmt.Fprintf(tw, "%d\t£%.1f\t£%.1f\t£%.1f\t%s\t%s\t%s\n",
					r.GW, r.Value, r.Bank, r.TotalValue, txStr, hitStr, chipStr)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}
