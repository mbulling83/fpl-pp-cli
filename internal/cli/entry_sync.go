package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/mbulling83/fpl-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

const fplAPIBase = "https://fantasy.premierleague.com/api"

func fplGet(ctx context.Context, path string) (json.RawMessage, error) {
	url := fplAPIBase + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "fpl-pp-cli/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	return json.RawMessage(body), nil
}

func newEntrySyncCmd(flags *rootFlags) *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "sync <entry-id>",
		Short: "Sync your personal GW history, picks, transfers, and live data to SQLite",
		Long: `Fetches your entry history, all GW picks, transfers, and live GW data from the
FPL API and stores them locally. Required before using captain-audit, bench-regret,
transfer-roi, rank-velocity, chip-roi, value-trajectory, deadweight, and fixture-planner.`,
		Example: `  fpl-pp-cli entry sync 1263296`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entryID := args[0]
			if dbPath == "" {
				dbPath = defaultDBPath("fpl-pp-cli")
			}
			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer db.Close()

			// Determine completed GWs from bootstrap-static
			var bsStr string
			if err := db.DB().QueryRowContext(cmd.Context(),
				`SELECT data FROM resources WHERE resource_type='bootstrap-static' AND id='bootstrap-static'`,
			).Scan(&bsStr); err != nil {
				return fmt.Errorf("bootstrap-static not found locally — run 'fpl-pp-cli sync' first: %w", err)
			}
			bsRaw := json.RawMessage(bsStr)
			var bs map[string]json.RawMessage
			if err := json.Unmarshal(bsRaw, &bs); err != nil {
				return fmt.Errorf("parsing bootstrap: %w", err)
			}
			var events []map[string]any
			if err := json.Unmarshal(bs["events"], &events); err != nil {
				return fmt.Errorf("parsing events: %w", err)
			}
			var finishedGWs []int
			currentGW := 0
			for _, ev := range events {
				if fin, ok := ev["finished"].(bool); ok && fin {
					if id, ok := ev["id"].(float64); ok {
						finishedGWs = append(finishedGWs, int(id))
						if int(id) > currentGW {
							currentGW = int(id)
						}
					}
				}
			}
			fmt.Fprintf(os.Stderr, "Syncing entry %s (GW1–%d)...\n", entryID, currentGW)

			upsert := func(table, id, entryIDVal string, data json.RawMessage) error {
				_, err := db.DB().ExecContext(cmd.Context(),
					`INSERT INTO `+table+` (id, entry_id, data, synced_at)
					 VALUES (?, ?, ?, ?)
					 ON CONFLICT(id) DO UPDATE SET entry_id=excluded.entry_id, data=excluded.data, synced_at=excluded.synced_at`,
					id, entryIDVal, string(data), time.Now(),
				)
				return err
			}

			upsertLive := func(gwStr string, data json.RawMessage) error {
				_, err := db.DB().ExecContext(cmd.Context(),
					`INSERT INTO live (id, event_id, data, synced_at)
					 VALUES (?, ?, ?, ?)
					 ON CONFLICT(id) DO UPDATE SET event_id=excluded.event_id, data=excluded.data, synced_at=excluded.synced_at`,
					gwStr, gwStr, string(data), time.Now(),
				)
				return err
			}

			// 1. History
			fmt.Fprintf(os.Stderr, "  → history... ")
			histData, err := fplGet(cmd.Context(), fmt.Sprintf("/entry/%s/history/", entryID))
			if err != nil {
				return fmt.Errorf("fetching history: %w", err)
			}
			if err := upsert("history", entryID, entryID, histData); err != nil {
				return fmt.Errorf("storing history: %w", err)
			}
			fmt.Fprintln(os.Stderr, "ok")

			// 2. Transfers
			fmt.Fprintf(os.Stderr, "  → transfers... ")
			txData, err := fplGet(cmd.Context(), fmt.Sprintf("/entry/%s/transfers/", entryID))
			if err != nil {
				return fmt.Errorf("fetching transfers: %w", err)
			}
			if err := upsert("transfers", entryID, entryID, txData); err != nil {
				return fmt.Errorf("storing transfers: %w", err)
			}
			fmt.Fprintln(os.Stderr, "ok")

			// 3. GW picks + live data
			client := &http.Client{Timeout: 15 * time.Second}
			_ = client
			for _, gw := range finishedGWs {
				gwStr := fmt.Sprintf("%d", gw)

				// Picks
				fmt.Fprintf(os.Stderr, "  → GW%d picks... ", gw)
				picksData, err := fplGet(cmd.Context(), fmt.Sprintf("/entry/%s/event/%d/picks/", entryID, gw))
				if err != nil {
					fmt.Fprintf(os.Stderr, "skip (%v)\n", err)
				} else {
					id := entryID + ":" + gwStr
					if err := upsert("entry_event", id, entryID, picksData); err != nil {
						fmt.Fprintf(os.Stderr, "warn: %v\n", err)
					} else {
						fmt.Fprintln(os.Stderr, "ok")
					}
				}

				// Live
				fmt.Fprintf(os.Stderr, "  → GW%d live... ", gw)
				liveData, err := fplGet(cmd.Context(), fmt.Sprintf("/event/%d/live/", gw))
				if err != nil {
					fmt.Fprintf(os.Stderr, "skip (%v)\n", err)
				} else {
					if err := upsertLive(gwStr, liveData); err != nil {
						fmt.Fprintf(os.Stderr, "warn: %v\n", err)
					} else {
						fmt.Fprintln(os.Stderr, "ok")
					}
				}
				// courtesy delay
				time.Sleep(200 * time.Millisecond)
			}

			fmt.Fprintf(os.Stderr, "Entry sync complete. Run 'fpl-pp-cli captain-audit %s' etc. to use transcendence features.\n", entryID)
			return nil
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}
