package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mbulling83/fpl-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

func newLeagueSyncCmd(flags *rootFlags) *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "sync <league-id>",
		Short: "Sync league standings and all managers' squads/history to local DB",
		Long: `Fetches a classic league's standings and syncs each manager's entry history
and current squad picks. Required before using league-rivals and league-season-plan.

Stores data in the same tables as 'entry sync', so rival analysis commands
can work fully offline after this runs.`,
		Example: `  fpl-pp-cli league sync 665797
  fpl-pp-cli league sync 665797 --db ~/fpl.db`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			leagueID := args[0]
			if dbPath == "" {
				dbPath = defaultDBPath("fpl-pp-cli")
			}
			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer db.Close()

			// Determine current GW from bootstrap
			var bsStr string
			if err := db.DB().QueryRowContext(cmd.Context(),
				`SELECT data FROM resources WHERE resource_type='bootstrap-static' AND id='bootstrap-static'`,
			).Scan(&bsStr); err != nil {
				return fmt.Errorf("bootstrap-static not found — run 'fpl-pp-cli sync' first: %w", err)
			}
			var bs map[string]json.RawMessage
			if err := json.Unmarshal(json.RawMessage(bsStr), &bs); err != nil {
				return fmt.Errorf("parsing bootstrap: %w", err)
			}
			var events []map[string]any
			if err := json.Unmarshal(bs["events"], &events); err != nil {
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
			if currentGW == 0 {
				return fmt.Errorf("no finished gameweeks found — sync bootstrap first")
			}

			// Fetch league standings (all pages)
			fmt.Fprintf(cmd.OutOrStdout(), "Syncing league %s standings...\n", leagueID)
			var allResults []json.RawMessage
			page := 1
			for {
				standingsRaw, err := fplGet(cmd.Context(), fmt.Sprintf("/leagues-classic/%s/standings/?page_standings=%d", leagueID, page))
				if err != nil {
					return fmt.Errorf("fetching league standings page %d: %w", page, err)
				}
				var standingsResp map[string]json.RawMessage
				if err := json.Unmarshal(standingsRaw, &standingsResp); err != nil {
					return fmt.Errorf("parsing standings: %w", err)
				}
				var standingsData map[string]json.RawMessage
				if err := json.Unmarshal(standingsResp["standings"], &standingsData); err != nil {
					return fmt.Errorf("parsing standings wrapper: %w", err)
				}
				var results []json.RawMessage
				if err := json.Unmarshal(standingsData["results"], &results); err != nil {
					return fmt.Errorf("parsing results: %w", err)
				}
				allResults = append(allResults, results...)

				// Check has_next
				var hasNext bool
				if hn, ok := standingsData["has_next"]; ok {
					json.Unmarshal(hn, &hasNext)
				}
				if !hasNext || len(results) == 0 {
					break
				}
				page++
				time.Sleep(200 * time.Millisecond)

				// Store last fetched page for reference
				if _, err := db.DB().ExecContext(cmd.Context(),
					`INSERT OR REPLACE INTO resources (resource_type, id, data, synced_at) VALUES ('leagues_classic_standings', ?, ?, CURRENT_TIMESTAMP)`,
					leagueID, string(standingsRaw),
				); err != nil {
					return fmt.Errorf("storing standings: %w", err)
				}
			}

			// Re-store final full standings blob
			finalStandings, _ := json.Marshal(allResults)
			if _, err := db.DB().ExecContext(cmd.Context(),
				`INSERT OR REPLACE INTO resources (resource_type, id, data, synced_at) VALUES ('leagues_classic_standings', ?, ?, CURRENT_TIMESTAMP)`,
				leagueID, string(finalStandings),
			); err != nil {
				return fmt.Errorf("storing final standings: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  → %d managers found\n", len(allResults))

			// Sync each manager's history + current GW picks
			for i, raw := range allResults {
				var entry map[string]any
				if err := json.Unmarshal(raw, &entry); err != nil {
					continue
				}
				entryID := fmt.Sprintf("%.0f", entry["entry"].(float64))
				entryName, _ := entry["entry_name"].(string)
				manager, _ := entry["player_name"].(string)

				fmt.Fprintf(cmd.OutOrStdout(), "  [%d/%d] %s (%s)...", i+1, len(allResults), manager, entryName)

				// Fetch history
				histRaw, err := fplGet(cmd.Context(), fmt.Sprintf("/entry/%s/history/", entryID))
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), " history err: %v\n", err)
					time.Sleep(200 * time.Millisecond)
					continue
				}
				if _, err := db.DB().ExecContext(cmd.Context(),
					`INSERT OR REPLACE INTO history (id, entry_id, data, synced_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`,
					entryID, entryID, string(histRaw),
				); err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), " history store err: %v\n", err)
					continue
				}

				// Fetch current GW picks
				picksRaw, err := fplGet(cmd.Context(), fmt.Sprintf("/entry/%s/event/%d/picks/", entryID, currentGW))
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), " picks err: %v\n", err)
					time.Sleep(200 * time.Millisecond)
					continue
				}
				pickKey := entryID + ":" + fmt.Sprintf("%d", currentGW)
				if _, err := db.DB().ExecContext(cmd.Context(),
					`INSERT OR REPLACE INTO entry_event (id, entry_id, data, synced_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`,
					pickKey, entryID, string(picksRaw),
				); err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), " picks store err: %v\n", err)
					continue
				}

				fmt.Fprintln(cmd.OutOrStdout(), " ok")
				time.Sleep(200 * time.Millisecond)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "League sync complete. Run 'fpl-pp-cli league-rivals %s <your-entry-id>' to analyse.\n", leagueID)
			return nil
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}
