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
				Name:            "Work",
				Type:            "work",
				Duration:        120,
				MinDuration:     90,
				HoursPerWeek:    20,
				MinHoursPerWeek: 19,
				MaxHoursPerWeek: 21,
				Priority:        "critical",
				Earliest:        "07:30",
				Latest:          "21:00",
				PreferredTime:   "morning",
				Constraints: []string{
					"consolidate_days",
					"avoid_window:fri:1800:2100:8",
					"avoid_window:sat:0730:1200:4",
					"avoid_window:sat:1800:2100:6",
					"avoid_window:sun:0730:1200:5",
				},
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
				Constraints: []string{
					"no_three_consecutive_gym_days",
					"prefer_late",
					"day_earliest:wed:13:45",
				},
				AllowedDays:   []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
				PreferredDays: []string{"mon", "tue", "wed", "thu", "fri", "sat"},
			},
			{
				Name:          "Easy Run",
				Type:          "fitness",
				Duration:      75,
				Frequency:     1,
				Priority:      "important",
				Earliest:      "07:30",
				Latest:        "18:45",
				PreferredTime: "18:15",
				PreferredDay:  "thu",
				Location:      "home",
				Constraints:   []string{"prefer_late"},
			},
			{
				Name:            "Self-Study/Admin/Projects",
				Type:            "uni",
				Duration:        60,
				MinDuration:     45,
				HoursPerWeek:    18,
				MinHoursPerWeek: 14,
				MaxHoursPerWeek: 18,
				Priority:        "mid",
				Earliest:        "07:30",
				Latest:          "21:00",
				Constraints: []string{
					"prefer_fuller_quota",
					"avoid_window:fri:1800:2100:4",
					"avoid_window:sat:0730:1200:4",
					"avoid_window:sat:1800:2100:5",
					"avoid_window:sun:0730:1200:5",
				},
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

// fixtureEvents returns a deterministic synthetic campus week for unit tests.
// Production scheduling uses Google Calendar events fetched at runtime.
func fixtureEvents() []calendar.Event {
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
		ev(0, "Campus Block Mon", 13, 30, 17, 30),    // Mon
		ev(1, "Campus Block Tue AM", 9, 45, 11, 15),  // Tue
		ev(1, "Campus Block Tue PM", 15, 0, 16, 30),  // Tue
		ev(2, "Campus Block Wed", 11, 30, 13, 0),     // Wed
		ev(3, "Campus Block Thu AM", 9, 0, 11, 30),   // Thu
		ev(3, "Campus Block Thu PM", 13, 15, 14, 45), // Thu
		ev(4, "Campus Block Fri AM", 11, 30, 13, 0),  // Fri
		ev(4, "Campus Block Fri PM", 13, 15, 14, 45), // Fri
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

func activityStartSlot(s *Schedule, day int, activity string) int {
	slotsPerDay := (s.DayEnd - s.DayStart) / 15
	for slot := 0; slot < slotsPerDay; slot++ {
		gi := day*slotsPerDay + slot
		if gi >= len(s.Slots) || s.Slots[gi].Activity != activity {
			continue
		}
		if slot == 0 || s.Slots[gi-1].Activity != activity {
			return slot
		}
	}
	return -1
}

func activityBlocks(s *Schedule, day int, activity string) [][2]int {
	slotsPerDay := (s.DayEnd - s.DayStart) / 15
	var blocks [][2]int
	start := -1
	for slot := 0; slot < slotsPerDay; slot++ {
		gi := day*slotsPerDay + slot
		matches := gi < len(s.Slots) && s.Slots[gi].Activity == activity
		if matches && start == -1 {
			start = slot
			continue
		}
		if !matches && start != -1 {
			blocks = append(blocks, [2]int{start, slot})
			start = -1
		}
	}
	if start != -1 {
		blocks = append(blocks, [2]int{start, slotsPerDay})
	}
	return blocks
}

func workTypeBlocks(s *Schedule, day int) [][2]int {
	slotsPerDay := (s.DayEnd - s.DayStart) / 15
	var blocks [][2]int
	start := -1
	hasWork := false
	for slot := 0; slot < slotsPerDay; slot++ {
		gi := day*slotsPerDay + slot
		matches := gi < len(s.Slots) && s.Slots[gi].Type == TypeWork
		if matches && start == -1 {
			start = slot
			hasWork = s.Slots[gi].Activity == "Work"
			continue
		}
		if matches {
			hasWork = hasWork || s.Slots[gi].Activity == "Work"
			continue
		}
		if start != -1 && hasWork {
			blocks = append(blocks, [2]int{start, slot})
		}
		start = -1
		hasWork = false
	}
	if start != -1 && hasWork {
		blocks = append(blocks, [2]int{start, slotsPerDay})
	}
	return blocks
}

func TestScheduleInvariants(t *testing.T) {
	cfg := baseConfig()
	events := fixtureEvents()
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
	minWorkSlots := 19 * 60 / 15
	maxWorkSlots := 21 * 60 / 15
	if totalWorkSlots < minWorkSlots || totalWorkSlots > maxWorkSlots {
		t.Errorf("Total Work: got %d slots (%d min), want between %d and %d slots (%d-%d min = 19h-21h)",
			totalWorkSlots, totalWorkSlots*15, minWorkSlots, maxWorkSlots, minWorkSlots*15, maxWorkSlots*15)
	}
	for d := 0; d < 7; d++ {
		for _, block := range workTypeBlocks(schedule, d) {
			durationMin := (block[1] - block[0]) * 15
			if durationMin < 90 {
				t.Errorf("%s: work span %02d:%02d-%02d:%02d is only %d min (want >= 90, counting Work Meeting as work)",
					dayNames[d],
					(schedule.DayStart+block[0]*15)/60, (schedule.DayStart+block[0]*15)%60,
					(schedule.DayStart+block[1]*15)/60, (schedule.DayStart+block[1]*15)%60,
					durationMin)
			}
		}
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

	for d := 0; d < 7; d++ {
		gymBlocks := activityBlocks(schedule, d, "Gym")
		if len(gymBlocks) == 0 {
			continue
		}
		gymStart := gymBlocks[0][0]
		for _, meal := range []string{"Breakfast", "Lunch", "Dinner"} {
			for _, block := range activityBlocks(schedule, d, meal) {
				mealEnd := block[1]
				if mealEnd <= gymStart {
					gapMin := (gymStart - mealEnd) * 15
					if gapMin < 60 {
						t.Errorf("%s: %s ends only %d min before Gym (want >= 60)",
							dayNames[d], meal, gapMin)
					}
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

	// --- Flexible productive bucket stays present and avoids tiny scraps ---
	totalStudySlots := 0
	for d := 0; d < 7; d++ {
		totalStudySlots += byDay[d]["Self-Study/Admin/Projects"]
	}
	if totalStudySlots == 0 {
		t.Errorf("Expected some Self-Study/Admin/Projects time, got 0 slots")
	}
	for d := 0; d < 7; d++ {
		for _, block := range activityBlocks(schedule, d, "Self-Study/Admin/Projects") {
			durationMin := (block[1] - block[0]) * 15
			if durationMin < 45 {
				t.Errorf("%s: Self-Study/Admin/Projects block %02d:%02d-%02d:%02d is only %d min (productive blocks under 45 min should be avoided)",
					dayNames[d],
					(schedule.DayStart+block[0]*15)/60, (schedule.DayStart+block[0]*15)%60,
					(schedule.DayStart+block[1]*15)/60, (schedule.DayStart+block[1]*15)%60,
					durationMin)
			}
		}
	}

	totalProductiveSlots := 0
	for _, slot := range schedule.Slots {
		if slot.Ignored {
			continue
		}
		if slot.Type == TypeWork || slot.Type == TypeUni {
			totalProductiveSlots++
		}
	}
	// Total productive time targets ~50-55h/week, enforced as a soft penalty
	// in the LP rather than a hard bound — per-activity quotas can cap the
	// achievable total below 50h, and we'd rather the solver return a good
	// schedule than fail. Log for visibility; fail only on extreme deviation.
	t.Logf("Total productive time: %d slots (%d min = %.1fh)",
		totalProductiveSlots, totalProductiveSlots*15, float64(totalProductiveSlots)*15/60)
	sanityMin := 40 * 60 / 15
	sanityMax := 60 * 60 / 15
	if totalProductiveSlots < sanityMin || totalProductiveSlots > sanityMax {
		t.Errorf("Total productive time: got %d slots (%.1fh), outside sanity range %d-%d slots (%dh-%dh)",
			totalProductiveSlots, float64(totalProductiveSlots)*15/60,
			sanityMin, sanityMax, sanityMin*15/60, sanityMax*15/60)
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
	for d := 0; d < 7; d++ {
		runBlocks := activityBlocks(schedule, d, "Easy Run")
		if len(runBlocks) == 0 {
			continue
		}
		runEnd := runBlocks[0][1]
		runEndMin := schedule.DayStart + runEnd*15
		if runEndMin > 18*60+45 {
			t.Errorf("%s: Easy Run ends at %02d:%02d, want it done by 18:45 so it never falls after dinner",
				dayNames[d], runEndMin/60, runEndMin%60)
		}
		dinnerBlocks := activityBlocks(schedule, d, "Dinner")
		if len(dinnerBlocks) > 0 {
			dinnerStartMin := schedule.DayStart + dinnerBlocks[0][0]*15
			if runEndMin > dinnerStartMin {
				t.Errorf("%s: Easy Run ends at %02d:%02d after Dinner starts at %02d:%02d",
					dayNames[d], runEndMin/60, runEndMin%60, dinnerStartMin/60, dinnerStartMin%60)
			}
		}
	}

	wedGymStart := activityStartSlot(schedule, 2, "Gym")
	if wedGymStart == -1 {
		t.Fatalf("expected a Wednesday Gym session")
	}
	wedGymStartMin := schedule.DayStart + wedGymStart*15
	if wedGymStartMin != 13*60+45 {
		t.Errorf("Wed Gym starts at %02d:%02d, want 13:45 right after Tutorium commute",
			wedGymStartMin/60, wedGymStartMin%60)
	}

	wedLunchStart := activityStartSlot(schedule, 2, "Lunch")
	if wedLunchStart == -1 {
		t.Fatalf("expected a Wednesday Lunch session")
	}
	wedLunchStartMin := schedule.DayStart + wedLunchStart*15
	if wedLunchStartMin < 16*60 {
		t.Errorf("Wed Lunch starts at %02d:%02d, want it after the late gym block",
			wedLunchStartMin/60, wedLunchStartMin%60)
	}

	for d := 0; d < 7; d++ {
		gymStart := activityStartSlot(schedule, d, "Gym")
		if gymStart != -1 && gymStart == 0 {
			t.Errorf("%s Gym still starts at %02d:%02d; expected gym to avoid the first slot of the day",
				dayNames[d], schedule.DayStart/60, schedule.DayStart%60)
		}
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
