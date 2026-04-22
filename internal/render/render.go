package render

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"github.com/amixx/kairon/internal/engine"
)

var typeColors = map[engine.ActivityType]string{
	engine.TypeUni:      "#4A90D9",
	engine.TypeWork:     "#E67E22",
	engine.TypeFitness:  "#27AE60",
	engine.TypeMeal:     "#F39C12",
	engine.TypeBreak:    "#BDC3C7",
	engine.TypePersonal: "#9B59B6",
	engine.TypeCommute:  "#7F8C8D",
}

type dayColumn struct {
	Date  string // "Mon 21"
	Cells []cell
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
	Type     string
	Color    string
	Hours    string
	Minutes  int
}

type templateData struct {
	Title      string
	Days       []dayColumn
	Times      []timeLabel
	NumRows    int
	Legend     []legendEntry
	Summary    []summaryEntry
	TotalHours string
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

	// Track time per activity type for summary
	typeMinutes := make(map[engine.ActivityType]int)

	for d := 0; d < numDays; d++ {
		date := schedule.WeekStart.AddDate(0, 0, d)
		days[d].Date = fmt.Sprintf("%s %d", dayNames[d], date.Day())
		days[d].Cells = make([]cell, slotsPerDay)

		for r := 0; r < slotsPerDay; r++ {
			s, ok := lookup[slotKey{d, r}]
			if !ok {
				days[d].Cells[r] = cell{}
				continue
			}

			color := typeColors[s.Type]
			if color == "" {
				color = "#EEEEEE"
			}

			// Count minutes per type
			if s.Activity != "" {
				typeMinutes[s.Type] += 15
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

			// Compute tooltip on the first slot of a block
			tooltip := ""
			if !prevSame && s.Activity != "" {
				// Find block end
				blockEnd := r + 1
				for blockEnd < slotsPerDay {
					ns, ok := lookup[slotKey{d, blockEnd}]
					if !ok || ns.Activity != s.Activity || ns.Type != s.Type || ns.Ignored != s.Ignored {
						break
					}
					blockEnd++
				}
				startMin := schedule.DayStart + r*15
				endMin := schedule.DayStart + blockEnd*15
				durMin := (blockEnd - r) * 15
				tooltip = fmt.Sprintf("%s — %02d:%02d–%02d:%02d (%dmin)",
					s.Activity, startMin/60, startMin%60, endMin/60, endMin%60, durMin)
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
	}

	// Build summary
	typeOrder := []struct {
		label string
		typ   engine.ActivityType
	}{
		{"Uni", engine.TypeUni},
		{"Work", engine.TypeWork},
		{"Fitness", engine.TypeFitness},
		{"Meal", engine.TypeMeal},
		{"Break", engine.TypeBreak},
		{"Personal", engine.TypePersonal},
		{"Commute", engine.TypeCommute},
	}
	var summary []summaryEntry
	totalMin := 0
	for _, to := range typeOrder {
		mins := typeMinutes[to.typ]
		if mins == 0 {
			continue
		}
		totalMin += mins
		summary = append(summary, summaryEntry{
			Type:    to.label,
			Color:   typeColors[to.typ],
			Hours:   fmt.Sprintf("%dh%02dm", mins/60, mins%60),
			Minutes: mins,
		})
	}
	totalHours := fmt.Sprintf("%dh%02dm", totalMin/60, totalMin%60)

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
		{"Work", typeColors[engine.TypeWork]},
		{"Fitness", typeColors[engine.TypeFitness]},
		{"Meal", typeColors[engine.TypeMeal]},
		{"Break", typeColors[engine.TypeBreak]},
		{"Personal", typeColors[engine.TypePersonal]},
		{"Commute", typeColors[engine.TypeCommute]},
	}

	return templateData{
		Title:      formatWeekTitle(schedule.WeekStart),
		Days:       days,
		Times:      times,
		NumRows:    slotsPerDay,
		Legend:     legend,
		Summary:    summary,
		TotalHours: totalHours,
	}
}

const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Title}}</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    background: #F5F6FA;
    color: #2c3e50;
    padding: 24px;
  }
  .container { max-width: 1200px; margin: 0 auto; }
  h1 {
    text-align: center;
    font-size: 1.5rem;
    font-weight: 600;
    margin-bottom: 20px;
    color: #34495e;
  }
  .grid-wrapper { overflow-x: auto; }
  table {
    border-collapse: collapse;
    width: 100%;
    table-layout: fixed;
  }
  th {
    background: #34495e;
    color: #fff;
    font-weight: 500;
    font-size: 0.85rem;
    padding: 8px 4px;
    text-align: center;
    position: sticky;
    top: 0;
    z-index: 2;
  }
  th.time-header { width: 56px; }
  td {
    height: 20px;
    font-size: 0.7rem;
    text-align: center;
    vertical-align: middle;
    border-left: 1px solid #e0e0e0;
    border-bottom: 1px solid #e8e8e8;
    padding: 0 2px;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  td.time-cell {
    background: #fff;
    font-size: 0.7rem;
    color: #7f8c8d;
    text-align: right;
    padding-right: 6px;
    border-bottom: 1px solid #ddd;
    font-variant-numeric: tabular-nums;
  }
  td.slot {
    color: #fff;
    font-weight: 500;
    text-shadow: 0 1px 1px rgba(0,0,0,0.2);
  }
  td.slot.empty {
    background: #fff;
    color: transparent;
    text-shadow: none;
  }
  td.slot.block-first {
    border-top: 1px solid rgba(0,0,0,0.12);
  }
  td.slot.block-mid {
    border-bottom-color: transparent;
  }
  td.slot.block-last {
    border-bottom: 1px solid rgba(0,0,0,0.12);
  }
  td.slot.ignored {
    opacity: 0.35;
    background-image: repeating-linear-gradient(
      135deg,
      transparent,
      transparent 4px,
      rgba(255,255,255,0.3) 4px,
      rgba(255,255,255,0.3) 8px
    ) !important;
  }
  /* Hour lines */
  tr.hour-start td {
    border-top: 1px solid #ccc;
  }
  .legend {
    margin-top: 24px;
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 16px;
  }
  .legend-item {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 0.8rem;
    color: #555;
  }
  .legend-swatch {
    width: 16px;
    height: 16px;
    border-radius: 3px;
  }
  .summary {
    margin-top: 24px;
    text-align: center;
  }
  .summary h2 {
    font-size: 1rem;
    font-weight: 600;
    color: #34495e;
    margin-bottom: 12px;
  }
  .summary-grid {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 16px;
  }
  .summary-item {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 0.8rem;
    color: #555;
  }
  .summary-swatch {
    width: 12px;
    height: 12px;
    border-radius: 2px;
  }
  .summary-hours {
    font-weight: 600;
    color: #2c3e50;
  }
  td.slot[title] { cursor: default; }
</style>
</head>
<body>
<div class="container">
  <h1>{{.Title}}</h1>
  <div class="grid-wrapper">
    <table>
      <thead>
        <tr>
          <th class="time-header"></th>
          {{- range .Days}}
          <th>{{.Date}}</th>
          {{- end}}
        </tr>
      </thead>
      <tbody>
        {{- range $r := intRange $.NumRows}}
        <tr{{if isHourStart $r $.Times}} class="hour-start"{{end}}>
          <td class="time-cell">{{timeLabel $r $.Times}}</td>
          {{- range $d := $.Days}}
          {{- with index $d.Cells $r}}
          {{- if eq .Activity ""}}
          <td class="slot empty"></td>
          {{- else}}
          <td class="slot{{if .IsFirst}} block-first{{end}}{{if not .IsLast}} block-mid{{end}}{{if .IsLast}} block-last{{end}}{{if .Ignored}} ignored{{end}}" style="background-color: {{.Color}};"{{if .Tooltip}} title="{{.Tooltip}}"{{end}}>{{if .IsFirst}}{{.Activity}}{{end}}</td>
          {{- end}}
          {{- end}}
          {{- end}}
        </tr>
        {{- end}}
      </tbody>
    </table>
  </div>
  <div class="legend">
    {{- range .Legend}}
    <div class="legend-item">
      <div class="legend-swatch" style="background: {{.Color}};"></div>
      {{.Type}}
    </div>
    {{- end}}
  </div>
  <div class="summary">
    <h2>Weekly Summary — {{.TotalHours}} total</h2>
    <div class="summary-grid">
      {{- range .Summary}}
      <div class="summary-item">
        <div class="summary-swatch" style="background: {{.Color}};"></div>
        <span class="summary-type">{{.Type}}</span>
        <span class="summary-hours">{{.Hours}}</span>
      </div>
      {{- end}}
    </div>
  </div>
</div>
</body>
</html>`

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
	template.New("schedule").Funcs(templateFuncs).Parse(htmlTemplate),
)
