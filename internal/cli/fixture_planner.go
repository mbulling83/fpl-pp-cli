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

type fixturePlannerRow struct {
	PlayerID int             `json:"player_id"`
	Name     string          `json:"name"`
	Team     string          `json:"team"`
	Position string          `json:"position"`
	Form     string          `json:"form"`
	Fixtures []gwFixtureCell `json:"fixtures"`
	AvgFDR   float64         `json:"avg_fdr"`
}

type gwFixtureCell struct {
	GW         int    `json:"gw"`
	Opponent   string `json:"opponent"`
	HomeAway   string `json:"home_away"`
	Difficulty int    `json:"difficulty"`
}

func newFixturePlannerCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var gwWindow int

	cmd := &cobra.Command{
		Use:   "fixture-planner <entry-id>",
		Short: "Upcoming fixture difficulty grid for your current squad",
		Long: `Shows FDR for each player in your squad over the next N gameweeks.
Helps identify when to captain, transfer, or play chips.`,
		Example: `  fpl-pp-cli fixture-planner 12345
  fpl-pp-cli fixture-planner 12345 --gws 8
  fpl-pp-cli fixture-planner 12345 --json`,
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
			bsRaw, err := db.Get("bootstrap_static", "bootstrap_static")
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
			var events []map[string]any
			if err := json.Unmarshal(bs["events"], &events); err != nil {
				return fmt.Errorf("parsing events: %w", err)
			}

			teamByID := make(map[int]map[string]any, len(teams))
			for _, t := range teams {
				if id, ok := t["id"].(float64); ok {
					teamByID[int(id)] = t
				}
			}
			playerByID := make(map[int]map[string]any, len(elements))
			for _, e := range elements {
				if id, ok := e["id"].(float64); ok {
					playerByID[int(id)] = e
				}
			}
			posMap := map[int]string{1: "GKP", 2: "DEF", 3: "MID", 4: "FWD"}

			// Current GW
			currentGW := 0
			for _, ev := range events {
				if fin, ok := ev["finished"].(bool); ok && fin {
					if id, ok := ev["id"].(float64); ok && int(id) > currentGW {
						currentGW = int(id)
					}
				}
			}
			nextGW := currentGW + 1
			endGW := nextGW + gwWindow - 1

			// Load fixtures for window
			type fixtureInfo struct {
				GW         int
				HomeTeamID int
				AwayTeamID int
				HomeDiff   int
				AwayDiff   int
			}
			fixturesByTeam := make(map[int][]fixtureInfo) // team_id -> fixtures

			fixtureRows, err := db.DB().QueryContext(cmd.Context(), `SELECT data FROM fixtures`)
			if err != nil {
				return fmt.Errorf("querying fixtures: %w", err)
			}
			defer fixtureRows.Close()
			for fixtureRows.Next() {
				var raw json.RawMessage
				if err := fixtureRows.Scan(&raw); err != nil {
					continue
				}
				var fixturesArr []map[string]any
				if err := json.Unmarshal(raw, &fixturesArr); err != nil {
					continue
				}
				for _, f := range fixturesArr {
					gw, _ := f["event"].(float64)
					if int(gw) < nextGW || int(gw) > endGW {
						continue
					}
					homeID, _ := f["team_h"].(float64)
					awayID, _ := f["team_a"].(float64)
					homeDiff, _ := f["team_h_difficulty"].(float64)
					awayDiff, _ := f["team_a_difficulty"].(float64)
					fi := fixtureInfo{
						GW:         int(gw),
						HomeTeamID: int(homeID),
						AwayTeamID: int(awayID),
						HomeDiff:   int(homeDiff),
						AwayDiff:   int(awayDiff),
					}
					fixturesByTeam[int(homeID)] = append(fixturesByTeam[int(homeID)], fi)
					fixturesByTeam[int(awayID)] = append(fixturesByTeam[int(awayID)], fi)
				}
			}

			// Load current squad (most recent GW)
			var squadPicks []map[string]any
			for gw := currentGW; gw >= 1; gw-- {
				gwStr := fmt.Sprintf("%d", gw)
				var raw json.RawMessage
				err := db.DB().QueryRowContext(cmd.Context(),
					`SELECT data FROM entry_event WHERE entry_id=? AND event_id=?`,
					entryID, gwStr).Scan(&raw)
				if err != nil {
					continue
				}
				var ev map[string]json.RawMessage
				if err := json.Unmarshal(raw, &ev); err != nil {
					continue
				}
				if err := json.Unmarshal(ev["picks"], &squadPicks); err != nil {
					continue
				}
				break
			}

			var result []fixturePlannerRow
			for _, pick := range squadPicks {
				pos := int(pick["position"].(float64))
				if pos > 11 {
					continue // starters only
				}
				eid := int(pick["element"].(float64))
				el := playerByID[eid]
				if el == nil {
					continue
				}
				teamID, _ := el["team"].(float64)
				name, _ := el["web_name"].(string)
				form, _ := el["form"].(string)
				elType, _ := el["element_type"].(float64)
				t := teamByID[int(teamID)]
				teamShort, _ := t["short_name"].(string)

				fixtures := fixturesByTeam[int(teamID)]
				sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].GW < fixtures[j].GW })

				var cells []gwFixtureCell
				var sumDiff float64
				for _, f := range fixtures {
					var oppID int
					var ha string
					var diff int
					if f.HomeTeamID == int(teamID) {
						oppID = f.AwayTeamID
						ha = "H"
						diff = f.HomeDiff
					} else {
						oppID = f.HomeTeamID
						ha = "A"
						diff = f.AwayDiff
					}
					opp := teamByID[oppID]
					oppShort := fmt.Sprintf("t%d", oppID)
					if opp != nil {
						if n, ok := opp["short_name"].(string); ok {
							oppShort = n
						}
					}
					cells = append(cells, gwFixtureCell{
						GW:         f.GW,
						Opponent:   oppShort,
						HomeAway:   ha,
						Difficulty: diff,
					})
					sumDiff += float64(diff)
				}
				avgFDR := 0.0
				if len(cells) > 0 {
					avgFDR = sumDiff / float64(len(cells))
				}
				result = append(result, fixturePlannerRow{
					PlayerID: eid,
					Name:     name,
					Team:     teamShort,
					Position: posMap[int(elType)],
					Form:     form,
					Fixtures: cells,
					AvgFDR:   avgFDR,
				})
			}

			sort.Slice(result, func(i, j int) bool { return result[i].AvgFDR < result[j].AvgFDR })

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// Build GW header
			gwHeaders := []string{"PLAYER", "TEAM", "POS", "FORM"}
			for gw := nextGW; gw <= endGW; gw++ {
				gwHeaders = append(gwHeaders, fmt.Sprintf("GW%d", gw))
			}
			gwHeaders = append(gwHeaders, "AVG_FDR")

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, strings.Join(gwHeaders, "\t"))
			for _, r := range result {
				gwMap := make(map[int]gwFixtureCell)
				for _, f := range r.Fixtures {
					gwMap[f.GW] = f
				}
				cols := []string{r.Name, r.Team, r.Position, r.Form}
				for gw := nextGW; gw <= endGW; gw++ {
					if f, ok := gwMap[gw]; ok {
						cols = append(cols, fmt.Sprintf("%s(%s)%d", f.Opponent, f.HomeAway, f.Difficulty))
					} else {
						cols = append(cols, "-")
					}
				}
				cols = append(cols, fmt.Sprintf("%.1f", r.AvgFDR))
				fmt.Fprintln(tw, strings.Join(cols, "\t"))
			}
			return tw.Flush()
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().IntVar(&gwWindow, "gws", 5, "Number of upcoming gameweeks to show")
	return cmd
}
