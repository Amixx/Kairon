package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/amixx/kairon/internal/calendar"
	"github.com/amixx/kairon/internal/config"
)

// baseConfig returns a minimal config for testing.
func baseConfig() *config.Config {
	return &config.Config{
		Day: config.DayConfig{
			Start:       "07:30",
			End:         "21:00",
			Granularity: 15,
		},
		CommuteBuffer: 45,
		MinMealGap:    180,
		Activities: []config.Activity{
			{
				Name:          "Work",
				Type:          "work",
				Duration:      120,
				MinDuration:   90,
				HoursPerWeek:  20,
				Priority:      "critical",
				Earliest:      "07:30",
				Latest:        "21:00",
				PreferredTime: "morning",
				Constraints:   []string{"consolidate_days"},
				AllowedDays:   []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
				PreferredDays: []string{"mon", "tue", "wed", "thu", "fri"},
			},
			{
				Name:      "Gym",
				Type:      "fitness",
				Duration:  135,
				Frequency: 4,
				Priority:  "important",
				Earliest:  "07:30",
				Latest:    "16:00",
				AllowedDays:   []string{"mon", "tue", "wed", "thu", "fri", "sun"},
				PreferredDays: []string{"mon", "tue", "wed", "thu", "fri"},
			},
			{
				Name:         "Easy Run",
				Type:         "fitness",
				Duration:     75,
				Frequency:    1,
				Priority:     "important",
				Earliest:     "07:30",
				Latest:       "21:00",
				PreferredDay: "thu",
				Location:     "home",
			},
			{
				Name:          "Self-Study",
				Type:          "uni",
				Duration:      60,
				MinDuration:   30,
				HoursPerWeek:  5,
				Priority:      "mid",
				Earliest:      "07:30",
				Latest:        "21:00",
			},
			{
				Name:          "Breakfast",
				Type:          "meal",
				Duration:      30,
				Frequency:     7,
				Priority:      "important",
				PreferredTime: "10:00",
				Latest:        "10:30",
			},
			{
				Name:          "Lunch",
				Type:          "meal",
				Duration:      30,
				Frequency:     7,
				Priority:      "important",
				PreferredTime: "13:00",
			},
			{
				Name:          "Dinner",
				Type:          "meal",
				Duration:      30,
				Frequency:     7,
				Priority:      "important",
				PreferredTime: "19:00",
				Latest:        "20:00",
			},
		},
		FixedEvents: []config.FixedEvent{
			{Name: "Work Meeting", Day: "thu", Start: "16:00", End: "16:30", Type: "work"},
		},
	}
}

// mondayOf returns a Monday for testing.
func mondayOf() time.Time {
	return time.Date(2026, 4, 20, 0, 0, 0, 0, time.Local)
}

// typicalEvents returns uni events for a typical week.
func typicalEvents() []calendar.Event {
	ws := mondayOf()
	loc := ws.Location()
	ev := func(day int, summary string, startH, startM, endH, endM int) calendar.Event {
		d := ws.AddDate(0, 0, day)
		return calendar.Event{
			Summary:  summary,
			Start:    time.Date(d.Year(), d.Month(), d.Day(), startH, startM, 0, 0, loc),
			End:      time.Date(d.Year(), d.Month(), d.Day(), endH, endM, 0, 0, loc),
			Calendar: "TUM",
		}
	}
	return []calendar.Event{
		ev(0, "Geo Sensor/GeoInfo3", 13, 30, 17, 30),            // Mon
		ev(1, "Semantic Modeling", 9, 45, 11, 15),                // Tue
		ev(1, "Räuml./AngeoInfo", 15, 0, 16, 30),                // Tue
		ev(2, "PM Tutorium", 11, 30, 13, 0),                     // Wed
		ev(3, "Deutsch A1.2", 9, 0, 11, 30),                     // Thu
		ev(3, "Räuml. Modellierung", 13, 15, 14, 45),            // Thu
		ev(4, "BIM.fundamentals VO", 11, 30, 13, 0),             // Fri
		ev(4, "BIM UE", 13, 15, 14, 45),                         // Fri
	}
}

// scheduleSlotsByDay groups schedule slots by day and activity.
func scheduleSlotsByDay(s *Schedule) map[int]map[string]int {
	slotsPerDay := (s.DayEnd - s.DayStart) / 15
	result := make(map[int]map[string]int)
	for d := 0; d < 7; d++ {
		result[d] = make(map[string]int)
	}
	for gi, slot := range s.Slots {
		if slot.Activity == "" || slot.Activity == "Break" {
			continue
		}
		day := gi / slotsPerDay
		if day < 7 {
			result[day][slot.Activity]++
		}
	}
	return result
}

func TestScheduleInvariants(t *testing.T) {
	cfg := baseConfig()
	events := typicalEvents()
	ws := mondayOf()

	schedule, err := Generate(cfg, events, ws)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	slotsPerDay := (schedule.DayEnd - schedule.DayStart) / 15
	byDay := scheduleSlotsByDay(schedule)
	dayNames := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

	// --- Meals: exactly once per day ---
	for _, meal := range []string{"Breakfast", "Lunch", "Dinner"} {
		for d := 0; d < 7; d++ {
			count := byDay[d][meal]
			durSlots := 30 / 15 // 2 slots
			if count != durSlots {
				t.Errorf("%s on %s: got %d slots (want %d = one %dmin meal)",
					meal, dayNames[d], count, durSlots, 30)
			}
		}
	}

	// --- No two gym sessions on the same day ---
	for d := 0; d < 7; d++ {
		gymSlots := byDay[d]["Gym"]
		maxOneDuration := 135 / 15 // 9 slots
		if gymSlots > maxOneDuration {
			t.Errorf("Day %s has %d Gym slots (%d min) — looks like 2 sessions",
				dayNames[d], gymSlots, gymSlots*15)
		}
	}

	// --- Total gym sessions = 4 per week ---
	totalGymSlots := 0
	for d := 0; d < 7; d++ {
		totalGymSlots += byDay[d]["Gym"]
	}
	expectedGymSlots := 4 * (135 / 15)
	if totalGymSlots != expectedGymSlots {
		t.Errorf("Total Gym: got %d slots (%d min), want %d slots (%d min = 4 sessions)",
			totalGymSlots, totalGymSlots*15, expectedGymSlots, expectedGymSlots*15)
	}

	// --- Work total = 20 hours (including fixed Work Meeting) ---
	totalWorkSlots := 0
	for d := 0; d < 7; d++ {
		totalWorkSlots += byDay[d]["Work"]
		totalWorkSlots += byDay[d]["Work Meeting"]
	}
	expectedWorkSlots := 20 * 60 / 15
	if totalWorkSlots != expectedWorkSlots {
		t.Errorf("Total Work: got %d slots (%d min), want %d slots (%d min = 20h)",
			totalWorkSlots, totalWorkSlots*15, expectedWorkSlots, expectedWorkSlots*15)
	}

	// --- No commute adjacent to work only (commute must be adjacent to uni) ---
	for gi, slot := range schedule.Slots {
		if slot.Type != TypeCommute {
			continue
		}
		day := gi / slotsPerDay
		// Check that this day has uni events
		hasUni := false
		for s := 0; s < slotsPerDay; s++ {
			dayGi := day*slotsPerDay + s
			if dayGi < len(schedule.Slots) && schedule.Slots[dayGi].Type == TypeUni {
				hasUni = true
				break
			}
		}
		if !hasUni {
			t.Errorf("Commute on %s (slot %d) but no uni events that day",
				dayNames[day], gi)
		}
	}

	// --- Minimum meal gap: meals on same day at least 3h apart ---
	for d := 0; d < 7; d++ {
		var mealStarts []struct {
			name string
			slot int
		}
		for s := 0; s < slotsPerDay; s++ {
			gi := d*slotsPerDay + s
			if gi >= len(schedule.Slots) {
				continue
			}
			sl := schedule.Slots[gi]
			if sl.Type == TypeMeal {
				// Check if this is the first slot of a meal block
				if s == 0 || schedule.Slots[gi-1].Activity != sl.Activity {
					mealStarts = append(mealStarts, struct {
						name string
						slot int
					}{sl.Activity, s})
				}
			}
		}
		for i := 0; i < len(mealStarts); i++ {
			for j := i + 1; j < len(mealStarts); j++ {
				gapMins := (mealStarts[j].slot - mealStarts[i].slot) * 15
				if gapMins < 180 {
					t.Errorf("%s: %s (slot %d) and %s (slot %d) only %d min apart (want >= 180)",
						dayNames[d], mealStarts[i].name, mealStarts[i].slot,
						mealStarts[j].name, mealStarts[j].slot, gapMins)
				}
			}
		}
	}

	// --- Breakfast must end by 10:30 ---
	for d := 0; d < 7; d++ {
		for s := 0; s < slotsPerDay; s++ {
			gi := d*slotsPerDay + s
			if gi >= len(schedule.Slots) {
				continue
			}
			if schedule.Slots[gi].Activity == "Breakfast" {
				slotEndMin := schedule.DayStart + (s+1)*15
				if slotEndMin > 10*60+30 {
					t.Errorf("%s: Breakfast at slot %d ends at %02d:%02d (must end by 10:30)",
						dayNames[d], s, slotEndMin/60, slotEndMin%60)
				}
			}
		}
	}

	// --- Self-Study total = 5 hours ---
	totalStudySlots := 0
	for d := 0; d < 7; d++ {
		totalStudySlots += byDay[d]["Self-Study"]
	}
	expectedStudySlots := 5 * 60 / 15
	if totalStudySlots != expectedStudySlots {
		t.Errorf("Total Self-Study: got %d slots (%d min), want %d slots (%d min = 5h)",
			totalStudySlots, totalStudySlots*15, expectedStudySlots, expectedStudySlots*15)
	}

	// --- Easy Run: exactly 1 per week ---
	totalRunSlots := 0
	for d := 0; d < 7; d++ {
		totalRunSlots += byDay[d]["Easy Run"]
	}
	expectedRunSlots := 75 / 15
	if totalRunSlots != expectedRunSlots {
		t.Errorf("Total Easy Run: got %d slots (%d min), want %d slots (%d min)",
			totalRunSlots, totalRunSlots*15, expectedRunSlots, expectedRunSlots*15)
	}

	// --- Print schedule summary for debugging ---
	t.Log("Schedule summary:")
	for d := 0; d < 7; d++ {
		var parts []string
		for s := 0; s < slotsPerDay; s++ {
			gi := d*slotsPerDay + s
			if gi >= len(schedule.Slots) {
				continue
			}
			sl := schedule.Slots[gi]
			// Print first slot of each block
			if s == 0 || schedule.Slots[gi-1].Activity != sl.Activity {
				startMin := schedule.DayStart + s*15
				parts = append(parts, strings.Repeat(" ", 0)+
					sl.Activity+
					"@"+
					timeStr(startMin))
			}
		}
		t.Logf("  %s: %s", dayNames[d], strings.Join(parts, " → "))
	}
}

func timeStr(mins int) string {
	return strings.TrimSpace(strings.Replace(
		strings.Replace(
			time.Date(0, 0, 0, mins/60, mins%60, 0, 0, time.UTC).Format("15:04"),
			"0", "0", 1),
		"  ", " ", 1))
}
