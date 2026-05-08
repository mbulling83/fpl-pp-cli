# Fantasy Premier League CLI

**Every FPL feature, plus offline queries, cross-gameweek analysis, and a local database no other FPL tool has.**

fpl-pp-cli syncs Fantasy Premier League data into a local SQLite store so you can query players, fixtures, and your team history offline. The SQLite layer enables cross-gameweek joins that no API call can answer: transfer ROI, bench regret, captain audit, and league momentum — all from a single static binary with no Python runtime required.

## Install

The recommended path installs both the `fpl-pp-cli` binary and the `pp-fpl` agent skill in one shot:

```bash
npx -y @mvanhorn/printing-press install fpl
```

For CLI only (no skill):

```bash
npx -y @mvanhorn/printing-press install fpl --cli-only
```

### Without Node (Go fallback)

If `npx` isn't available (no Node, offline), install the CLI directly via Go (requires Go 1.26.3 or newer):

```bash
go install github.com/mvanhorn/printing-press-library/library/other/fpl/cmd/fpl-pp-cli@latest
```

This installs the CLI only — no skill.

### Pre-built binary

Download a pre-built binary for your platform from the [latest release](https://github.com/mvanhorn/printing-press-library/releases/tag/fpl-current). On macOS, clear the Gatekeeper quarantine: `xattr -d com.apple.quarantine <binary>`. On Unix, mark it executable: `chmod +x <binary>`.

<!-- pp-hermes-install-anchor -->
## Install for Hermes

From the Hermes CLI:

```bash
hermes skills install mvanhorn/printing-press-library/cli-skills/pp-fpl --force
```

Inside a Hermes chat session:

```bash
/skills install mvanhorn/printing-press-library/cli-skills/pp-fpl --force
```

## Install for OpenClaw

Tell your OpenClaw agent (copy this):

```
Install the pp-fpl skill from https://github.com/mvanhorn/printing-press-library/tree/main/cli-skills/pp-fpl. The skill defines how its required CLI can be installed.
```

## Quick Start

```bash
# Sync all FPL data to local SQLite (bootstrap, fixtures, gameweeks)
fpl-pp-cli sync


# Current GW, deadline, last result, flagged players
fpl-pp-cli status


# Best available midfielders by xG
fpl-pp-cli players list --pos MID --sort xg --available --json


# 6-gameweek fixture difficulty grid for all teams
fpl-pp-cli fixtures fdr --gws 6 --json


# Your squad health check with FDR
fpl-pp-cli entry squad 12345

```

## Usage

Run `fpl-pp-cli --help` for the full command reference and flag list.

## Commands

### bootstrap-static

Manage bootstrap static

- **`fpl-pp-cli bootstrap-static get-bootstrap`** - The main data endpoint. Returns all players (800+), teams (20), gameweeks (38), game settings, and element types. This is the primary data source for the local SQLite store.

### dream-team

Manage dream team

- **`fpl-pp-cli dream-team get`** - The best XI of the entire season by total points scored.

### element-summary

Manage element summary

- **`fpl-pp-cli element-summary get-player-summary`** - Per-player game-by-game stats, upcoming fixtures with difficulty ratings, and multi-season history.

### entry

Manage entry

- **`fpl-pp-cli entry get`** - Manager profile including team name, overall rank, points, and league memberships.

### event

Gameweek events and live data


### event-status

Manage event status

- **`fpl-pp-cli event-status get`** - Status of bonus points processing and league updates for recent match days.

### fixtures

Match fixtures and results

- **`fpl-pp-cli fixtures list`** - All Premier League fixtures for the season, or filtered to a specific gameweek.

### leagues-classic

Manage leagues classic


### leagues-h2h

Manage leagues h2h


### leagues-h2h-matches

Manage leagues h2h matches

- **`fpl-pp-cli leagues-h2h-matches list-h2-hmatches`** - All head-to-head match results for a specific entry in an H2H league.


## Output Formats

```bash
# Human-readable table (default in terminal, JSON when piped)
fpl-pp-cli dream-team

# JSON for scripting and agents
fpl-pp-cli dream-team --json

# Filter to specific fields
fpl-pp-cli dream-team --json --select id,name,status

# Dry run — show the request without sending
fpl-pp-cli dream-team --dry-run

# Agent mode — JSON + compact + no prompts in one flag
fpl-pp-cli dream-team --agent
```

## Agent Usage

This CLI is designed for AI agent consumption:

- **Non-interactive** - never prompts, every input is a flag
- **Pipeable** - `--json` output to stdout, errors to stderr
- **Filterable** - `--select id,name` returns only fields you need
- **Previewable** - `--dry-run` shows the request without sending
- **Read-only by default** - this CLI does not create, update, delete, publish, send, or mutate remote resources
- **Offline-friendly** - sync/search commands can use the local SQLite store when available
- **Agent-safe by default** - no colors or formatting unless `--human-friendly` is set

Exit codes: `0` success, `2` usage error, `3` not found, `5` API error, `7` rate limited, `10` config error.

## Use with Claude Code

Install the focused skill — it auto-installs the CLI on first invocation:

```bash
npx skills add mvanhorn/printing-press-library/cli-skills/pp-fpl -g
```

Then invoke `/pp-fpl <query>` in Claude Code. The skill is the most efficient path — Claude Code drives the CLI directly without an MCP server in the middle.

<details>
<summary>Use as an MCP server in Claude Code (advanced)</summary>

If you'd rather register this CLI as an MCP server in Claude Code, install the MCP binary first:

```bash
go install github.com/mvanhorn/printing-press-library/library/other/fpl/cmd/fpl-pp-mcp@latest
```

Then register it:

```bash
claude mcp add fpl fpl-pp-mcp
```

</details>

## Use with Claude Desktop

This CLI ships an [MCPB](https://github.com/modelcontextprotocol/mcpb) bundle — Claude Desktop's standard format for one-click MCP extension installs (no JSON config required).

To install:

1. Download the `.mcpb` for your platform from the [latest release](https://github.com/mvanhorn/printing-press-library/releases/tag/fpl-current).
2. Double-click the `.mcpb` file. Claude Desktop opens and walks you through the install.

Requires Claude Desktop 1.0.0 or later. Pre-built bundles ship for macOS Apple Silicon (`darwin-arm64`) and Windows (`amd64`, `arm64`); for other platforms, use the manual config below.

<details>
<summary>Manual JSON config (advanced)</summary>

If you can't use the MCPB bundle (older Claude Desktop, unsupported platform), install the MCP binary and configure it manually.

```bash
go install github.com/mvanhorn/printing-press-library/library/other/fpl/cmd/fpl-pp-mcp@latest
```

Add to your Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "fpl": {
      "command": "fpl-pp-mcp"
    }
  }
}
```

</details>

## Health Check

```bash
fpl-pp-cli doctor
```

Verifies configuration and connectivity to the API.

## Configuration

Config file: `~/.config/fpl-pp-cli/config.toml`

## Troubleshooting
**Not found errors (exit code 3)**
- Check the resource ID is correct
- Run the `list` command to see available items

### API-specific

- **Empty results from player commands** — Run fpl-pp-cli sync first to populate the local database
- **Stale player prices or form values** — Run fpl-pp-cli sync --force to refresh from the API
- **Entry ID not found** — Find your entry ID in the URL at fantasy.premierleague.com (it's the number after /entry/)

## HTTP Transport

This CLI uses Chrome-compatible HTTP transport for browser-facing endpoints. It does not require a resident browser process for normal API calls.

---

## Sources & Inspiration

This CLI was built by studying these projects and resources:

- [**amosbastian/fpl**](https://github.com/amosbastian/fpl) — Python (325 stars)
- [**jeppe-smith/fpl-api**](https://github.com/jeppe-smith/fpl-api) — TypeScript (17 stars)
- [**janerikcarlsen/fpl-cli**](https://github.com/janerikcarlsen/fpl-cli) — Python (7 stars)
- [**rossgroomio/fpl-cli**](https://github.com/rossgroomio/fpl-cli) — Python
- [**lewis-king/fpl-mcp-server**](https://github.com/lewis-king/fpl-mcp-server) — Python
- [**rishijatia/fantasy-pl-mcp**](https://github.com/rishijatia/fantasy-pl-mcp) — Python

Generated by [CLI Printing Press](https://github.com/mvanhorn/cli-printing-press)
