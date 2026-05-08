---
name: pp-fpl
description: "Every FPL feature, plus offline queries, cross-gameweek analysis, and a local database no other FPL tool has. Trigger phrases: `check my fpl squad`, `best players to transfer in`, `fpl fixture difficulty`, `who should I captain this week`, `how many points did I leave on my bench`, `fpl league standings`, `check for fpl injuries`."
author: "mbulling83"
license: "Apache-2.0"
argument-hint: "<command> [args] | install cli|mcp"
allowed-tools: "Read Bash"
metadata:
  openclaw:
    requires:
      bins:
        - fpl-pp-cli
    install:
      - kind: go
        bins: [fpl-pp-cli]
        module: github.com/mvanhorn/printing-press-library/library/other/fpl/cmd/fpl-pp-cli
---

# Fantasy Premier League — Printing Press CLI

## Prerequisites: Install the CLI

This skill drives the `fpl-pp-cli` binary. **You must verify the CLI is installed before invoking any command from this skill.** If it is missing, install it first:

1. Install via the Printing Press installer:
   ```bash
   npx -y @mvanhorn/printing-press install fpl --cli-only
   ```
2. Verify: `fpl-pp-cli --version`
3. Ensure `$GOPATH/bin` (or `$HOME/go/bin`) is on `$PATH`.

If the `npx` install fails (no Node, offline, etc.), fall back to a direct Go install (requires Go 1.26.3 or newer):

```bash
go install github.com/mvanhorn/printing-press-library/library/other/fpl/cmd/fpl-pp-cli@latest
```

If `--version` reports "command not found" after install, the install step did not put the binary on `$PATH`. Do not proceed with skill commands until verification succeeds.

fpl-pp-cli syncs Fantasy Premier League data into a local SQLite store so you can query players, fixtures, and your team history offline. The SQLite layer enables cross-gameweek joins that no API call can answer: transfer ROI, bench regret, captain audit, and league momentum — all from a single static binary with no Python runtime required.

## When to Use This CLI

Use fpl-pp-cli when you want to analyse FPL data from the terminal, run cross-gameweek queries, generate structured JSON output for scripts or agents, or check player/fixture data without a browser. Ideal for power managers, developers, and AI agents making transfer decisions.

## When Not to Use This CLI

Do not activate this CLI for requests that require creating, updating, deleting, publishing, commenting, upvoting, inviting, ordering, sending messages, booking, purchasing, or changing remote state. This printed CLI exposes read-only commands for inspection, export, sync, and analysis.

## HTTP Transport

This CLI uses Chrome-compatible HTTP transport for browser-facing endpoints. It does not require a resident browser process for normal API calls.

## Command Reference

**bootstrap-static** — Manage bootstrap static

- `fpl-pp-cli bootstrap-static` — The main data endpoint. Returns all players (800+), teams (20), gameweeks (38), game settings, and element types....

**dream-team** — Manage dream team

- `fpl-pp-cli dream-team` — The best XI of the entire season by total points scored.

**element-summary** — Manage element summary

- `fpl-pp-cli element-summary <elementId>` — Per-player game-by-game stats, upcoming fixtures with difficulty ratings, and multi-season history.

**entry** — Manage entry

- `fpl-pp-cli entry <entryId>` — Manager profile including team name, overall rank, points, and league memberships.

**event** — Gameweek events and live data


**event-status** — Manage event status

- `fpl-pp-cli event-status` — Status of bonus points processing and league updates for recent match days.

**fixtures** — Match fixtures and results

- `fpl-pp-cli fixtures` — All Premier League fixtures for the season, or filtered to a specific gameweek.

**leagues-classic** — Manage leagues classic


**leagues-h2h** — Manage leagues h2h


**leagues-h2h-matches** — Manage leagues h2h matches

- `fpl-pp-cli leagues-h2h-matches <leagueId>` — All head-to-head match results for a specific entry in an H2H league.


### Finding the right command

When you know what you want to do but not which command does it, ask the CLI directly:

```bash
fpl-pp-cli which "<capability in your own words>"
```

`which` resolves a natural-language capability query to the best matching command from this CLI's curated feature index. Exit code `0` means at least one match; exit code `2` means no confident match — fall back to `--help` or use a narrower query.

## Recipes


### Best midfielders by xG before the deadline

```bash
fpl-pp-cli players list --pos MID --sort expected_goals --available --json --select web_name,team,now_cost,expected_goals,form
```

Filter available midfielders sorted by season xG with key fields only

### 6-GW fixture difficulty for your squad

```bash
fpl-pp-cli entry squad 12345 --grid --gws 6 --json
```

See which players in your squad have the best upcoming fixtures

### Which transfer this season cost you most points

```bash
fpl-pp-cli transfer roi 12345 --json --select element_out,points_cost,event
```

Cross-GW join of transfer history with player actual points

### Bench regret this season

```bash
fpl-pp-cli bench regret 12345 --json
```

Total points left on bench and worst individual GW misses

### League momentum over last 5 GWs

```bash
fpl-pp-cli leagues momentum 314159 --gws 5 --json --select player_name,rank_change
```

Who's rising and falling in your mini-league recently

## Auth Setup

No authentication required.

Run `fpl-pp-cli doctor` to verify setup.

## Agent Mode

Add `--agent` to any command. Expands to: `--json --compact --no-input --no-color --yes`.

- **Pipeable** — JSON on stdout, errors on stderr
- **Filterable** — `--select` keeps a subset of fields. Dotted paths descend into nested structures; arrays traverse element-wise. Critical for keeping context small on verbose APIs:

  ```bash
  fpl-pp-cli dream-team --agent --select id,name,status
  ```
- **Previewable** — `--dry-run` shows the request without sending
- **Offline-friendly** — sync/search commands can use the local SQLite store when available
- **Non-interactive** — never prompts, every input is a flag
- **Read-only** — do not use this CLI for create, update, delete, publish, comment, upvote, invite, order, send, or other mutating requests

### Response envelope

Commands that read from the local store or the API wrap output in a provenance envelope:

```json
{
  "meta": {"source": "live" | "local", "synced_at": "...", "reason": "..."},
  "results": <data>
}
```

Parse `.results` for data and `.meta.source` to know whether it's live or local. A human-readable `N results (live)` summary is printed to stderr only when stdout is a terminal — piped/agent consumers get pure JSON on stdout.

## Agent Feedback

When you (or the agent) notice something off about this CLI, record it:

```
fpl-pp-cli feedback "the --since flag is inclusive but docs say exclusive"
fpl-pp-cli feedback --stdin < notes.txt
fpl-pp-cli feedback list --json --limit 10
```

Entries are stored locally at `~/.fpl-pp-cli/feedback.jsonl`. They are never POSTed unless `FPL_FEEDBACK_ENDPOINT` is set AND either `--send` is passed or `FPL_FEEDBACK_AUTO_SEND=true`. Default behavior is local-only.

Write what *surprised* you, not a bug report. Short, specific, one line: that is the part that compounds.

## Output Delivery

Every command accepts `--deliver <sink>`. The output goes to the named sink in addition to (or instead of) stdout, so agents can route command results without hand-piping. Three sinks are supported:

| Sink | Effect |
|------|--------|
| `stdout` | Default; write to stdout only |
| `file:<path>` | Atomically write output to `<path>` (tmp + rename) |
| `webhook:<url>` | POST the output body to the URL (`application/json` or `application/x-ndjson` when `--compact`) |

Unknown schemes are refused with a structured error naming the supported set. Webhook failures return non-zero and log the URL + HTTP status on stderr.

## Named Profiles

A profile is a saved set of flag values, reused across invocations. Use it when a scheduled agent calls the same command every run with the same configuration - HeyGen's "Beacon" pattern.

```
fpl-pp-cli profile save briefing --json
fpl-pp-cli --profile briefing dream-team
fpl-pp-cli profile list --json
fpl-pp-cli profile show briefing
fpl-pp-cli profile delete briefing --yes
```

Explicit flags always win over profile values; profile values win over defaults. `agent-context` lists all available profiles under `available_profiles` so introspecting agents discover them at runtime.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 2 | Usage error (wrong arguments) |
| 3 | Resource not found |
| 5 | API error (upstream issue) |
| 7 | Rate limited (wait and retry) |
| 10 | Config error |

## Argument Parsing

Parse `$ARGUMENTS`:

1. **Empty, `help`, or `--help`** → show `fpl-pp-cli --help` output
2. **Starts with `install`** → ends with `mcp` → MCP installation; otherwise → see Prerequisites above
3. **Anything else** → Direct Use (execute as CLI command with `--agent`)

## MCP Server Installation

1. Install the MCP server:
   ```bash
   go install github.com/mvanhorn/printing-press-library/library/other/fpl/cmd/fpl-pp-mcp@latest
   ```
2. Register with Claude Code:
   ```bash
   claude mcp add fpl-pp-mcp -- fpl-pp-mcp
   ```
3. Verify: `claude mcp list`

## Direct Use

1. Check if installed: `which fpl-pp-cli`
   If not found, offer to install (see Prerequisites at the top of this skill).
2. Match the user query to the best command from the Unique Capabilities and Command Reference above.
3. Execute with the `--agent` flag:
   ```bash
   fpl-pp-cli <command> [subcommand] [args] --agent
   ```
4. If ambiguous, drill into subcommand help: `fpl-pp-cli <command> --help`.
