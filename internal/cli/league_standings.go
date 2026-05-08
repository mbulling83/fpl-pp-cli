package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mbulling83/fpl-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

type leagueStandingsRow struct {
	Rank       int    `json:"rank"`
	EntryID    int    `json:"entry_id"`
	EntryName  string `json:"entry_name"`
	Manager    string `json:"manager"`
	TotalPts   int    `json:"total_points"`
	GWPts      int    `json:"gw_points"`
	GapToFirst int    `json:"gap_to_first"`
	GapToAbove int    `json:"gap_to_above"`
}

func newLeagueStandingsCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var live bool

	cmd := &cobra.Command{
		Use:   "league-standings <league-id>",
		Short: "Mini-league standings table with points gaps",
		Long: `Shows the full classic league standings with gap-to-first and gap-to-above columns.
Run 'fpl-pp-cli league sync <league-id>' first to cache data locally, or use --live to fetch direct.`,
		Example: `  fpl-pp-cli league-standings 665797
  fpl-pp-cli league-standings 665797 --live
  fpl-pp-cli league-standings 665797 --json`,
		Annotations: map[string]string{"mcp:read-only": "true"},
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			leagueID := args[0]

			var results []map[string]any

			if !live {
				// Try local DB first
				if dbPath == "" {
					dbPath = defaultDBPath("fpl-pp-cli")
				}
				db, err := store.OpenWithContext(cmd.Context(), dbPath)
				if err == nil {
					defer db.Close()
					// Try resources table first (league sync stores there)
					var raw sqliteJSON
					if err := db.DB().QueryRowContext(cmd.Context(),
						`SELECT data FROM resources WHERE resource_type='leagues_classic_standings' AND id=?`, leagueID,
					).Scan(&raw); err == nil && raw.v != nil {
						// This is the full results array
						if jsonErr := json.Unmarshal(raw.v, &results); jsonErr != nil {
							// Might be the raw API response — unwrap it
							var apiResp map[string]json.RawMessage
							if jsonErr2 := json.Unmarshal(raw.v, &apiResp); jsonErr2 == nil {
								if sd, ok := apiResp["standings"]; ok {
									var standingsData map[string]json.RawMessage
									if json.Unmarshal(sd, &standingsData) == nil {
										json.Unmarshal(standingsData["results"], &results)
									}
								}
							}
						}
					}
				}
			}

			// Fall back to live fetch
			if len(results) == 0 {
				standingsRaw, err := fplGet(cmd.Context(), fmt.Sprintf("/leagues-classic/%s/standings/", leagueID))
				if err != nil {
					return fmt.Errorf("fetching league standings: %w", err)
				}
				var standingsResp map[string]json.RawMessage
				if err := json.Unmarshal(standingsRaw, &standingsResp); err != nil {
					return fmt.Errorf("parsing standings: %w", err)
				}
				leagueName := ""
				if ln, ok := standingsResp["league"]; ok {
					var lMap map[string]any
					if json.Unmarshal(ln, &lMap) == nil {
						leagueName, _ = lMap["name"].(string)
					}
				}
				_ = leagueName
				var standingsData map[string]json.RawMessage
				if err := json.Unmarshal(standingsResp["standings"], &standingsData); err != nil {
					return fmt.Errorf("parsing standings wrapper: %w", err)
				}
				if err := json.Unmarshal(standingsData["results"], &results); err != nil {
					return fmt.Errorf("parsing results: %w", err)
				}
			}

			if len(results) == 0 {
				return fmt.Errorf("no standings found for league %s — run 'fpl-pp-cli league sync %s' first", leagueID, leagueID)
			}

			// Build output rows
			topPts := int(results[0]["total"].(float64))
			var rows []leagueStandingsRow
			prevPts := topPts
			for _, r := range results {
				rank := int(r["rank"].(float64))
				total := int(r["total"].(float64))
				gw := 0
				if gwv, ok := r["event_total"].(float64); ok {
					gw = int(gwv)
				}
				entryID := 0
				if eid, ok := r["entry"].(float64); ok {
					entryID = int(eid)
				}
				rows = append(rows, leagueStandingsRow{
					Rank:       rank,
					EntryID:    entryID,
					EntryName:  strVal(r, "entry_name"),
					Manager:    strVal(r, "player_name"),
					TotalPts:   total,
					GWPts:      gw,
					GapToFirst: total - topPts,
					GapToAbove: total - prevPts,
				})
				prevPts = total
			}

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "League %s standings\n\n", leagueID)
			fmt.Fprintln(tw, "RANK\tMANAGER\tTEAM\tGW\tTOTAL\tGAP_1ST\tGAP_ABOVE")
			for _, r := range rows {
				gap1 := fmt.Sprintf("%+d", r.GapToFirst)
				gapA := fmt.Sprintf("%+d", r.GapToAbove)
				if r.GapToAbove == 0 {
					gapA = "—"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%d\t%s\t%s\n",
					r.Rank, r.Manager, r.EntryName, r.GWPts, r.TotalPts, gap1, gapA)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().BoolVar(&live, "live", false, "Fetch live from FPL API instead of local cache")
	return cmd
}

func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
