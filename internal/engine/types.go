package engine

import "time"

// ActivityType categorizes scheduled activities for color coding.
type ActivityType string

const (
	TypeUni      ActivityType = "uni"
	TypeWork     ActivityType = "work"
	TypeFitness  ActivityType = "fitness"
	TypeMeal     ActivityType = "meal"
	TypeBreak    ActivityType = "break"
	TypePersonal ActivityType = "personal"
	TypeCommute  ActivityType = "commute"
)

// Slot represents a single 15-minute time slot in the schedule.
type Slot struct {
	Start    time.Time
	End      time.Time
	Activity string
	Type     ActivityType
	Ignored  bool // calendar event marked as ignored in config
}

// Schedule holds a full week of scheduled slots.
type Schedule struct {
	WeekStart time.Time // Monday of the week
	WeekEnd   time.Time // Sunday end-of-day
	DayStart  int       // minutes from midnight (e.g., 450 for 07:30)
	DayEnd    int       // minutes from midnight (e.g., 1260 for 21:00)
	Slots     []Slot
}
