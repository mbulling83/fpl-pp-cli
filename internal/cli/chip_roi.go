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

type chipROIRow struct {
	GW         int     `json:"gw"`
	Chip       string  `json:"chip"`
	Points     int     `json:"points"`
	GWAvg      float64 `json:"gw_average"`
	Uplift     float64 `json:"uplift_vs_avg"`
	UpliftPct  float64 `json:"uplift_pct"`
}

func newChipROICmd(flags *rootFlags) *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "chip-roi <entry-id>",
		Short: "How many extra points did each chip earn vs the gameweek average?",
		Long: `Compares your score on each chip gameweek to the GW average score.
Chips: wildcard (WC), triple captain (3xc), bench boost (bboost), free hit (freehit).`,
		Example:     "  fpl-pp-cli chip-roi 12345\n  fpl-pp-cli chip-roi 12345 --json",
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

			// Load entry history for GW points + chips
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
				return fmt.Errorf("parsing chips: %w", err)
			}

			// Build chip GW map
			chipsByGW := make(map[int]string)
			for _, c := range chips {
				gw, _ := c["event"].(float64)
				name, _ := c["name"].(string)
				chipsByGW[int(gw)] = name
			}

			// Load bootstrap for GW averages
			bsRaw, err := db.Get("bootstrap-static", "bootstrap-static")
			if err != nil {
				return fmt.Errorf("bootstrap_static not found: %w", err)
			}
			var bs map[string]json.RawMessage
			if err := json.Unmarshal(bsRaw, &bs); err != nil {
				return fmt.Errorf("parsing bootstrap: %w", err)
			}
			var events []map[string]any
			if err := json.Unmarshal(bs["events"], &events); err != nil {
				return fmt.Errorf("parsing events: %w", err)
			}
			gwAvg := make(map[int]float64)
			for _, ev := range events {
				if id, ok := ev["id"].(float64); ok {
					if avg, ok := ev["average_entry_score"].(float64); ok {
						gwAvg[int(id)] = avg
					}
				}
			}

			gwPts := make(map[int]int)
			for _, gw := range current {
				gwNum, _ := gw["event"].(float64)
				pts, _ := gw["points"].(float64)
				gwPts[int(gwNum)] = int(pts)
			}

			var result []chipROIRow
			for gw, chip := range chipsByGW {
				pts := gwPts[gw]
				avg := gwAvg[gw]
				uplift := float64(pts) - avg
				upliftPct := 0.0
				if avg > 0 {
					upliftPct = (uplift / avg) * 100
				}
				result = append(result, chipROIRow{
					GW:        gw,
					Chip:      chip,
					Points:    pts,
					GWAvg:     avg,
					Uplift:    uplift,
					UpliftPct: upliftPct,
				})
			}

			sort.Slice(result, func(i, j int) bool { return result[i].GW < result[j].GW })

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GW\tCHIP\tYOUR_PTS\tGW_AVG\tUPLIFT\tUPLIFT%")
			for _, r := range result {
				fmt.Fprintf(tw, "%d\t%s\t%d\t%.1f\t%+.1f\t%+.1f%%\n",
					r.GW, r.Chip, r.Points, r.GWAvg, r.Uplift, r.UpliftPct)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}
