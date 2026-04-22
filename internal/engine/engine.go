package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amixx/kairon/internal/calendar"
	"github.com/amixx/kairon/internal/config"
)

// slotInfo describes a single 15-min slot in the week grid.
type slotInfo struct {
	day       int // 0=Mon .. 6=Sun
	index     int // slot index within the day
	startMins int // minutes from midnight
}

// actInstance is one instance of a frequency-based activity (e.g. gym1, gym2).
type actInstance struct {
	name     string // display name
	act      config.Activity
	duration int // in slots
	id       string
}

type GenerateOptions struct {
	AuditModel bool
}

type modelAudit struct {
	ConstraintCount  int
	BinaryVarCount   int
	ConstraintGroups map[string]int
	VariableGroups   map[string]int
}

type penaltyWindow struct {
	start   int
	end     int
	penalty int
}

// Generate builds a weekly schedule using MILP via CBC.
func Generate(cfg *config.Config, events []calendar.Event, weekStart time.Time) (*Schedule, error) {
	return GenerateWithOptions(cfg, events, weekStart, GenerateOptions{})
}

// GenerateWithOptions builds a weekly schedule using MILP via CBC with optional diagnostics.
func GenerateWithOptions(cfg *config.Config, events []calendar.Event, weekStart time.Time, opts GenerateOptions) (*Schedule, error) {
	gran := cfg.Day.Granularity
	dayStartMin := cfg.Day.StartMinutes()
	dayEndMin := cfg.Day.EndMinutes()
	slotsPerDay := (dayEndMin - dayStartMin) / gran

	// Build the slot grid
	slots := make([]slotInfo, 0, 7*slotsPerDay)
	for d := 0; d < 7; d++ {
		for s := 0; s < slotsPerDay; s++ {
			slots = append(slots, slotInfo{
				day:       d,
				index:     s,
				startMins: dayStartMin + s*gran,
			})
		}
	}

	// Mark which slots are occupied by calendar/fixed events.
	// occupied[globalSlotIdx] -> event info
	type occupant struct {
		name    string
		typ     ActivityType
		ignored bool
	}
	occupied := make(map[int]occupant)

	// Place calendar events (first pass: place event slots only)
	type dayEvent struct {
		startMin int
		endMin   int
		ignored  bool
	}
	campusEventsByDay := make(map[int][]dayEvent) // dayOffset -> on-campus events

	for _, ev := range events {
		ignored := isIgnored(ev, cfg.IgnoredEvents)
		evDayStart := time.Date(ev.Start.Year(), ev.Start.Month(), ev.Start.Day(), 0, 0, 0, 0, ev.Start.Location())
		dayOffset := int(evDayStart.Sub(weekStart).Hours() / 24)
		if dayOffset < 0 || dayOffset >= 7 {
			continue
		}

		evStartMin := ev.Start.Hour()*60 + ev.Start.Minute()
		evEndMin := ev.End.Hour()*60 + ev.End.Minute()

		// Place event slots
		for s := 0; s < slotsPerDay; s++ {
			slotStart := dayStartMin + s*gran
			slotEnd := slotStart + gran
			if slotStart >= evStartMin && slotEnd <= evEndMin {
				gi := dayOffset*slotsPerDay + s
				if ignored {
					occupied[gi] = occupant{ev.Summary, TypeUni, true}
				} else {
					occupied[gi] = occupant{ev.Summary, TypeUni, false}
				}
			}
		}

		// Track non-ignored campus events for commute consolidation
		if !ignored {
			campusEventsByDay[dayOffset] = append(campusEventsByDay[dayOffset], dayEvent{evStartMin, evEndMin, ignored})
		}
	}

	// Place commute buffers: one commute TO campus before the first event,
	// one commute FROM campus after the last event per day.
	if cfg.CommuteBuffer > 0 {
		for dayOffset, dayEvents := range campusEventsByDay {
			if len(dayEvents) == 0 {
				continue
			}
			// Find earliest start and latest end across all campus events this day
			firstStart := dayEvents[0].startMin
			lastEnd := dayEvents[0].endMin
			for _, de := range dayEvents[1:] {
				if de.startMin < firstStart {
					firstStart = de.startMin
				}
				if de.endMin > lastEnd {
					lastEnd = de.endMin
				}
			}

			// Commute TO campus before first event
			for s := 0; s < slotsPerDay; s++ {
				slotStart := dayStartMin + s*gran
				if slotStart >= firstStart-cfg.CommuteBuffer && slotStart < firstStart {
					gi := dayOffset*slotsPerDay + s
					if _, ok := occupied[gi]; !ok {
						occupied[gi] = occupant{"Commute", TypeCommute, false}
					}
				}
			}
			// Commute FROM campus after last event
			commuteSlots := cfg.CommuteBuffer / gran
			for s := 0; s < slotsPerDay; s++ {
				slotStart := dayStartMin + s*gran
				if slotStart >= lastEnd && slotStart < lastEnd+commuteSlots*gran {
					gi := dayOffset*slotsPerDay + s
					if _, ok := occupied[gi]; !ok {
						occupied[gi] = occupant{"Commute", TypeCommute, false}
					}
				}
			}
		}
	}

	// Place fixed events from config
	for _, fe := range cfg.FixedEvents {
		dayIdx := dayOfWeekIndex(fe.Day)
		if dayIdx < 0 {
			continue
		}
		feStartMin := parseHHMM(fe.Start)
		feEndMin := parseHHMM(fe.End)
		for s := 0; s < slotsPerDay; s++ {
			slotStart := dayStartMin + s*gran
			slotEnd := slotStart + gran
			if slotStart >= feStartMin && slotEnd <= feEndMin {
				gi := dayIdx*slotsPerDay + s
				occupied[gi] = occupant{fe.Name, ActivityType(fe.Type), false}
			}
		}
	}

	// Determine free slots (including ignored event slots, which are schedulable)
	freeSlots := make([]int, 0)
	for gi := 0; gi < 7*slotsPerDay; gi++ {
		occ, isOccupied := occupied[gi]
		if !isOccupied || occ.ignored {
			freeSlots = append(freeSlots, gi)
		}
	}
	freeSet := make(map[int]bool, len(freeSlots))
	for _, gi := range freeSlots {
		freeSet[gi] = true
	}
	occupiedTypes := make(map[int]ActivityType, len(occupied))
	for gi, occ := range occupied {
		occupiedTypes[gi] = occ.typ
	}

	// Count fixed (non-ignored) productive slots already on the grid. These
	// contribute to the week's total productive time but are outside the LP's
	// decision variables, so the solver needs to know their count when
	// targeting the 50-55h soft range.
	fixedProdSlots := 0
	for _, occ := range occupied {
		if occ.ignored {
			continue
		}
		if occ.typ == TypeWork || occ.typ == TypeUni {
			fixedProdSlots++
		}
	}

	// Build activity instances
	instances := buildInstances(cfg, gran)

	// Generate LP
	tmpDir, err := os.MkdirTemp("", "scheduler-lp-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	lpFile := filepath.Join(tmpDir, "schedule.lp")
	solFile := filepath.Join(tmpDir, "schedule.sol")

	if err := writeLPFile(lpFile, instances, cfg, slots, freeSet, occupiedTypes, slotsPerDay, gran, dayStartMin, fixedProdSlots); err != nil {
		return nil, fmt.Errorf("writing LP file: %w", err)
	}
	if opts.AuditModel {
		audit, err := auditLPFile(lpFile)
		if err != nil {
			return nil, fmt.Errorf("auditing LP file: %w", err)
		}
		printModelAudit(os.Stderr, audit)
	}

	// Solve with time limit and lightweight progress updates.
	const solverLimit = 45 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), solverLimit+5*time.Second)
	defer cancel()

	fmt.Fprintf(os.Stderr, "scheduler: generated MILP, starting CBC solve (limit %s)\n", solverLimit)
	cmd := exec.CommandContext(ctx, "cbc", lpFile, "solve", "-sec", "45", "-solution", solFile)
	var outputBuf bytes.Buffer
	cmd.Stdout = &outputBuf
	cmd.Stderr = &outputBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting CBC solver: %w", err)
	}
	done := make(chan error, 1)
	startedAt := time.Now()
	go func() {
		done <- cmd.Wait()
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var solverErr error
	for {
		select {
		case solverErr = <-done:
			elapsed := time.Since(startedAt).Round(time.Second)
			fmt.Fprintf(os.Stderr, "scheduler: CBC finished in %s\n", elapsed)
			goto solverDone
		case <-ticker.C:
			elapsed := time.Since(startedAt).Round(time.Second)
			remaining := solverLimit - time.Since(startedAt)
			if remaining < 0 {
				remaining = 0
			}
			fmt.Fprintf(os.Stderr, "scheduler: solving... elapsed %s, up to ~%s remaining before timeout\n",
				elapsed, remaining.Round(time.Second))
		}
	}

solverDone:
	output := outputBuf.Bytes()
	if solverErr != nil {
		return nil, fmt.Errorf("CBC solver failed: %w\nOutput: %s", solverErr, string(output))
	}

	// Check if solution file was written (CBC exits 0 even if infeasible)
	if _, err := os.Stat(solFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("CBC produced no solution (likely infeasible).\nCBC output: %s", string(output))
	}

	// Parse solution
	solution, solStatus, err := parseSolution(solFile)
	if err != nil {
		return nil, fmt.Errorf("parsing solution: %w", err)
	}
	if solStatus != "optimal" {
		return nil, fmt.Errorf("CBC did not find optimal solution (status: %s).\nCBC output: %s", solStatus, string(output))
	}

	// Build schedule
	schedule := &Schedule{
		WeekStart: weekStart,
		WeekEnd:   weekStart.AddDate(0, 0, 7),
		DayStart:  dayStartMin,
		DayEnd:    dayEndMin,
		Slots:     make([]Slot, 7*slotsPerDay),
	}

	// Fill all slots with times first
	for gi := 0; gi < 7*slotsPerDay; gi++ {
		si := slots[gi]
		day := weekStart.AddDate(0, 0, si.day)
		slotTime := time.Date(day.Year(), day.Month(), day.Day(), si.startMins/60, si.startMins%60, 0, 0, weekStart.Location())
		schedule.Slots[gi] = Slot{
			Start: slotTime,
			End:   slotTime.Add(time.Duration(gran) * time.Minute),
		}
	}

	// Place fixed/calendar events
	for gi, occ := range occupied {
		schedule.Slots[gi].Activity = occ.name
		schedule.Slots[gi].Type = occ.typ
		schedule.Slots[gi].Ignored = occ.ignored
	}

	// Place solved activities
	for varName, val := range solution {
		if val < 0.5 {
			continue
		}
		if !strings.HasPrefix(varName, "x_") {
			continue
		}
		parts := strings.SplitN(varName, "_", 4)
		if len(parts) < 4 {
			continue
		}
		instID := parts[1]
		dayIdx, _ := strconv.Atoi(parts[2])
		slotIdx, _ := strconv.Atoi(parts[3])
		gi := dayIdx*slotsPerDay + slotIdx

		inst := findInstance(instances, instID)
		if inst == nil {
			continue
		}

		// Only place if slot is free (or ignored)
		if schedule.Slots[gi].Activity == "" || schedule.Slots[gi].Ignored {
			schedule.Slots[gi].Activity = inst.name
			schedule.Slots[gi].Type = ActivityType(inst.act.Type)
			schedule.Slots[gi].Ignored = false
		}
	}

	// Fill remaining with breaks
	for gi := range schedule.Slots {
		if schedule.Slots[gi].Activity == "" {
			schedule.Slots[gi].Activity = "Break"
			schedule.Slots[gi].Type = TypeBreak
		}
	}

	relabelShortBreakSlivers(schedule, cfg, slotsPerDay)

	// Validate invariants
	if errs := validateSchedule(schedule, cfg, slotsPerDay); len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "⚠️  Schedule validation warnings:")
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
	}

	return schedule, nil
}

// validateSchedule checks hard invariants on the generated schedule.
func validateSchedule(schedule *Schedule, cfg *config.Config, slotsPerDay int) []string {
	var errs []string
	dayNames := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

	// Count activity slots per day
	dayActivity := make(map[int]map[string]int)
	for d := 0; d < 7; d++ {
		dayActivity[d] = make(map[string]int)
	}
	for gi, slot := range schedule.Slots {
		if slot.Activity == "" || slot.Activity == "Break" {
			continue
		}
		day := gi / slotsPerDay
		if day < 7 {
			dayActivity[day][slot.Activity]++
		}
	}

	// Check frequency-based activities appear correct number of times
	for _, act := range cfg.Activities {
		if act.Frequency == 0 {
			continue
		}
		durSlots := act.Duration / cfg.Day.Granularity
		totalSlots := 0
		for d := 0; d < 7; d++ {
			count := dayActivity[d][act.Name]
			totalSlots += count
			// No double sessions on same day
			if count > durSlots {
				errs = append(errs, fmt.Sprintf("%s on %s: %d slots (%d min) — more than one session",
					act.Name, dayNames[d], count, count*cfg.Day.Granularity))
			}
		}
		expectedTotal := act.Frequency * durSlots
		if totalSlots != expectedTotal {
			errs = append(errs, fmt.Sprintf("%s: %d total slots (%d min), want %d (%d min = %dx/week)",
				act.Name, totalSlots, totalSlots*cfg.Day.Granularity,
				expectedTotal, expectedTotal*cfg.Day.Granularity, act.Frequency))
		}
		// For daily activities (freq=7), check each day has exactly one
		if act.Frequency == 7 {
			for d := 0; d < 7; d++ {
				if dayActivity[d][act.Name] != durSlots {
					errs = append(errs, fmt.Sprintf("%s missing on %s (got %d slots, want %d)",
						act.Name, dayNames[d], dayActivity[d][act.Name], durSlots))
				}
			}
		}
	}

	// Check hourly activities total (account for fixed events of the same type)
	fixedSlotsByType := make(map[string]int)
	for _, fe := range cfg.FixedEvents {
		feStart := parseHHMM(fe.Start)
		feEnd := parseHHMM(fe.End)
		fixedSlotsByType[fe.Type] += (feEnd - feStart) / cfg.Day.Granularity
	}
	for _, act := range cfg.Activities {
		if act.HoursPerWeek == 0 {
			continue
		}
		minSlots := int(act.HoursPerWeek*60/float64(cfg.Day.Granularity)) - fixedSlotsByType[act.Type]
		if act.MinHoursPerWeek > 0 {
			minSlots = int(act.MinHoursPerWeek*60/float64(cfg.Day.Granularity)) - fixedSlotsByType[act.Type]
		}
		maxSlots := int(act.HoursPerWeek*60/float64(cfg.Day.Granularity)) - fixedSlotsByType[act.Type]
		if act.MaxHoursPerWeek > 0 {
			maxSlots = int(act.MaxHoursPerWeek*60/float64(cfg.Day.Granularity)) - fixedSlotsByType[act.Type]
		}
		totalSlots := 0
		for d := 0; d < 7; d++ {
			totalSlots += dayActivity[d][act.Name]
		}
		if totalSlots < minSlots || totalSlots > maxSlots {
			if minSlots == maxSlots {
				errs = append(errs, fmt.Sprintf("%s: %d total slots (%d min), want %d (%d min = %.0fh/week minus fixed events)",
					act.Name, totalSlots, totalSlots*cfg.Day.Granularity,
					minSlots, minSlots*cfg.Day.Granularity, act.HoursPerWeek))
			} else {
				errs = append(errs, fmt.Sprintf("%s: %d total slots (%d min), want between %d and %d slots (%d-%d min/week)",
					act.Name, totalSlots, totalSlots*cfg.Day.Granularity,
					minSlots, maxSlots, minSlots*cfg.Day.Granularity, maxSlots*cfg.Day.Granularity))
			}
		}

		if act.Name == "Work" && act.MinDuration > 0 {
			minSlots := act.MinDuration / cfg.Day.Granularity
			for d := 0; d < 7; d++ {
				run := 0
				hasWork := false
				for s := 0; s < slotsPerDay; s++ {
					gi := d*slotsPerDay + s
					if gi < len(schedule.Slots) && schedule.Slots[gi].Type == TypeWork {
						run++
						if schedule.Slots[gi].Activity == act.Name {
							hasWork = true
						}
						continue
					}
					if run > 0 && hasWork && run < minSlots {
						errs = append(errs, fmt.Sprintf("%s on %s: %d min block is shorter than minimum %d min",
							act.Name, dayNames[d], run*cfg.Day.Granularity, act.MinDuration))
					}
					run = 0
					hasWork = false
				}
				if run > 0 && hasWork && run < minSlots {
					errs = append(errs, fmt.Sprintf("%s on %s: %d min block is shorter than minimum %d min",
						act.Name, dayNames[d], run*cfg.Day.Granularity, act.MinDuration))
				}
			}
		}
	}

	// Check commute only on days with uni events
	for d := 0; d < 7; d++ {
		hasCommute := dayActivity[d]["Commute"] > 0
		hasUni := false
		for name, count := range dayActivity[d] {
			if count > 0 && name != "Commute" {
				for gi := d * slotsPerDay; gi < (d+1)*slotsPerDay && gi < len(schedule.Slots); gi++ {
					if schedule.Slots[gi].Type == TypeUni && !schedule.Slots[gi].Ignored {
						hasUni = true
						break
					}
				}
			}
			if hasUni {
				break
			}
		}
		if hasCommute && !hasUni {
			errs = append(errs, fmt.Sprintf("Commute on %s but no uni events", dayNames[d]))
		}
	}

	// Gym and Easy Run must not share a day.
	for d := 0; d < 7; d++ {
		if dayActivity[d]["Gym"] > 0 && dayActivity[d]["Easy Run"] > 0 {
			errs = append(errs, fmt.Sprintf("Gym and Easy Run both scheduled on %s", dayNames[d]))
		}
	}

	return errs
}

func buildInstances(cfg *config.Config, gran int) []actInstance {
	var instances []actInstance
	for _, act := range cfg.Activities {
		if act.Frequency > 0 && act.HoursPerWeek == 0 {
			// Frequency-based: create N instances
			durSlots := act.Duration / gran
			for i := 0; i < act.Frequency; i++ {
				instances = append(instances, actInstance{
					name:     act.Name,
					act:      act,
					duration: durSlots,
					id:       fmt.Sprintf("%s%d", sanitize(act.Name), i),
				})
			}
		} else if act.HoursPerWeek > 0 {
			// Hourly-based: single "pool" instance
			instances = append(instances, actInstance{
				name:     act.Name,
				act:      act,
				duration: 0, // filled flexibly
				id:       sanitize(act.Name),
			})
		}
	}
	return instances
}

func writeLPFile(path string, instances []actInstance, cfg *config.Config, slots []slotInfo, freeSet map[int]bool, occupiedTypes map[int]ActivityType, slotsPerDay, gran, dayStartMin, fixedProdSlots int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	// Collect all variables and objective terms
	objCoeffs := make(map[string]int) // variable -> coefficient (aggregated)
	var constraints []string
	var binVars []string

	// For frequency-based activities: start variables s_{id}_{day}_{slot}
	// Assignment: x_{id}_{day}_{slot} is derived
	// For hourly activities: x_{id}_{day}_{slot} directly

	// --- Frequency-based activities (gym, run, meals) ---
	freqInstances := make([]actInstance, 0)
	hourlyInstances := make([]actInstance, 0)
	for _, inst := range instances {
		if inst.duration > 0 {
			freqInstances = append(freqInstances, inst)
		} else {
			hourlyInstances = append(hourlyInstances, inst)
		}
	}

	// Track actual start variables created per instance for constraints
	instStartVars := make(map[string][]string) // inst.id -> list of start var names

	// For each frequency instance, create start variables
	for _, inst := range freqInstances {
		var startVars []string
		prefTime := parseHHMM(inst.act.PreferredTime)

		for d := 0; d < 7; d++ {
			if !isDayAllowed(inst.act, d) {
				continue
			}
			earliest := parseHHMM(inst.act.Earliest)
			if dayEarliest, ok := constraintTimeForDay(inst.act.Constraints, "day_earliest", d); ok && dayEarliest > earliest {
				earliest = dayEarliest
			}
			latest := parseHHMM(inst.act.Latest)
			if dayLatest, ok := constraintTimeForDay(inst.act.Constraints, "day_latest", d); ok {
				latest = dayLatest
			}
			if latest == 0 {
				latest = cfg.Day.EndMinutes()
			}
			latestStart := latest - inst.duration*gran
			if latestStart < dayStartMin {
				latestStart = dayStartMin
			}

			for s := 0; s < slotsPerDay; s++ {
				slotStart := dayStartMin + s*gran
				slotEnd := slotStart + inst.duration*gran

				// Check time window
				if earliest > 0 && slotStart < earliest {
					continue
				}
				if latest > 0 && slotEnd > latest {
					continue
				}

				// Check all slots in the block are free
				allFree := true
				for k := 0; k < inst.duration; k++ {
					gi := d*slotsPerDay + s + k
					if s+k >= slotsPerDay || !freeSet[gi] {
						allFree = false
						break
					}
				}
				if !allFree {
					continue
				}

				sv := fmt.Sprintf("s_%s_%d_%d", inst.id, d, s)
				startVars = append(startVars, sv)
				binVars = append(binVars, sv)

				// Also declare x vars for the block
				for k := 0; k < inst.duration; k++ {
					xv := fmt.Sprintf("x_%s_%d_%d", inst.id, d, s+k)
					binVars = append(binVars, xv)
				}

				// Objective: prefer preferred time / preferred day
				if prefTime > 0 {
					penalty := abs(slotStart+(inst.duration*gran/2)-prefTime) / gran
					// Meals get a stronger preferred_time penalty so breakfast sticks near 10:00
					if inst.act.Type == "meal" {
						penalty *= 5
					}
					if penalty > 0 {
						objCoeffs[sv] += penalty
					}
				}

				// Prefer preferred days
				if isPreferredDay(inst.act, d) {
					objCoeffs[sv] -= 10
				}

				// Some activities, like gym, work better later in their feasible window.
				if contains(inst.act.Constraints, "prefer_late") {
					latePenalty := (latestStart - slotStart) / gran
					if latePenalty > 0 {
						objCoeffs[sv] += latePenalty * 2
					}
				}
			}
		}

		// Exactly one start per instance
		if len(startVars) == 0 {
			return fmt.Errorf("no feasible placement for activity %s", inst.id)
		}
		instStartVars[inst.id] = startVars
		constraints = append(constraints, fmt.Sprintf("  one_%s: %s = 1", inst.id, strings.Join(startVars, " + ")))

		// Link start vars to x vars: x_{id}_{d}_{s} = sum of start vars that cover (d,s)
		for d := 0; d < 7; d++ {
			for s := 0; s < slotsPerDay; s++ {
				gi := d*slotsPerDay + s
				if !freeSet[gi] {
					continue
				}
				xv := fmt.Sprintf("x_%s_%d_%d", inst.id, d, s)
				var coveringStarts []string
				for k := 0; k < inst.duration; k++ {
					startSlot := s - k
					if startSlot < 0 {
						continue
					}
					sv := fmt.Sprintf("s_%s_%d_%d", inst.id, d, startSlot)
					// Check this start var exists
					for _, existing := range startVars {
						if existing == sv {
							coveringStarts = append(coveringStarts, sv)
							break
						}
					}
				}
				if len(coveringStarts) > 0 {
					constraints = append(constraints, fmt.Sprintf("  link_%s_%d_%d: %s - %s = 0",
						inst.id, d, s, xv, strings.Join(coveringStarts, " - ")))
				}
			}
		}
	}

	// Ensure same-name frequency instances are on different days
	// Group by activity name
	nameToInstances := make(map[string][]actInstance)
	for _, inst := range freqInstances {
		nameToInstances[inst.name] = append(nameToInstances[inst.name], inst)
	}
	for _, group := range nameToInstances {
		if len(group) <= 1 {
			continue
		}
		// For each day, at most one instance of this activity
		for d := 0; d < 7; d++ {
			var dayStarts []string
			dayPrefix := fmt.Sprintf("_%d_", d)
			for _, inst := range group {
				for _, sv := range instStartVars[inst.id] {
					// Only include start vars for this day
					if strings.Contains(sv, dayPrefix) {
						// Verify it's actually day d (parse s_{id}_{day}_{slot})
						parts := strings.Split(sv, "_")
						if len(parts) >= 3 && parts[len(parts)-2] == strconv.Itoa(d) {
							dayStarts = append(dayStarts, sv)
						}
					}
				}
			}
			if len(dayStarts) > 1 {
				constraints = append(constraints, fmt.Sprintf("  oneperday_%s_%d: %s <= 1",
					sanitize(group[0].name), d, strings.Join(dayStarts, " + ")))
			}
		}
	}

	// Count fixed event slots per type to subtract from hourly quotas
	fixedSlotsByType := make(map[string]int)
	for _, fe := range cfg.FixedEvents {
		feStartMin := parseHHMM(fe.Start)
		feEndMin := parseHHMM(fe.End)
		feSlots := (feEndMin - feStartMin) / gran
		fixedSlotsByType[fe.Type] += feSlots
	}

	// --- Hourly activities (work, study) ---
	for _, inst := range hourlyInstances {
		targetSlots := int(inst.act.HoursPerWeek*60/float64(gran)) - fixedSlotsByType[inst.act.Type]
		minSlots := targetSlots
		if inst.act.MinHoursPerWeek > 0 {
			minSlots = int(inst.act.MinHoursPerWeek*60/float64(gran)) - fixedSlotsByType[inst.act.Type]
		}
		maxSlots := targetSlots
		if inst.act.MaxHoursPerWeek > 0 {
			maxSlots = int(inst.act.MaxHoursPerWeek*60/float64(gran)) - fixedSlotsByType[inst.act.Type]
		}
		earliest := parseHHMM(inst.act.Earliest)
		latest := parseHHMM(inst.act.Latest)
		if latest == 0 {
			latest = cfg.Day.EndMinutes()
		}
		minBlock := inst.act.MinDuration / gran
		if minBlock < 1 {
			minBlock = 1
		}
		allowOccupiedBridge := inst.act.Type == "work"

		var xVars []string
		for d := 0; d < 7; d++ {
			if !isDayAllowed(inst.act, d) {
				continue
			}
			for s := 0; s < slotsPerDay; s++ {
				gi := d*slotsPerDay + s
				if !freeSet[gi] {
					continue
				}
				slotStart := dayStartMin + s*gran
				if earliest > 0 && slotStart < earliest {
					continue
				}
				if latest > 0 && slotStart+gran > latest {
					continue
				}

				xv := fmt.Sprintf("x_%s_%d_%d", inst.id, d, s)
				xVars = append(xVars, xv)
				binVars = append(binVars, xv)

				// Prefer morning
				if inst.act.PreferredTime == "morning" {
					hour := slotStart / 60
					if hour < 12 {
						objCoeffs[xv] -= 1
					} else {
						objCoeffs[xv] += 1
					}
				}

				// Prefer earlier in the day for all hourly activities
				// Penalty increases with slot position to avoid late-day scheduling
				slotHour := slotStart / 60
				objCoeffs[xv] += slotHour / 3 // gentle push toward earlier

				// Prefer weekdays over weekends (higher penalty for Sat=5, Sun=6)
				if d == 5 { // Saturday
					objCoeffs[xv] += 15
				} else if d == 6 { // Sunday
					objCoeffs[xv] += 10
				}

				// Prefer preferred days
				if isPreferredDay(inst.act, d) {
					objCoeffs[xv] -= 2
				}

				if inst.act.Type == "work" && hasAdjacentOccupiedType(d, s, slotsPerDay, occupiedTypes, ActivityType(inst.act.Type)) {
					objCoeffs[xv] -= 4
				}
				for _, window := range constraintPenaltyWindowsForDay(inst.act.Constraints, d) {
					if slotStart >= window.start && slotStart < window.end {
						objCoeffs[xv] += window.penalty
					}
				}
				if contains(inst.act.Constraints, "prefer_fuller_quota") {
					objCoeffs[xv] -= 1
				}
			}
		}

		// Total slots = quota
		if len(xVars) == 0 {
			return fmt.Errorf("no feasible slots for activity %s", inst.id)
		}
		if minSlots == maxSlots {
			constraints = append(constraints, fmt.Sprintf("  quota_%s: %s = %d", inst.id, strings.Join(xVars, " + "), minSlots))
		} else {
			constraints = append(constraints, fmt.Sprintf("  quota_min_%s: %s >= %d", inst.id, strings.Join(xVars, " + "), minSlots))
			constraints = append(constraints, fmt.Sprintf("  quota_max_%s: %s <= %d", inst.id, strings.Join(xVars, " + "), maxSlots))
		}

		// Minimum block size constraints
		// If x[d][s]=1 and x[d][s-1]=0, then x[d][s+1]..x[d][s+minBlock-1] must all be 1
		if minBlock > 1 {
			for d := 0; d < 7; d++ {
				if !isDayAllowed(inst.act, d) {
					continue
				}
				for s := 0; s < slotsPerDay; s++ {
					gi := d*slotsPerDay + s
					if !freeSet[gi] {
						continue
					}
					slotStart := dayStartMin + s*gran
					if earliest > 0 && slotStart < earliest {
						continue
					}
					if latest > 0 && slotStart+gran > latest {
						continue
					}

					xCur := fmt.Sprintf("x_%s_%d_%d", inst.id, d, s)

					prevFree := false
					prevSameTypeOccupied := false
					backCompatOccupied := 0
					if s > 0 {
						prevGi := d*slotsPerDay + s - 1
						prevStart := dayStartMin + (s-1)*gran
						if freeSet[prevGi] && (earliest == 0 || prevStart >= earliest) && (latest == 0 || prevStart+gran <= latest) {
							prevFree = true
						}
						if allowOccupiedBridge && occupiedTypes[prevGi] == ActivityType(inst.act.Type) {
							prevSameTypeOccupied = true
							for back := s - 1; back >= 0; back-- {
								backGi := d*slotsPerDay + back
								if occupiedTypes[backGi] != ActivityType(inst.act.Type) {
									break
								}
								backCompatOccupied++
							}
						}
					}

					collectRequiredFuture := func(initialCompat int) ([]int, bool) {
						neededCompat := minBlock - initialCompat
						if neededCompat <= 0 {
							return nil, true
						}
						var requiredFreeSlots []int
						for nextS := s + 1; nextS < slotsPerDay; nextS++ {
							nextGi := d*slotsPerDay + nextS
							nextStart := dayStartMin + nextS*gran
							if allowOccupiedBridge && occupiedTypes[nextGi] == ActivityType(inst.act.Type) {
								neededCompat--
							} else if freeSet[nextGi] && (latest == 0 || nextStart+gran <= latest) {
								requiredFreeSlots = append(requiredFreeSlots, nextS)
								neededCompat--
							} else {
								return nil, false
							}
							if neededCompat == 0 {
								return requiredFreeSlots, true
							}
						}
						return nil, false
					}
					if !prevFree && !prevSameTypeOccupied {
						requiredFreeSlots, ok := collectRequiredFuture(1)
						if !ok {
							constraints = append(constraints, fmt.Sprintf("  noshort_%s_%d_%d: %s = 0", inst.id, d, s, xCur))
							continue
						}
						for k, nextS := range requiredFreeSlots {
							xNext := fmt.Sprintf("x_%s_%d_%d", inst.id, d, nextS)
							constraints = append(constraints, fmt.Sprintf("  minblk_%s_%d_%d_%d: %s - %s >= 0", inst.id, d, s, k+1, xNext, xCur))
						}
					} else if prevSameTypeOccupied && !prevFree {
						requiredFreeSlots, ok := collectRequiredFuture(backCompatOccupied + 1)
						if !ok {
							constraints = append(constraints, fmt.Sprintf("  noshort_%s_%d_%d: %s = 0", inst.id, d, s, xCur))
							continue
						}
						for k, nextS := range requiredFreeSlots {
							xNext := fmt.Sprintf("x_%s_%d_%d", inst.id, d, nextS)
							constraints = append(constraints, fmt.Sprintf("  minblk_%s_%d_%d_%d: %s - %s >= 0", inst.id, d, s, k+1, xNext, xCur))
						}
					} else {
						xPrev := fmt.Sprintf("x_%s_%d_%d", inst.id, d, s-1)
						requiredFreeSlots, ok := collectRequiredFuture(1)
						if !ok {
							constraints = append(constraints, fmt.Sprintf("  noshort_%s_%d_%d: %s - %s <= 0", inst.id, d, s, xCur, xPrev))
							continue
						}
						for k, nextS := range requiredFreeSlots {
							xNext := fmt.Sprintf("x_%s_%d_%d", inst.id, d, nextS)
							constraints = append(constraints, fmt.Sprintf("  minblk_%s_%d_%d_%d: %s - %s + %s >= 0",
								inst.id, d, s, k+1, xNext, xCur, xPrev))
						}
					}
				}
			}
		}

		// Work consolidation: penalize each day that has work
		if contains(inst.act.Constraints, "consolidate_days") {
			for d := 0; d < 7; d++ {
				// Binary: day_has_work_d = 1 if any work on day d
				dv := fmt.Sprintf("dayhas_%s_%d", inst.id, d)
				binVars = append(binVars, dv)
				objCoeffs[dv] += 5 // mild consolidation, must not override weekend avoidance

				for s := 0; s < slotsPerDay; s++ {
					xv := fmt.Sprintf("x_%s_%d_%d", inst.id, d, s)
					// x <= dayhas
					constraints = append(constraints, fmt.Sprintf("  daylink_%s_%d_%d: %s - %s <= 0", inst.id, d, s, xv, dv))
				}
			}
		}
	}

	// --- No-overlap on free slots ---
	// Build a set of all declared x variables for quick lookup
	declaredXVars := make(map[string]bool)
	for _, v := range binVars {
		if strings.HasPrefix(v, "x_") {
			declaredXVars[v] = true
		}
	}
	// For each free slot, sum of x vars that actually exist <= 1
	for d := 0; d < 7; d++ {
		for s := 0; s < slotsPerDay; s++ {
			gi := d*slotsPerDay + s
			if !freeSet[gi] {
				continue
			}

			var slotVars []string
			for _, inst := range instances {
				xv := fmt.Sprintf("x_%s_%d_%d", inst.id, d, s)
				if declaredXVars[xv] {
					slotVars = append(slotVars, xv)
				}
			}
			if len(slotVars) > 0 {
				constraints = append(constraints, fmt.Sprintf("  nooverlap_%d_%d: %s <= 1", d, s, strings.Join(slotVars, " + ")))
			}
		}
	}

	// --- Soft total productive time: target 50-55h/week ---
	// Sum of all productive (work/uni) x vars plus pre-placed fixed productive
	// slots should land in [50h, 55h]. Rather than enforcing as a hard bound
	// (which can clash with per-activity quotas and make the model infeasible),
	// we penalize slots of deviation on either side.
	{
		minProdSlots := 50 * 60 / gran
		maxProdSlots := 55 * 60 / gran
		remainingMin := minProdSlots - fixedProdSlots
		if remainingMin < 0 {
			remainingMin = 0
		}
		remainingMax := maxProdSlots - fixedProdSlots
		if remainingMax < 0 {
			remainingMax = 0
		}

		var prodXVars []string
		for _, inst := range instances {
			if inst.act.Type != "work" && inst.act.Type != "uni" {
				continue
			}
			prefix := fmt.Sprintf("x_%s_", inst.id)
			for v := range declaredXVars {
				if strings.HasPrefix(v, prefix) {
					prodXVars = append(prodXVars, v)
				}
			}
		}
		if len(prodXVars) > 0 {
			sumStr := strings.Join(prodXVars, " + ")
			constraints = append(constraints,
				fmt.Sprintf("  prodmin: %s + under_prod >= %d", sumStr, remainingMin))
			constraints = append(constraints,
				fmt.Sprintf("  prodmax: %s - over_prod <= %d", sumStr, remainingMax))
			const prodDeviationPenalty = 25
			objCoeffs["under_prod"] += prodDeviationPenalty
			objCoeffs["over_prod"] += prodDeviationPenalty
			// under_prod and over_prod are left out of the Binary section so CBC
			// treats them as non-negative continuous slack variables.
		}
	}

	// --- No 3 consecutive gym days ---
	gymInstances := nameToInstances["Gym"]
	if len(gymInstances) > 0 && instancesHaveConstraint(gymInstances, "no_three_consecutive_gym_days") {
		for d := 0; d <= 4; d++ { // d, d+1, d+2
			var threeDay []string
			for _, inst := range gymInstances {
				for dd := d; dd <= d+2; dd++ {
					dayStr := strconv.Itoa(dd)
					for _, sv := range instStartVars[inst.id] {
						parts := strings.Split(sv, "_")
						if len(parts) >= 3 && parts[len(parts)-2] == dayStr {
							threeDay = append(threeDay, sv)
						}
					}
				}
			}
			if len(threeDay) > 0 {
				constraints = append(constraints, fmt.Sprintf("  no3gym_%d: %s <= 2", d, strings.Join(threeDay, " + ")))
			}
		}
	}

	// --- Easy Run and Gym cannot share a day ---
	runInstances := nameToInstances["Easy Run"]
	if len(gymInstances) > 0 && len(runInstances) > 0 {
		for d := 0; d < 7; d++ {
			dayStr := strconv.Itoa(d)
			var dayStarts []string
			for _, inst := range gymInstances {
				for _, sv := range instStartVars[inst.id] {
					parts := strings.Split(sv, "_")
					if len(parts) >= 3 && parts[len(parts)-2] == dayStr {
						dayStarts = append(dayStarts, sv)
					}
				}
			}
			for _, inst := range runInstances {
				for _, sv := range instStartVars[inst.id] {
					parts := strings.Split(sv, "_")
					if len(parts) >= 3 && parts[len(parts)-2] == dayStr {
						dayStarts = append(dayStarts, sv)
					}
				}
			}
			if len(dayStarts) > 1 {
				constraints = append(constraints, fmt.Sprintf("  nogymrun_%d: %s <= 1",
					d, strings.Join(dayStarts, " + ")))
			}
		}
	}

	// --- Meal aggregation: build per-type per-day per-slot indicators ---
	// Used by both meal gap and meal-before-gym constraints.
	type mealSlotKey struct {
		name string
		day  int
		slot int
	}
	mealSlotVars := make(map[mealSlotKey]string) // key -> var name

	mealByName := make(map[string][]actInstance)
	for _, inst := range freqInstances {
		if inst.act.Type == "meal" {
			mealByName[inst.name] = append(mealByName[inst.name], inst)
		}
	}

	for name, group := range mealByName {
		for d := 0; d < 7; d++ {
			dayStr := strconv.Itoa(d)
			slotToStarts := make(map[int][]string)
			for _, inst := range group {
				for _, sv := range instStartVars[inst.id] {
					parts := strings.Split(sv, "_")
					if len(parts) < 4 || parts[len(parts)-2] != dayStr {
						continue
					}
					slot, _ := strconv.Atoi(parts[len(parts)-1])
					slotToStarts[slot] = append(slotToStarts[slot], sv)
				}
			}
			for slot, starts := range slotToStarts {
				mv := fmt.Sprintf("m_%s_%d_%d", sanitize(name), d, slot)
				mealSlotVars[mealSlotKey{name, d, slot}] = mv
				binVars = append(binVars, mv)
				constraints = append(constraints, fmt.Sprintf("  mlink_%s_%d_%d: %s - %s = 0",
					sanitize(name), d, slot, mv, strings.Join(starts, " - ")))
			}
		}
	}

	// --- Minimum gap between meals on the same day ---
	if cfg.MinMealGap > 0 {
		gapSlots := cfg.MinMealGap / gran

		var mealNames []string
		for name := range mealByName {
			mealNames = append(mealNames, name)
		}
		for i := 0; i < len(mealNames); i++ {
			for j := i + 1; j < len(mealNames); j++ {
				nameA := mealNames[i]
				nameB := mealNames[j]
				for d := 0; d < 7; d++ {
					for slotA, mvA := range mealSlotVars {
						if slotA.name != nameA || slotA.day != d {
							continue
						}
						for slotB, mvB := range mealSlotVars {
							if slotB.name != nameB || slotB.day != d {
								continue
							}
							if abs(slotA.slot-slotB.slot) < gapSlots {
								constraints = append(constraints, fmt.Sprintf("  mealgap_%s_%s_%d_%d_%d: %s + %s <= 1",
									sanitize(nameA), sanitize(nameB), d, slotA.slot, slotB.slot, mvA, mvB))
							}
						}
					}
				}
			}
		}
	}

	// --- Hard constraint: meals must finish at least 1h before gym ---
	// Use aggregated gym-day indicators and per-meal-type-day indicators to keep
	// the constraint count manageable.
	{
		mealGymGapSlots := 60 / gran // 4 slots = 1h
		gymInsts := nameToInstances["Gym"]
		if len(gymInsts) > 0 {
			// Build per-day gym start slot aggregator: for each (day, slot),
			// g_{day}_{slot} = 1 if any gym instance starts there
			type gymDaySlot struct{ day, slot int }
			gymAgg := make(map[gymDaySlot]string)
			for d := 0; d < 7; d++ {
				dayStr := strconv.Itoa(d)
				slotToGymStarts := make(map[int][]string)
				for _, gi := range gymInsts {
					for _, gsv := range instStartVars[gi.id] {
						parts := strings.Split(gsv, "_")
						if len(parts) < 4 || parts[len(parts)-2] != dayStr {
							continue
						}
						slot, _ := strconv.Atoi(parts[len(parts)-1])
						slotToGymStarts[slot] = append(slotToGymStarts[slot], gsv)
					}
				}
				for slot, starts := range slotToGymStarts {
					gv := fmt.Sprintf("gagg_%d_%d", d, slot)
					gymAgg[gymDaySlot{d, slot}] = gv
					binVars = append(binVars, gv)
					constraints = append(constraints, fmt.Sprintf("  gagglink_%d_%d: %s - %s = 0",
						d, slot, gv, strings.Join(starts, " - ")))
				}
			}

			// For each meal type, find its duration in slots
			mealDurByName := make(map[string]int)
			for _, inst := range freqInstances {
				if inst.act.Type == "meal" {
					mealDurByName[inst.name] = inst.duration
				}
			}

			// For each (meal-agg-slot, gym-agg-slot) on same day where gap < 1h,
			// disallow choosing both placements together.
			for mKey, mv := range mealSlotVars {
				mealDur := mealDurByName[mKey.name]
				if mealDur == 0 {
					continue
				}
				mealEnd := mKey.slot + mealDur
				for gKey, gv := range gymAgg {
					if gKey.day != mKey.day {
						continue
					}
					gap := gKey.slot - mealEnd
					if gap >= 0 && gap < mealGymGapSlots {
						constraints = append(constraints, fmt.Sprintf("  mealgymgap_%s_%d_%d_%d: %s + %s <= 1",
							sanitize(mKey.name), mKey.day, mKey.slot, gKey.slot, mv, gv))
					}
				}
			}
		}
	}

	// Build objective terms from aggregated coefficients
	var objTerms []string
	for v, c := range objCoeffs {
		if c == 0 {
			continue
		}
		if c > 0 {
			objTerms = append(objTerms, fmt.Sprintf("+ %d %s", c, v))
		} else {
			objTerms = append(objTerms, fmt.Sprintf("- %d %s", -c, v))
		}
	}

	// Write LP file
	fmt.Fprintln(w, "Minimize")
	if len(objTerms) == 0 {
		fmt.Fprintln(w, "  obj: 0 x_dummy")
		binVars = append(binVars, "x_dummy")
	} else {
		fmt.Fprintf(w, "  obj: %s\n", strings.Join(objTerms, " "))
	}

	fmt.Fprintln(w, "Subject To")
	for _, c := range constraints {
		fmt.Fprintln(w, c)
	}

	fmt.Fprintln(w, "Binary")
	// Deduplicate
	seen := make(map[string]bool)
	for _, v := range binVars {
		if !seen[v] {
			fmt.Fprintf(w, "  %s\n", v)
			seen[v] = true
		}
	}

	fmt.Fprintln(w, "End")
	return nil
}

func auditLPFile(path string) (*modelAudit, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	audit := &modelAudit{
		ConstraintGroups: make(map[string]int),
		VariableGroups:   make(map[string]int),
	}

	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case "Subject To":
			section = "constraints"
			continue
		case "Binary":
			section = "binary"
			continue
		case "End", "Minimize":
			section = ""
			continue
		}
		if line == "" || strings.HasPrefix(line, "obj:") {
			continue
		}

		switch section {
		case "constraints":
			label := line
			if idx := strings.Index(line, ":"); idx != -1 {
				label = strings.TrimSpace(line[:idx])
			}
			audit.ConstraintCount++
			audit.ConstraintGroups[auditConstraintGroup(label)]++
		case "binary":
			audit.BinaryVarCount++
			audit.VariableGroups[auditVariableGroup(line)]++
		}
	}

	return audit, scanner.Err()
}

func printModelAudit(w *os.File, audit *modelAudit) {
	fmt.Fprintf(w, "scheduler: model audit: %d constraints, %d binary vars\n", audit.ConstraintCount, audit.BinaryVarCount)
	printAuditGroups(w, "constraint groups", audit.ConstraintGroups)
	printAuditGroups(w, "variable groups", audit.VariableGroups)
}

func printAuditGroups(w *os.File, title string, groups map[string]int) {
	type kv struct {
		name  string
		count int
	}
	var entries []kv
	for name, count := range groups {
		entries = append(entries, kv{name: name, count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count == entries[j].count {
			return entries[i].name < entries[j].name
		}
		return entries[i].count > entries[j].count
	})
	fmt.Fprintf(w, "scheduler: %s:\n", title)
	limit := 8
	if len(entries) < limit {
		limit = len(entries)
	}
	for i := 0; i < limit; i++ {
		fmt.Fprintf(w, "  - %s: %d\n", entries[i].name, entries[i].count)
	}
}

func auditConstraintGroup(label string) string {
	switch {
	case strings.HasPrefix(label, "link_"):
		return "frequency link"
	case strings.HasPrefix(label, "one_"):
		return "frequency choose-one"
	case strings.HasPrefix(label, "oneperday_"):
		return "same-day exclusivity"
	case strings.HasPrefix(label, "quota_"), strings.HasPrefix(label, "quota_min_"), strings.HasPrefix(label, "quota_max_"):
		return "hourly quota"
	case strings.HasPrefix(label, "minblk_"), strings.HasPrefix(label, "noshort_"):
		return "minimum block"
	case strings.HasPrefix(label, "daylink_"):
		return "day consolidation link"
	case strings.HasPrefix(label, "nooverlap_"):
		return "slot no-overlap"
	case strings.HasPrefix(label, "no3gym_"):
		return "gym spacing"
	case strings.HasPrefix(label, "nogymrun_"):
		return "gym/run day exclusion"
	case strings.HasPrefix(label, "mlink_"):
		return "meal aggregation"
	case strings.HasPrefix(label, "mealgap_"):
		return "meal gap"
	case strings.HasPrefix(label, "gagglink_"):
		return "gym aggregation"
	case strings.HasPrefix(label, "mealgymgap_"):
		return "meal before gym"
	case label == "prodmin", label == "prodmax":
		return "total productive range"
	default:
		return "other"
	}
}

func auditVariableGroup(name string) string {
	switch {
	case strings.HasPrefix(name, "x_"):
		return "schedule occupancy"
	case strings.HasPrefix(name, "s_"):
		return "frequency starts"
	case strings.HasPrefix(name, "dayhas_"):
		return "day usage flags"
	case strings.HasPrefix(name, "m_"):
		return "meal aggregates"
	case strings.HasPrefix(name, "gagg_"):
		return "gym aggregates"
	case name == "under_prod", name == "over_prod":
		return "total productive slack"
	default:
		return "other"
	}
}

func parseSolution(path string) (map[string]float64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	result := make(map[string]float64)
	status := "unknown"
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Optimal") {
			status = "optimal"
			continue
		}
		if strings.Contains(line, "Infeasible") {
			status = "infeasible"
			continue
		}
		if strings.HasPrefix(line, "**") || line == "" {
			continue
		}
		fields := strings.Fields(line)
		// CBC solution format: index varName value objCoeff
		if len(fields) >= 3 {
			varName := fields[1]
			val, err := strconv.ParseFloat(fields[2], 64)
			if err != nil {
				continue
			}
			result[varName] = val
		}
	}
	return result, status, scanner.Err()
}

func findInstance(instances []actInstance, id string) *actInstance {
	for i := range instances {
		if instances[i].id == id {
			return &instances[i]
		}
	}
	return nil
}

func isIgnored(ev calendar.Event, rules []config.IgnoreRule) bool {
	for _, rule := range rules {
		if strings.Contains(strings.ToLower(ev.Summary), strings.ToLower(rule.Pattern)) {
			if rule.Calendar == "" || rule.Calendar == ev.Calendar {
				return true
			}
		}
	}
	return false
}

func isDayAllowed(act config.Activity, dayIdx int) bool {
	if len(act.AllowedDays) == 0 {
		return true
	}
	dayName := indexToDayName(dayIdx)
	for _, d := range act.AllowedDays {
		if strings.EqualFold(d, dayName) {
			return true
		}
	}
	return false
}

func isPreferredDay(act config.Activity, dayIdx int) bool {
	dayName := indexToDayName(dayIdx)
	if act.PreferredDay != "" && strings.EqualFold(act.PreferredDay, dayName) {
		return true
	}
	for _, d := range act.PreferredDays {
		if strings.EqualFold(d, dayName) {
			return true
		}
	}
	return false
}

func dayOfWeekIndex(day string) int {
	days := map[string]int{"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}
	idx, ok := days[strings.ToLower(day)]
	if !ok {
		return -1
	}
	return idx
}

func indexToDayName(idx int) string {
	names := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	if idx < 0 || idx >= 7 {
		return ""
	}
	return names[idx]
}

func parseHHMM(s string) int {
	if s == "" {
		return 0
	}
	if len(s) == 4 && !strings.Contains(s, ":") {
		h, errH := strconv.Atoi(s[:2])
		m, errM := strconv.Atoi(s[2:])
		if errH == nil && errM == nil && h >= 0 && h < 24 && m >= 0 && m < 60 {
			return h*60 + m
		}
	}
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0
	}
	return t.Hour()*60 + t.Minute()
}

func constraintTimeForDay(constraints []string, prefix string, dayIdx int) (int, bool) {
	dayName := indexToDayName(dayIdx)
	prefix = strings.ToLower(prefix)
	for _, c := range constraints {
		parts := strings.Split(strings.ToLower(c), ":")
		if len(parts) != 3 {
			continue
		}
		if parts[0] != prefix || parts[1] != dayName {
			continue
		}
		return parseHHMM(parts[2]), true
	}
	return 0, false
}

func constraintPenaltyWindowsForDay(constraints []string, dayIdx int) []penaltyWindow {
	dayName := indexToDayName(dayIdx)
	var windows []penaltyWindow
	for _, c := range constraints {
		parts := strings.Split(strings.ToLower(c), ":")
		if len(parts) != 5 {
			continue
		}
		if parts[0] != "avoid_window" || parts[1] != dayName {
			continue
		}
		start := parseHHMM(parts[2])
		end := parseHHMM(parts[3])
		penalty, err := strconv.Atoi(parts[4])
		if err != nil || start == 0 || end == 0 || end <= start || penalty <= 0 {
			continue
		}
		windows = append(windows, penaltyWindow{start: start, end: end, penalty: penalty})
	}
	return windows
}

func instancesHaveConstraint(instances []actInstance, constraint string) bool {
	for _, inst := range instances {
		if contains(inst.act.Constraints, constraint) {
			return true
		}
	}
	return false
}

func hasAdjacentOccupiedType(day, slot, slotsPerDay int, occupiedTypes map[int]ActivityType, typ ActivityType) bool {
	gi := day*slotsPerDay + slot
	for _, neighbor := range []int{gi - 1, gi + 1} {
		if occupiedTypes[neighbor] == typ {
			return true
		}
	}
	return false
}

func relabelShortBreakSlivers(schedule *Schedule, cfg *config.Config, slotsPerDay int) {
	minWorkSlots := minWorkBlockSlots(cfg)
	if minWorkSlots <= 1 {
		return
	}
	minAdminSlots := 45 / cfg.Day.Granularity
	if minAdminSlots < 1 {
		minAdminSlots = 1
	}
	for d := 0; d < 7; d++ {
		runStart := -1
		runLen := 0
		flush := func() {
			if runStart == -1 {
				return
			}
			if runLen >= minAdminSlots && runLen < minWorkSlots {
				for offset := 0; offset < runLen; offset++ {
					gi := d*slotsPerDay + runStart + offset
					if gi >= len(schedule.Slots) {
						continue
					}
					schedule.Slots[gi].Activity = secondaryProductiveActivityName(cfg)
					schedule.Slots[gi].Type = TypeUni
				}
			}
			runStart = -1
			runLen = 0
		}
		for s := 0; s < slotsPerDay; s++ {
			gi := d*slotsPerDay + s
			if gi < len(schedule.Slots) && schedule.Slots[gi].Activity == "Break" {
				if runStart == -1 {
					runStart = s
				}
				runLen++
				continue
			}
			flush()
		}
		flush()
	}
}

func minWorkBlockSlots(cfg *config.Config) int {
	for _, act := range cfg.Activities {
		if act.Name != "Work" || act.MinDuration <= 0 {
			continue
		}
		return act.MinDuration / cfg.Day.Granularity
	}
	return 0
}

func secondaryProductiveActivityName(cfg *config.Config) string {
	for _, act := range cfg.Activities {
		if act.Type == "uni" && strings.Contains(strings.ToLower(act.Name), "self-study") {
			return act.Name
		}
	}
	return "Self-Study/Admin/Projects"
}

func sanitize(s string) string {
	replacer := strings.NewReplacer(
		" ", "",
		"-", "",
		"/", "",
		"(", "",
		")", "",
		".", "",
	)
	return replacer.Replace(strings.ToLower(s))
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// Unused but needed to avoid import error
var _ = math.Abs
