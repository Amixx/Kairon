package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gcal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

type Event struct {
	Summary   string
	Start     time.Time
	End       time.Time
	Calendar  string
	IsIgnored bool
}

type Client struct {
	service *gcal.Service
}

func NewClient(ctx context.Context, credentialsFile, tokenFile string) (*Client, error) {
	credBytes, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("reading credentials file: %w", err)
	}

	config, err := google.ConfigFromJSON(credBytes, gcal.CalendarReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}

	tok, err := tokenFromFile(tokenFile)
	if err != nil {
		tok, err = getTokenFromWeb(ctx, config)
		if err != nil {
			return nil, fmt.Errorf("obtaining token: %w", err)
		}
		if err := saveToken(tokenFile, tok); err != nil {
			return nil, fmt.Errorf("saving token: %w", err)
		}
	}

	svc, err := gcal.NewService(ctx, option.WithTokenSource(config.TokenSource(ctx, tok)))
	if err != nil {
		return nil, fmt.Errorf("creating calendar service: %w", err)
	}

	return &Client{service: svc}, nil
}

func (c *Client) FetchWeekEvents(ctx context.Context, calendarIDs []string, weekStart time.Time) ([]Event, error) {
	timeMin := weekStart.Format(time.RFC3339)
	timeMax := weekStart.AddDate(0, 0, 7).Format(time.RFC3339)

	var events []Event
	for _, calID := range calendarIDs {
		result, err := c.service.Events.List(calID).
			Context(ctx).
			TimeMin(timeMin).
			TimeMax(timeMax).
			SingleEvents(true).
			OrderBy("startTime").
			Do()
		if err != nil {
			return nil, fmt.Errorf("fetching events from %s: %w", calID, err)
		}

		for _, item := range result.Items {
			if item.Start.DateTime == "" {
				continue // skip all-day events
			}

			start, end, err := parseEventTimes(item)
			if err != nil {
				return nil, fmt.Errorf("parsing event %q: %w", item.Summary, err)
			}

			events = append(events, Event{
				Summary:  item.Summary,
				Start:    start,
				End:      end,
				Calendar: calID,
			})
		}
	}

	return events, nil
}

func (c *Client) ListCalendars(ctx context.Context) ([]string, error) {
	list, err := c.service.CalendarList.List().Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("listing calendars: %w", err)
	}

	var result []string
	for _, entry := range list.Items {
		result = append(result, fmt.Sprintf("%s (%s)", entry.Summary, entry.Id))
	}
	return result, nil
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tok := &oauth2.Token{}
	return tok, json.NewDecoder(f).Decode(tok)
}

func saveToken(path string, token *oauth2.Token) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating token file: %w", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

func parseEventTimes(item *gcal.Event) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, item.Start.DateTime)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing start time: %w", err)
	}
	end, err := time.Parse(time.RFC3339, item.End.DateTime)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing end time: %w", err)
	}
	return start, end, nil
}

func getTokenFromWeb(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	config.RedirectURL = "http://localhost:8085/callback"

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code in callback")
			fmt.Fprintln(w, "Error: no code received.")
			return
		}
		codeCh <- code
		fmt.Fprintln(w, "Authorization successful! You can close this tab.")
	})

	server := &http.Server{Addr: ":8085", Handler: mux}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Open the following URL in your browser:\n%s\n", authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		server.Shutdown(ctx)
		return nil, err
	case <-ctx.Done():
		server.Shutdown(ctx)
		return nil, ctx.Err()
	}

	server.Shutdown(ctx)

	tok, err := config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchanging code for token: %w", err)
	}
	return tok, nil
}
