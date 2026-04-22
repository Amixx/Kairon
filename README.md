# Kairon

A personal weekly schedule generator. Pulls events from Google Calendar, applies scheduling rules via MILP optimization, and produces a color-coded HTML weekly view.

## Requirements

- **Go 1.21+**
- **CBC solver** (COIN-OR Branch and Cut): `brew install cbc`
- **Google Cloud project** with Calendar API enabled (project: `kairon-il`)

## Setup

1. Place Google OAuth credentials in `credentials/client_secret.json` (see [Google Calendar API quickstart](https://developers.google.com/calendar/api/quickstart/go))
2. Run the scheduler — it will open a browser for OAuth on first run

## Usage

```bash
# Generate this week's schedule
go run ./cmd/scheduler/

# Generate a specific week
go run ./cmd/scheduler/ --week 2026-W17

# Print MILP size by rule family before solving
go run ./cmd/scheduler/ --audit-model
```

Schedules are saved to `schedules/` as HTML files (e.g., `2026-W17.html`).

## Configuration

Edit `config.yaml` to change scheduling rules, or ask the AI agent to update it via natural language.
