# Kairon - Agent Instructions

## Project Overview

Kairon is a personal life scheduler for Imants. It pulls events from Google Calendar (2 calendars on iliepins01@gmail.com — personal + TUM uni subscription), applies scheduling rules from a YAML config, and generates a filled weekly schedule with 15-minute granularity. Output is a color-coded HTML weekly view served locally.

## Architecture

- **Language:** Go (single binary, no runtime deps)
- **Calendar:** Google Calendar API via OAuth2, project `kairon-il` on GCP (iliepins01@gmail.com account)
- **Config:** `config.yaml` — all scheduling rules, activity definitions, constraints, ignore list
- **Engine:** Rule-based scheduler that fills free slots between fixed calendar events
- **Output:** Static HTML weekly grid served via local HTTP server. One file per week (e.g., `schedules/2026-W17.html`) so weeks can be compared.

## Key Concepts

- **15-minute granularity** — minimum slot size, no gaps during the day (7:30–21:00)
- **Priorities:** critical, important, mid, suggestion — for scheduling order, NOT for color coding
- **Color coding:** by activity type (uni, work, personal/fitness, meals, breaks, etc.), NOT by priority
- **Ignored events:** Calendar events can be marked ignored in config → shown faded/transparent in output, treated as free time for scheduling
- **Commute buffers:** 45 min each way around on-campus uni events
- **Schedule rules** are updated by asking the agent in natural language; the agent edits `config.yaml`

## Tool Installation

Install whatever tools are needed to get the job done. If a CLI tool, library, or dependency is required, install it (e.g., `brew install`, `go get`, etc.) without asking. Document external requirements in the README.

## Git Policy

**NEVER run git commands that modify state** (no `git add`, `git commit`, `git push`, `git checkout`, `git reset`, `git stash`, `git merge`, `git rebase`, etc.). Only readonly git commands are allowed (`git status`, `git log`, `git diff`, `git branch --list`, etc.). The user manages git themselves.

## Google Cloud

- **Project ID:** `kairon-il`
- **Account:** `iliepins01@gmail.com` (NEVER use work email)
- **OAuth credentials:** stored in `credentials/` directory (gitignored)
- **Calendars:** personal calendar + TUM subscription calendar, both on the same Google account

## File Structure

```
kairon/
├── AGENTS.md           # This file
├── SCHEDULE.md         # Human-readable context about Imants' schedule
├── config.yaml         # Scheduling rules, activities, constraints, ignore list
├── credentials/        # OAuth credentials (gitignored)
├── schedules/          # Generated HTML schedules (e.g., 2026-W17.html)
├── go.mod
├── cmd/
│   └── scheduler/
│       └── main.go     # Entry point
├── internal/
│   ├── calendar/       # Google Calendar integration
│   ├── config/         # YAML config parsing
│   ├── engine/         # Scheduling algorithm
│   └── render/         # HTML generation
└── .gitignore
```

## Config Format (config.yaml)

Activities have: name, type (uni/work/fitness/meal/personal/break), duration, frequency (per week), priority, time constraints (earliest, latest, preferred), and scheduling hints.

## Running

```bash
go run ./cmd/scheduler/           # Generate this week's schedule and serve it
go run ./cmd/scheduler/ --week 2026-W17  # Generate specific week
```
