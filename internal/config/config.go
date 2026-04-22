package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the full scheduling configuration from config.yaml.
type Config struct {
	Calendars     []CalendarRef `yaml:"calendars"`
	Day           DayConfig     `yaml:"day"`
	IgnoredEvents []IgnoreRule  `yaml:"ignored_events"`
	CommuteBuffer int           `yaml:"commute_buffer"`
	MinMealGap    int           `yaml:"min_meal_gap"`
	Activities    []Activity    `yaml:"activities"`
	FixedEvents   []FixedEvent  `yaml:"fixed_events"`
}

type CalendarRef struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

type DayConfig struct {
	Start       string `yaml:"start"`
	End         string `yaml:"end"`
	Granularity int    `yaml:"granularity"`
}

type IgnoreRule struct {
	Pattern  string `yaml:"pattern"`
	Calendar string `yaml:"calendar"`
}

type Activity struct {
	Name            string   `yaml:"name"`
	Type            string   `yaml:"type"`
	Duration        int      `yaml:"duration"`
	MinDuration     int      `yaml:"min_duration"`
	HoursPerWeek    float64  `yaml:"hours_per_week"`
	MinHoursPerWeek float64  `yaml:"min_hours_per_week"`
	MaxHoursPerWeek float64  `yaml:"max_hours_per_week"`
	Frequency       int      `yaml:"frequency"`
	Priority        string   `yaml:"priority"`
	Earliest        string   `yaml:"earliest"`
	Latest          string   `yaml:"latest"`
	PreferredTime   string   `yaml:"preferred_time"`
	PreferredDay    string   `yaml:"preferred_day"`
	PreferredDays   []string `yaml:"preferred_days"`
	Constraints     []string `yaml:"constraints"`
	AllowedDays     []string `yaml:"allowed_days"`
	Location        string   `yaml:"location"`
	Notes           string   `yaml:"notes"`
}

type FixedEvent struct {
	Name  string `yaml:"name"`
	Day   string `yaml:"day"`
	Start string `yaml:"start"`
	End   string `yaml:"end"`
	Type  string `yaml:"type"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Day.Granularity == 0 {
		cfg.Day.Granularity = 15
	}
	return &cfg, nil
}

func (d DayConfig) StartMinutes() int {
	dur, _ := parseTimeOfDay(d.Start)
	return int(dur / time.Minute)
}

func (d DayConfig) EndMinutes() int {
	dur, _ := parseTimeOfDay(d.End)
	return int(dur / time.Minute)
}

func parseTimeOfDay(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}
