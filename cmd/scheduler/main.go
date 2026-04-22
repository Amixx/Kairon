package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amixx/kairon/internal/calendar"
	"github.com/amixx/kairon/internal/config"
	"github.com/amixx/kairon/internal/engine"
	"github.com/amixx/kairon/internal/render"
)

func main() {
	weekFlag := flag.String("week", "", "ISO week to generate (e.g., 2026-W17). Defaults to current week.")
	listCals := flag.Bool("list-calendars", false, "List available Google Calendar IDs and exit.")
	port := flag.Int("port", 8080, "Port for the local HTTP server.")
	configPath := flag.String("config", "config.yaml", "Path to config file.")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Loading config: %v", err)
	}

	ctx := context.Background()

	credFile := filepath.Join("credentials", "client_secret.json")
	tokenFile := filepath.Join("credentials", "token.json")

	client, err := calendar.NewClient(ctx, credFile, tokenFile)
	if err != nil {
		log.Fatalf("Creating calendar client: %v", err)
	}

	if *listCals {
		cals, err := client.ListCalendars(ctx)
		if err != nil {
			log.Fatalf("Listing calendars: %v", err)
		}
		fmt.Println("Available calendars:")
		for _, c := range cals {
			fmt.Printf("  %s\n", c)
		}
		return
	}

	weekStart, err := resolveWeekStart(*weekFlag)
	if err != nil {
		log.Fatalf("Invalid week: %v", err)
	}

	fmt.Printf("Generating schedule for week of %s...\n", weekStart.Format("2006-01-02"))

	// Fetch calendar IDs
	var calIDs []string
	for _, c := range cfg.Calendars {
		calIDs = append(calIDs, c.ID)
	}

	events, err := client.FetchWeekEvents(ctx, calIDs, weekStart)
	if err != nil {
		log.Fatalf("Fetching calendar events: %v", err)
	}
	fmt.Printf("Fetched %d calendar events.\n", len(events))

	schedule, err := engine.Generate(cfg, events, weekStart)
	if err != nil {
		log.Fatalf("Generating schedule: %v", err)
	}

	outDir := "schedules"
	htmlPath, err := render.RenderWeek(schedule, outDir)
	if err != nil {
		log.Fatalf("Rendering schedule: %v", err)
	}
	fmt.Printf("Schedule written to %s\n", htmlPath)

	// Serve locally
	absDir, _ := filepath.Abs(outDir)
	fmt.Printf("Serving at http://localhost:%d/%s\n", *port, filepath.Base(htmlPath))
	http.Handle("/", http.FileServer(http.Dir(absDir)))
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}

func resolveWeekStart(weekStr string) (time.Time, error) {
	if weekStr == "" {
		// Current week's Monday
		now := time.Now()
		offset := int(time.Monday - now.Weekday())
		if offset > 0 {
			offset -= 7
		}
		monday := now.AddDate(0, 0, offset)
		return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, now.Location()), nil
	}

	// Parse "2026-W17" format
	parts := strings.Split(weekStr, "-W")
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("expected format YYYY-Www, got %q", weekStr)
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid year: %w", err)
	}
	week, err := strconv.Atoi(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid week: %w", err)
	}

	// Find Jan 4 of the year (always in ISO week 1)
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.Local)
	offset := int(time.Monday - jan4.Weekday())
	if offset > 0 {
		offset -= 7
	}
	week1Monday := jan4.AddDate(0, 0, offset)
	return week1Monday.AddDate(0, 0, (week-1)*7), nil
}
