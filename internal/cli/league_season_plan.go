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

type seasonPlanEntry struct {
	Rank          int      `json:"rank"`
	EntryID       int      `json:"entry_id"`
	Manager       string   `json:"manager"`
	TeamName      string   `json:"team_name"`
	TotalPts      int      `json:"total_pts"`
	GapToYou      int      `json:"gap_to_you"`
	ChipsLeft     []string `json:"chips_remaining"`
	ChipThreat    string   `json:"chip_threat"` // "HIGH" | "MEDIUM" | "LOW" | ""
	SharedWith    []string `json:"shared_starters_with_you"`
	UniqueToRival []string `json:"unique_to_rival"`
	UniqueToYou   []string `json:"unique_to_you"`
	Direction     string   `json:"direction"`
}

type captainCandidate struct {
	GW     int    `json:"gw"`
	Player string `json:"player"`
	Team   string `json:"team"`
	FDR    int    `json:"fixture_fdr"`
	Form   string `json:"form"`
}

type seasonPlanResult struct {
	LeagueID        string            `json:"league_id"`
	EntryID         string            `json:"entry_id"`
	MyRank          int               `json:"my_rank"`
	MyPts           int               `json:"my_pts"`
	GWsRemaining    int               `json:"gws_remaining"`
	Rivals          []seasonPlanEntry `json:"rivals"`
	CaptainPlan     []captainCandidate `json:"captain_plan"`
	TransferAlerts  []string          `json:"transfer_alerts"`
	KeyInsights     []string          `json:"key_insights"`
}

func newLeagueSeasonPlanCmd(flags *rootFlags) *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "league-season-plan <league-id> <entry-id>",
		Short: "End-of-season mini-league strategy: gaps, chip threats, captain plan, transfer alerts",
		Long: `Combines league standings, rival squads, fixture difficulty, and chip status
into a structured end-of-season plan. Shows who you can catch, who threatens you,
shared players that cancel out, your unique differentials, and captain recommendations.

Run 'fpl-pp-cli league sync <league-id>' and 'fpl-pp-cli entry sync <entry-id>' first.`,
		Example: `  fpl-pp-cli league-season-plan 665797 1263296
  fpl-pp-cli league-season-plan 665797 1263296 --json`,
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

			// ── Bootstrap data ──────────────────────────────────────────────
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
			json.Unmarshal(bs["events"], &events)
			var elements []map[string]any
			json.Unmarshal(bs["elements"], &elements)
			var teams []map[string]any
			json.Unmarshal(bs["teams"], &teams)

			teamByID := map[int]map[string]any{}
			for _, t := range teams {
				if id, ok := t["id"].(float64); ok {
					teamByID[int(id)] = t
				}
			}
			playerByID := map[int]map[string]any{}
			for _, e := range elements {
				if id, ok := e["id"].(float64); ok {
					playerByID[int(id)] = e
				}
			}

			currentGW, totalGWs := 0, 38
			for _, ev := range events {
				if fin, ok := ev["finished"].(bool); ok && fin {
					if id, ok := ev["id"].(float64); ok && int(id) > currentGW {
						currentGW = int(id)
					}
				}
			}
			gwsRemaining := totalGWs - currentGW

			// ── League standings ────────────────────────────────────────────
			var standingsResults []map[string]any
			var standRaw sqliteJSON
			if err := db.DB().QueryRowContext(cmd.Context(),
				`SELECT data FROM resources WHERE resource_type='leagues_classic_standings' AND id=?`, leagueID,
			).Scan(&standRaw); err != nil {
				return fmt.Errorf("league %s not found locally — run 'fpl-pp-cli league sync %s' first", leagueID, leagueID)
			}
			if err := json.Unmarshal(standRaw.v, &standingsResults); err != nil {
				var apiResp map[string]json.RawMessage
				if err2 := json.Unmarshal(standRaw.v, &apiResp); err2 == nil {
					if sd, ok := apiResp["standings"]; ok {
						var sd2 map[string]json.RawMessage
						if json.Unmarshal(sd, &sd2) == nil {
							json.Unmarshal(sd2["results"], &standingsResults)
						}
					}
				}
			}

			myRank, myPts := 0, 0
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

			// ── My current starters ─────────────────────────────────────────
			myStarters := map[int]bool{}
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
								myStarters[int(p["element"].(float64))] = true
							}
						}
					}
				}
			}

			// ── Fixture data for captain analysis ────────────────────────────
			type fixtureKey struct{ teamID, gw int }
			type fixInfo struct{ oppShort, ha string; fdr int }
			fixturesByTeam := map[fixtureKey][]fixInfo{}

			fixtRows, err := db.DB().QueryContext(cmd.Context(),
				`SELECT data FROM fixtures WHERE event > ? AND event <= 38`, currentGW)
			if err == nil {
				defer fixtRows.Close()
				for fixtRows.Next() {
					var fraw sqliteJSON
					if fixtRows.Scan(&fraw) != nil {
						continue
					}
					var f map[string]any
					if json.Unmarshal(fraw.v, &f) != nil {
						continue
					}
					gw := int(f["event"].(float64))
					homeID := int(f["team_h"].(float64))
					awayID := int(f["team_a"].(float64))
					homeDiff := int(f["team_h_difficulty"].(float64))
					awayDiff := int(f["team_a_difficulty"].(float64))

					homeTeam := teamByID[awayID]
					awayTeam := teamByID[homeID]
					homeShort, awayShort := "?", "?"
					if homeTeam != nil {
						homeShort, _ = homeTeam["short_name"].(string)
					}
					if awayTeam != nil {
						awayShort, _ = awayTeam["short_name"].(string)
					}
					k1 := fixtureKey{homeID, gw}
					fixturesByTeam[k1] = append(fixturesByTeam[k1], fixInfo{homeShort, "H", homeDiff})
					k2 := fixtureKey{awayID, gw}
					fixturesByTeam[k2] = append(fixturesByTeam[k2], fixInfo{awayShort, "A", awayDiff})
				}
			}

			// ── Captain candidates from my squad ─────────────────────────────
			var captainPlan []captainCandidate
			for pid := range myStarters {
				pl := playerByID[pid]
				if pl == nil {
					continue
				}
				teamID := int(pl["team"].(float64))
				name, _ := pl["web_name"].(string)
				form, _ := pl["form"].(string)
				t := teamByID[teamID]
				teamShort := "?"
				if t != nil {
					teamShort, _ = t["short_name"].(string)
				}
				for gw := currentGW + 1; gw <= min(currentGW+3, 38); gw++ {
					for _, fix := range fixturesByTeam[fixtureKey{teamID, gw}] {
						captainPlan = append(captainPlan, captainCandidate{
							GW:     gw,
							Player: name,
							Team:   teamShort,
							FDR:    fix.fdr,
							Form:   form,
						})
					}
				}
			}
			// Sort by GW then FDR (lower = easier = better captain pick)
			sort.Slice(captainPlan, func(i, j int) bool {
				if captainPlan[i].GW != captainPlan[j].GW {
					return captainPlan[i].GW < captainPlan[j].GW
				}
				return captainPlan[i].FDR < captainPlan[j].FDR
			})

			// ── Transfer alerts: dubious/injured starters ────────────────────
			var transferAlerts []string
			for pid := range myStarters {
				pl := playerByID[pid]
				if pl == nil {
					continue
				}
				status, _ := pl["status"].(string)
				news, _ := pl["news"].(string)
				name, _ := pl["web_name"].(string)
				form := 0.0
				if f, ok := pl["form"].(string); ok {
					fmt.Sscanf(f, "%f", &form)
				}
				if status == "d" || status == "i" || status == "u" {
					transferAlerts = append(transferAlerts, fmt.Sprintf("%s — status=%s: %s", name, status, news))
				} else if form < 2.0 {
					transferAlerts = append(transferAlerts, fmt.Sprintf("%s — form %.1f (low)", name, form))
				}
			}

			// ── Rival analysis ───────────────────────────────────────────────
			var rivals []seasonPlanEntry
			for _, r := range standingsResults {
				rank := int(r["rank"].(float64))
				entryID := int(r["entry"].(float64))
				total := int(r["total"].(float64))
				entryIDStr := fmt.Sprintf("%d", entryID)

				direction := ""
				if rank < myRank {
					direction = "above"
				} else if rank > myRank {
					direction = "below"
				} else {
					direction = "you"
				}

				// Chips
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
				chipsLeft := chipsRemaining(chipsUsed)

				// Chip threat assessment
				chipThreat := ""
				if direction == "below" {
					numChips := len(chipsLeft)
					gap := myPts - total // positive = they're behind
					if numChips >= 3 {
						chipThreat = "HIGH"
					} else if numChips >= 2 && gap < 30 {
						chipThreat = "HIGH"
					} else if numChips >= 1 && gap < 15 {
						chipThreat = "MEDIUM"
					} else if numChips == 0 {
						chipThreat = "LOW"
					}
				}

				// Squad overlap
				var shared, uniqueToRival, uniqueToYou []string
				var rivalPicksRaw sqliteJSON
				rivalStarters := map[int]bool{}
				if err := db.DB().QueryRowContext(cmd.Context(),
					`SELECT data FROM entry_event WHERE entry_id=? ORDER BY id DESC LIMIT 1`, entryIDStr,
				).Scan(&rivalPicksRaw); err == nil {
					var ev map[string]json.RawMessage
					if json.Unmarshal(rivalPicksRaw.v, &ev) == nil {
						var picks []map[string]any
						if json.Unmarshal(ev["picks"], &picks) == nil {
							for _, p := range picks {
								if pos := int(p["position"].(float64)); pos <= 11 {
									rivalStarters[int(p["element"].(float64))] = true
								}
							}
						}
					}
				}
				for pid := range myStarters {
					name := playerByID[pid]["web_name"].(string)
					if rivalStarters[pid] {
						shared = append(shared, name)
					} else {
						uniqueToYou = append(uniqueToYou, name)
					}
				}
				for pid := range rivalStarters {
					if !myStarters[pid] {
						if pl := playerByID[pid]; pl != nil {
							if name, ok := pl["web_name"].(string); ok {
								uniqueToRival = append(uniqueToRival, name)
							}
						}
					}
				}
				sort.Strings(shared)
				sort.Strings(uniqueToRival)
				sort.Strings(uniqueToYou)

				rivals = append(rivals, seasonPlanEntry{
					Rank:          rank,
					EntryID:       entryID,
					Manager:       strVal(r, "player_name"),
					TeamName:      strVal(r, "entry_name"),
					TotalPts:      total,
					GapToYou:      total - myPts,
					ChipsLeft:     chipsLeft,
					ChipThreat:    chipThreat,
					SharedWith:    shared,
					UniqueToRival: uniqueToRival,
					UniqueToYou:   uniqueToYou,
					Direction:     direction,
				})
			}
			sort.Slice(rivals, func(i, j int) bool { return rivals[i].Rank < rivals[j].Rank })

			// ── Key insights ─────────────────────────────────────────────────
			var insights []string
			for _, rv := range rivals {
				if rv.Direction == "above" && rv.GapToYou < 0 {
					gap := -rv.GapToYou
					perGW := 0.0
					if gwsRemaining > 0 {
						perGW = float64(gap) / float64(gwsRemaining)
					}
					insights = append(insights, fmt.Sprintf(
						"To catch rank %d (%s, %+d pts): need +%.1f pts/GW over %d GWs — %d shared players cancel out",
						rv.Rank, rv.Manager, rv.GapToYou, perGW, gwsRemaining, len(rv.SharedWith),
					))
				}
				if rv.Direction == "below" && rv.ChipThreat == "HIGH" {
					insights = append(insights, fmt.Sprintf(
						"⚠ Rank %d (%s, %+d pts) has %d chips left — HIGH threat to overtake you",
						rv.Rank, rv.Manager, rv.GapToYou, len(rv.ChipsLeft),
					))
				}
			}

			result := seasonPlanResult{
				LeagueID:       leagueID,
				EntryID:        myEntryID,
				MyRank:         myRank,
				MyPts:          myPts,
				GWsRemaining:   gwsRemaining,
				Rivals:         rivals,
				CaptainPlan:    captainPlan,
				TransferAlerts: transferAlerts,
				KeyInsights:    insights,
			}

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// ── Human output ─────────────────────────────────────────────────
			fmt.Fprintf(os.Stdout, "=== League %s — End-of-Season Plan ===\n", leagueID)
			fmt.Fprintf(os.Stdout, "You: rank %d | %d pts | %d GWs remaining\n\n", myRank, myPts, gwsRemaining)

			// Standings + rival analysis
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "RANK\tMANAGER\tTEAM\tPTS\tGAP\tCHIPS_LEFT\tTHREAT\tSHARED")
			for _, rv := range rivals {
				gap := fmt.Sprintf("%+d", rv.GapToYou)
				chips := strings.Join(rv.ChipsLeft, "+")
				if chips == "" {
					chips = "—"
				}
				shared := fmt.Sprintf("%d players", len(rv.SharedWith))
				threat := rv.ChipThreat
				if threat == "" {
					threat = "—"
				}
				marker := ""
				if rv.Direction == "you" {
					marker = " ◀"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s\t%s%s\n",
					rv.Rank, rv.Manager, rv.TeamName, rv.TotalPts, gap, chips, threat, shared, marker)
			}
			tw.Flush()

			// Key insights
			if len(insights) > 0 {
				fmt.Println("\n── Key Insights ──")
				for _, ins := range insights {
					fmt.Println("  •", ins)
				}
			}

			// Transfer alerts
			if len(transferAlerts) > 0 {
				fmt.Println("\n── Transfer Alerts (from your squad) ──")
				for _, alert := range transferAlerts {
					fmt.Println("  !", alert)
				}
			}

			// Captain plan (top pick per GW)
			fmt.Println("\n── Captain Candidates (your starters, upcoming GWs) ──")
			tw2 := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw2, "GW\tPLAYER\tTEAM\tFDR\tFORM")
			shown := map[int]int{} // gw → count shown
			for _, c := range captainPlan {
				if shown[c.GW] >= 3 {
					continue
				}
				fmt.Fprintf(tw2, "%d\t%s\t%s\t%d\t%s\n", c.GW, c.Player, c.Team, c.FDR, c.Form)
				shown[c.GW]++
			}
			tw2.Flush()

			// Differential breakdown for top rivals
			fmt.Println("\n── Squad Differentials (vs nearest rivals) ──")
			for _, rv := range rivals {
				if rv.Direction == "you" {
					continue
				}
				gap := rv.GapToYou
				if gap > 30 || gap < -30 {
					continue // only show close rivals
				}
				dir := "↑ above"
				if rv.Direction == "below" {
					dir = "↓ below"
				}
				fmt.Printf("\nVs rank %d %s (%s, %+d pts):\n", rv.Rank, dir, rv.Manager, rv.GapToYou)
				if len(rv.SharedWith) > 0 {
					fmt.Printf("  Shared (cancel out): %s\n", strings.Join(rv.SharedWith, ", "))
				}
				if len(rv.UniqueToYou) > 0 {
					fmt.Printf("  Your edge: %s\n", strings.Join(rv.UniqueToYou, ", "))
				}
				if len(rv.UniqueToRival) > 0 {
					fmt.Printf("  Their edge: %s\n", strings.Join(rv.UniqueToRival, ", "))
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	return cmd
}

