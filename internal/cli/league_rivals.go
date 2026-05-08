package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/mbulling83/fpl-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

type rivalRow struct {
	Rank          int      `json:"rank"`
	EntryID       int      `json:"entry_id"`
	Manager       string   `json:"manager"`
	TeamName      string   `json:"team_name"`
	TotalPts      int      `json:"total_pts"`
	GapToYou      int      `json:"gap_to_you"`
	ChipsUsed     []string `json:"chips_used"`
	ChipsLeft     []string `json:"chips_remaining"`
	SharedPlayers []string `json:"shared_players_with_you"`
	Direction     string   `json:"direction"` // "above" | "below" | "you"
}

// allChips is the full set in FPL 24/25 (2 of each).
var allChips = []string{"wildcard", "wildcard2", "3xc", "3xc2", "bboost", "bboost2", "freehit", "freehit2"}

// displayChip converts internal chip names to readable labels.
func displayChip(name string) string {
	switch name {
	case "wildcard", "wildcard2":
		return "WC"
	case "3xc", "3xc2":
		return "3xC"
	case "bboost", "bboost2":
		return "BB"
	case "freehit", "freehit2":
		return "FH"
	}
	return name
}

func chipsRemaining(used []string) []string {
	counts := map[string]int{
		"wildcard": 2, "3xc": 2, "bboost": 2, "freehit": 2,
	}
	for _, c := range used {
		base := strings.TrimSuffix(c, "2")
		counts[base]--
	}
	var remaining []string
	for _, chip := range []string{"wildcard", "3xc", "bboost", "freehit"} {
		for i := 0; i < counts[chip]; i++ {
			remaining = append(remaining, displayChip(chip))
		}
	}
	return remaining
}

func chipsUsedDisplay(used []string) []string {
	var out []string
	for _, c := range used {
		out = append(out, displayChip(c))
	}
	return out
}

func newLeagueRivalsCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var window int

	cmd := &cobra.Command{
		Use:   "league-rivals <league-id> <entry-id>",
		Short: "Rival analysis: who's above/below you, chips remaining, squad overlap",
		Long: `Shows managers within reach above and below your position in the league.
For each rival: points gap, chips remaining, squad overlap with your current team.
Run 'fpl-pp-cli league sync <league-id>' and 'fpl-pp-cli entry sync <entry-id>' first.`,
		Example: `  fpl-pp-cli league-rivals 665797 1263296
  fpl-pp-cli league-rivals 665797 1263296 --window 5
  fpl-pp-cli league-rivals 665797 1263296 --json`,
		Annotations: map[string]string{"mcp:read-only": "true"},
		Args:        cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			leagueID := args[0]
			myEntryID := args[1]

			if dbPath == "" {
				dbPath = defaultDBPath("fpl-pp-cli")
			}
			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer db.Close()

			// Load league standings
			var standingsResults []map[string]any
			var raw sqliteJSON
			if err := db.DB().QueryRowContext(cmd.Context(),
				`SELECT data FROM resources WHERE resource_type='leagues_classic_standings' AND id=?`, leagueID,
			).Scan(&raw); err != nil {
				return fmt.Errorf("league %s not found locally — run 'fpl-pp-cli league sync %s' first", leagueID, leagueID)
			}
			if err := json.Unmarshal(raw.v, &standingsResults); err != nil {
				// Try the wrapped API response format
				var apiResp map[string]json.RawMessage
				if err2 := json.Unmarshal(raw.v, &apiResp); err2 != nil {
					return fmt.Errorf("parsing standings: %w", err)
				}
				if sd, ok := apiResp["standings"]; ok {
					var standingsData map[string]json.RawMessage
					if json.Unmarshal(sd, &standingsData) == nil {
						json.Unmarshal(standingsData["results"], &standingsResults)
					}
				}
			}

			// Find my position
			myRank := 0
			myPts := 0
			for _, r := range standingsResults {
				if fmt.Sprintf("%.0f", r["entry"].(float64)) == myEntryID {
					myRank = int(r["rank"].(float64))
					myPts = int(r["total"].(float64))
					break
				}
			}
			if myRank == 0 {
				return fmt.Errorf("entry %s not found in league %s", myEntryID, leagueID)
			}

			// Load my current squad (most recent GW picks)
			myPlayers := map[int]bool{}
			var myPicksRaw sqliteJSON
			if err := db.DB().QueryRowContext(cmd.Context(),
				`SELECT data FROM entry_event WHERE entry_id=? ORDER BY id DESC LIMIT 1`, myEntryID,
			).Scan(&myPicksRaw); err == nil {
				var ev map[string]json.RawMessage
				if json.Unmarshal(myPicksRaw.v, &ev) == nil {
					var picks []map[string]any
					if json.Unmarshal(ev["picks"], &picks) == nil {
						for _, p := range picks {
							if pos := int(p["position"].(float64)); pos <= 11 {
								myPlayers[int(p["element"].(float64))] = true
							}
						}
					}
				}
			}

			// Build player names from bootstrap
			playerByID := map[int]string{}
			var bsStr string
			if err := db.DB().QueryRowContext(cmd.Context(),
				`SELECT data FROM resources WHERE resource_type='bootstrap-static' AND id='bootstrap-static'`,
			).Scan(&bsStr); err == nil {
				var bs map[string]json.RawMessage
				if json.Unmarshal(json.RawMessage(bsStr), &bs) == nil {
					var elements []map[string]any
					if json.Unmarshal(bs["elements"], &elements) == nil {
						for _, e := range elements {
							if id, ok := e["id"].(float64); ok {
								if name, ok := e["web_name"].(string); ok {
									playerByID[int(id)] = name
								}
							}
						}
					}
				}
			}

			// Filter to rivals within window positions above and below
			var rivals []rivalRow
			for _, r := range standingsResults {
				rank := int(r["rank"].(float64))
				entryID := int(r["entry"].(float64))
				total := int(r["total"].(float64))
				entryIDStr := fmt.Sprintf("%d", entryID)

				direction := ""
				if rank < myRank {
					if myRank-rank > window {
						continue
					}
					direction = "above"
				} else if rank > myRank {
					if rank-myRank > window {
						continue
					}
					direction = "below"
				} else {
					direction = "you"
				}

				// Load chips used from history
				var chipsUsed []string
				var histRaw sqliteJSON
				if err := db.DB().QueryRowContext(cmd.Context(),
					`SELECT data FROM history WHERE id=?`, entryIDStr,
				).Scan(&histRaw); err == nil {
					var hist map[string]json.RawMessage
					if json.Unmarshal(histRaw.v, &hist) == nil {
						var chips []map[string]any
						if json.Unmarshal(hist["chips"], &chips) == nil {
							for _, c := range chips {
								if name, ok := c["name"].(string); ok {
									chipsUsed = append(chipsUsed, name)
								}
							}
						}
					}
				}

				// Load squad picks → find shared players
				var shared []string
				var picksRaw sqliteJSON
				if err := db.DB().QueryRowContext(cmd.Context(),
					`SELECT data FROM entry_event WHERE entry_id=? ORDER BY id DESC LIMIT 1`, entryIDStr,
				).Scan(&picksRaw); err == nil {
					var ev map[string]json.RawMessage
					if json.Unmarshal(picksRaw.v, &ev) == nil {
						var picks []map[string]any
						if json.Unmarshal(ev["picks"], &picks) == nil {
							for _, p := range picks {
								if pos := int(p["position"].(float64)); pos <= 11 {
									eid := int(p["element"].(float64))
									if myPlayers[eid] {
										if name := playerByID[eid]; name != "" {
											shared = append(shared, name)
										} else {
											shared = append(shared, fmt.Sprintf("id_%d", eid))
										}
									}
								}
							}
						}
					}
				}
				sort.Strings(shared)

				rivals = append(rivals, rivalRow{
					Rank:          rank,
					EntryID:       entryID,
					Manager:       strVal(r, "player_name"),
					TeamName:      strVal(r, "entry_name"),
					TotalPts:      total,
					GapToYou:      total - myPts,
					ChipsUsed:     chipsUsedDisplay(chipsUsed),
					ChipsLeft:     chipsRemaining(chipsUsed),
					SharedPlayers: shared,
					Direction:     direction,
				})
			}

			sort.Slice(rivals, func(i, j int) bool { return rivals[i].Rank < rivals[j].Rank })

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rivals)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "League %s — rivals around rank %d (%d pts)\n\n", leagueID, myRank, myPts)
			fmt.Fprintln(tw, "RANK\tMANAGER\tTEAM\tTOTAL\tGAP\tCHIPS_LEFT\tSHARED_PLAYERS")
			for _, r := range rivals {
				gap := fmt.Sprintf("%+d", r.GapToYou)
				chips := strings.Join(r.ChipsLeft, ",")
				if chips == "" {
					chips = "none"
				}
				shared := strings.Join(r.SharedPlayers, ",")
				if shared == "" {
					shared = "—"
				}
				marker := ""
				if r.Direction == "you" {
					marker = " ◀ YOU"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s%s\n",
					r.Rank, r.Manager, r.TeamName, r.TotalPts, gap, chips, shared, marker)
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().IntVar(&window, "window", 5, "Number of positions above and below to show")
	return cmd
}
