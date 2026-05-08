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

type transferROIRow struct {
	GW          int     `json:"gw"`
	PlayerIn    string  `json:"player_in"`
	PlayerOut   string  `json:"player_out"`
	CostHit     int     `json:"cost_hit"`
	PointsIn    float64 `json:"points_in_after_transfer"`
	PointsOut   float64 `json:"points_out_after_transfer"`
	NetROI      float64 `json:"net_roi"`
}

func newTransferRoiCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var entryID string

	cmd := &cobra.Command{
		Use:   "transfer-roi <entry-id>",
		Short: "Audit every transfer: points gained vs lost vs cost hit",
		Long: `Compare points earned by the player bought vs the player sold, for every GW
after each transfer was made. Requires synced history, transfers, and bootstrap-static.`,
		Example:     "  fpl-pp-cli transfer-roi 12345\n  fpl-pp-cli transfer-roi 12345 --json",
		Annotations: map[string]string{"mcp:read-only": "true"},
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entryID = args[0]
			if dbPath == "" {
				dbPath = defaultDBPath("fpl-pp-cli")
			}
			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening local database: %w\nRun 'fpl-pp-cli sync' first.", err)
			}
			defer db.Close()

			// Load bootstrap-static for player name lookup and per-GW points
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
			playerByID := make(map[int]map[string]any, len(elements))
			for _, e := range elements {
				if id, ok := e["id"].(float64); ok {
					playerByID[int(id)] = e
				}
			}

			// Load transfers
			var txRaw sqliteJSON
			if err = db.DB().QueryRowContext(cmd.Context(), `SELECT data FROM transfers WHERE id=?`, entryID).Scan(&txRaw); err != nil {
				return fmt.Errorf("transfers not found for entry %s. Run 'fpl-pp-cli entry sync %s' first: %w", entryID, entryID, err)
			}
			var transfers []map[string]any
			if err := json.Unmarshal(txRaw.v, &transfers); err != nil {
				return fmt.Errorf("parsing transfers: %w", err)
			}

			// Build per-GW points from live table (gw -> element_id -> points)
			gwPoints := make(map[int]map[int]float64) // element_id -> gw -> total_points
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
					if gw == 0 {
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
						eid, _ := el["id"].(float64)
						if stats, ok := el["stats"].(map[string]any); ok {
							pts, _ := stats["total_points"].(float64)
							if _, ok := gwPoints[int(eid)]; !ok {
								gwPoints[int(eid)] = make(map[int]float64)
							}
							gwPoints[int(eid)][gw] = pts
						}
					}
				}
			}

			playerName := func(id int) string {
				if p, ok := playerByID[id]; ok {
					if n, ok := p["web_name"].(string); ok {
						return n
					}
				}
				return fmt.Sprintf("player_%d", id)
			}

			sumAfter := func(elementID, fromGW int) float64 {
				pts, ok := gwPoints[elementID]
				if !ok {
					return 0
				}
				var total float64
				for gw, p := range pts {
					if gw >= fromGW {
						total += p
					}
				}
				return total
			}

			var result []transferROIRow
			for _, tx := range transfers {
				inID := int(tx["element_in"].(float64))
				outID := int(tx["element_out"].(float64))
				gw := int(tx["event"].(float64))
				cost := 0
				if c, ok := tx["event_transfers_cost"].(float64); ok {
					cost = int(c)
				}
				ptsIn := sumAfter(inID, gw)
				ptsOut := sumAfter(outID, gw)
				result = append(result, transferROIRow{
					GW:       gw,
					PlayerIn: playerName(inID),
					PlayerOut: playerName(outID),
					CostHit:  cost,
					PointsIn: ptsIn,
					PointsOut: ptsOut,
					NetROI:   ptsIn - ptsOut - float64(cost),
				})
			}

			sort.Slice(result, func(i, j int) bool { return result[i].GW < result[j].GW })

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GW\tIN\tOUT\tHIT\tPTS_IN\tPTS_OUT\tNET_ROI")
			for _, r := range result {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%.0f\t%.0f\t%+.0f\n",
					r.GW, r.PlayerIn, r.PlayerOut, r.CostHit, r.PointsIn, r.PointsOut, r.NetROI)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}
