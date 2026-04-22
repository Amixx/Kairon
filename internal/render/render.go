package render

import (
	"embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"github.com/amixx/kairon/internal/engine"
)

//go:embed templates/schedule.html
var templatesFS embed.FS

var typeColors = map[engine.ActivityType]string{
	engine.TypeUni:      "#4A90D9",
	engine.TypeWork:     "#E67E22",
	engine.TypeFitness:  "#27AE60",
	engine.TypeMeal:     "#F39C12",
	engine.TypeBreak:    "#BDC3C7",
	engine.TypePersonal: "#9B59B6",
	engine.TypeCommute:  "#7F8C8D",
}

var activityColors = map[string]string{
	"Self-Study/Admin/Projects": "#6FAEE8",
}

type dayColumn struct {
	Date         string // "Mon 21"
	ProductiveHM string // "5h30m" — productive time for the day
	Cells        []cell
}

type cell struct {
	Activity string
	Color    string
	Ignored  bool
	IsFirst  bool // first slot of a contiguous block
	IsLast   bool // last slot of a contiguous block
	Tooltip  string
}

type timeLabel struct {
	Label string
	Row   int
}

type legendEntry struct {
	Type  string
	Color string
}

type summaryEntry struct {
	Type    string
	Color   string
	Hours   string
	Minutes int
}

type templateData struct {
	Title               string
	Days                []dayColumn
	Times               []timeLabel
	NumRows             int
	Legend              []legendEntry
	Summary             []summaryEntry
	ProductivitySummary []summaryEntry
	TotalHours          string
}

func weekFileName(weekStart time.Time) string {
	year, week := weekStart.ISOWeek()
	return fmt.Sprintf("%d-W%02d.html", year, week)
}

func formatWeekTitle(weekStart time.Time) string {
	_, week := weekStart.ISOWeek()
	weekEnd := weekStart.AddDate(0, 0, 6)
	return fmt.Sprintf("Week %d — %s–%s",
		week,
		weekStart.Format("Jan 2"),
		weekEnd.Format("2, 2006"),
	)
}

// RenderWeek writes an HTML schedule file and returns its path.
func RenderWeek(schedule *engine.Schedule, outputDir string) (string, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	filename := weekFileName(schedule.WeekStart)
	outPath := filepath.Join(outputDir, filename)

	data := buildTemplateData(schedule)

	f, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if err := scheduleTemplate.Execute(f, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return outPath, nil
}

func buildTemplateData(schedule *engine.Schedule) templateData {
	slotsPerDay := (schedule.DayEnd - schedule.DayStart) / 15
	numDays := 7

	// Build slot lookup: [day][slotIndex] -> *Slot
	type slotKey struct {
		day  int
		slot int
	}
	lookup := make(map[slotKey]*engine.Slot)
	for i := range schedule.Slots {
		s := &schedule.Slots[i]
		day := int(s.Start.Weekday())
		// Convert Sunday=0 to 6, Mon=1 to 0, etc.
		day = (day + 6) % 7
		minuteOfDay := s.Start.Hour()*60 + s.Start.Minute()
		slotIdx := (minuteOfDay - schedule.DayStart) / 15
		if slotIdx >= 0 && slotIdx < slotsPerDay {
			lookup[slotKey{day, slotIdx}] = s
		}
	}

	// Build day columns
	days := make([]dayColumn, numDays)
	dayNames := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

	// Track time for summaries
	typeMinutes := make(map[engine.ActivityType]int)
	activityMinutes := make(map[string]int)
	productiveMinutes := 0
	regenerativeMinutes := 0

	for d := 0; d < numDays; d++ {
		date := schedule.WeekStart.AddDate(0, 0, d)
		days[d].Date = fmt.Sprintf("%s %d", dayNames[d], date.Day())
		days[d].Cells = make([]cell, slotsPerDay)
		dayProductiveMin := 0

		for r := 0; r < slotsPerDay; r++ {
			s, ok := lookup[slotKey{d, r}]
			if !ok {
				days[d].Cells[r] = cell{}
				continue
			}

			color := colorForSlot(s)

			// Count minutes per type
			if s.Activity != "" {
				typeMinutes[s.Type] += 15
				activityMinutes[s.Activity] += 15
				if isProductiveSlot(s) {
					productiveMinutes += 15
					dayProductiveMin += 15
				} else {
					regenerativeMinutes += 15
				}
			}

			// Determine block boundaries
			prevSame := false
			nextSame := false
			if r > 0 {
				if ps, ok := lookup[slotKey{d, r - 1}]; ok {
					prevSame = ps.Activity == s.Activity && ps.Type == s.Type && ps.Ignored == s.Ignored
				}
			}
			if r < slotsPerDay-1 {
				if ns, ok := lookup[slotKey{d, r + 1}]; ok {
					nextSame = ns.Activity == s.Activity && ns.Type == s.Type && ns.Ignored == s.Ignored
				}
			}

			// Compute tooltip for every cell in the block so the popup shows
			// regardless of which slot in the block the cursor is on. We walk
			// backwards/forwards to find the block's start/end.
			tooltip := ""
			if s.Activity != "" {
				blockStart := r
				for blockStart > 0 {
					ps, ok := lookup[slotKey{d, blockStart - 1}]
					if !ok || ps.Activity != s.Activity || ps.Type != s.Type || ps.Ignored != s.Ignored {
						break
					}
					blockStart--
				}
				blockEnd := r + 1
				for blockEnd < slotsPerDay {
					ns, ok := lookup[slotKey{d, blockEnd}]
					if !ok || ns.Activity != s.Activity || ns.Type != s.Type || ns.Ignored != s.Ignored {
						break
					}
					blockEnd++
				}
				startMin := schedule.DayStart + blockStart*15
				endMin := schedule.DayStart + blockEnd*15
				durMin := (blockEnd - blockStart) * 15
				durStr := fmt.Sprintf("%dmin", durMin)
				if durMin >= 60 {
					h := durMin / 60
					m := durMin % 60
					if m == 0 {
						durStr = fmt.Sprintf("%dh", h)
					} else {
						durStr = fmt.Sprintf("%dh%02dm", h, m)
					}
				}
				tooltip = fmt.Sprintf("%s · %02d:%02d–%02d:%02d · %s",
					s.Activity, startMin/60, startMin%60, endMin/60, endMin%60, durStr)
			}

			days[d].Cells[r] = cell{
				Activity: s.Activity,
				Color:    color,
				Ignored:  s.Ignored,
				IsFirst:  !prevSame,
				IsLast:   !nextSame,
				Tooltip:  tooltip,
			}
		}
		days[d].ProductiveHM = formatHM(dayProductiveMin)
	}

	// Build summary
	typeOrder := []struct {
		label       string
		typ         engine.ActivityType
		activity    string
		color       string
		useActivity bool
	}{
		{"Uni Classes", engine.TypeUni, "", typeColors[engine.TypeUni], false},
		{"Self-Study/Admin/Projects", engine.TypeUni, "Self-Study/Admin/Projects", activityColors["Self-Study/Admin/Projects"], true},
		{"Work", engine.TypeWork, "", typeColors[engine.TypeWork], false},
		{"Fitness", engine.TypeFitness, "", typeColors[engine.TypeFitness], false},
		{"Meal", engine.TypeMeal, "", typeColors[engine.TypeMeal], false},
		{"Break", engine.TypeBreak, "", typeColors[engine.TypeBreak], false},
		{"Personal", engine.TypePersonal, "", typeColors[engine.TypePersonal], false},
		{"Commute", engine.TypeCommute, "", typeColors[engine.TypeCommute], false},
	}
	var summary []summaryEntry
	totalMin := 0
	for _, to := range typeOrder {
		mins := 0
		if to.useActivity {
			mins = activityMinutes[to.activity]
		} else if to.typ == engine.TypeUni {
			mins = typeMinutes[to.typ] - activityMinutes["Self-Study/Admin/Projects"]
		} else {
			mins = typeMinutes[to.typ]
		}
		if mins == 0 {
			continue
		}
		totalMin += mins
		summary = append(summary, summaryEntry{
			Type:    to.label,
			Color:   to.color,
			Hours:   fmt.Sprintf("%dh%02dm", mins/60, mins%60),
			Minutes: mins,
		})
	}
	totalHours := fmt.Sprintf("%dh%02dm", totalMin/60, totalMin%60)
	productivitySummary := []summaryEntry{
		{
			Type:    "Productive",
			Color:   "#2D8CFF",
			Hours:   fmt.Sprintf("%dh%02dm", productiveMinutes/60, productiveMinutes%60),
			Minutes: productiveMinutes,
		},
		{
			Type:    "Regenerative",
			Color:   "#AAB2BD",
			Hours:   fmt.Sprintf("%dh%02dm", regenerativeMinutes/60, regenerativeMinutes%60),
			Minutes: regenerativeMinutes,
		},
	}

	// Build time labels (every hour)
	var times []timeLabel
	for r := 0; r < slotsPerDay; r++ {
		mins := schedule.DayStart + r*15
		if mins%60 == 0 {
			times = append(times, timeLabel{
				Label: fmt.Sprintf("%02d:%02d", mins/60, mins%60),
				Row:   r,
			})
		}
	}
	// Also add the very first label if DayStart is not on the hour
	if schedule.DayStart%60 != 0 {
		first := timeLabel{
			Label: fmt.Sprintf("%02d:%02d", schedule.DayStart/60, schedule.DayStart%60),
			Row:   0,
		}
		times = append([]timeLabel{first}, times...)
	}

	// Legend
	legend := []legendEntry{
		{"Uni", typeColors[engine.TypeUni]},
		{"Self-Study/Admin/Projects", activityColors["Self-Study/Admin/Projects"]},
		{"Work", typeColors[engine.TypeWork]},
		{"Fitness", typeColors[engine.TypeFitness]},
		{"Meal", typeColors[engine.TypeMeal]},
		{"Break", typeColors[engine.TypeBreak]},
		{"Personal", typeColors[engine.TypePersonal]},
		{"Commute", typeColors[engine.TypeCommute]},
	}

	return templateData{
		Title:               formatWeekTitle(schedule.WeekStart),
		Days:                days,
		Times:               times,
		NumRows:             slotsPerDay,
		Legend:              legend,
		Summary:             summary,
		ProductivitySummary: productivitySummary,
		TotalHours:          totalHours,
	}
}

func colorForSlot(s *engine.Slot) string {
	if color, ok := activityColors[s.Activity]; ok {
		return color
	}
	color := typeColors[s.Type]
	if color == "" {
		return "#EEEEEE"
	}
	return color
}

func formatHM(mins int) string {
	return fmt.Sprintf("%dh%02dm", mins/60, mins%60)
}

func isProductiveSlot(s *engine.Slot) bool {
	switch s.Type {
	case engine.TypeWork:
		return true
	case engine.TypeUni:
		return true
	default:
		return false
	}
}

var templateFuncs = template.FuncMap{
	"intRange": func(n int) []int {
		s := make([]int, n)
		for i := range s {
			s[i] = i
		}
		return s
	},
	"isHourStart": func(row int, times []timeLabel) bool {
		for _, t := range times {
			if t.Row == row {
				return true
			}
		}
		return false
	},
	"timeLabel": func(row int, times []timeLabel) string {
		for _, t := range times {
			if t.Row == row {
				return t.Label
			}
		}
		return ""
	},
}

var scheduleTemplate = template.Must(
	template.New("schedule.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/schedule.html"),
)
