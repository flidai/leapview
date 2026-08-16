package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/robfig/cron/v3"
)

const MinimumInterval = 5 * time.Minute

// Definition is the deployable description of one semantic-model refresh pipeline.
type Definition struct {
	ID              projectgraph.ResourceID
	Name            string
	SemanticModelID projectgraph.ResourceID
	Schedules       []Schedule
}

// Schedule is a validated five-field POSIX cron schedule evaluated in Timezone.
type Schedule struct {
	Expression string
	Timezone   string
	location   *time.Location
	schedule   cron.Schedule
}

var githubActionsParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

func ParseSchedule(expression, timezone string) (Schedule, error) {
	expression = strings.Join(strings.Fields(expression), " ")
	if expression == "" || strings.HasPrefix(expression, "@") || len(strings.Fields(expression)) != 5 {
		return Schedule{}, fmt.Errorf("cron must be a five-field POSIX expression")
	}
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Schedule{}, fmt.Errorf("timezone %q must be a valid IANA timezone: %w", timezone, err)
	}
	parsed, err := githubActionsParser.Parse(expression)
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid five-field POSIX cron %q: %w", expression, err)
	}
	if err := validateMinimumInterval(expression); err != nil {
		return Schedule{}, err
	}
	return Schedule{
		Expression: expression,
		Timezone:   timezone,
		location:   location,
		schedule:   parsed,
	}, nil
}

// Next returns the first scheduled instant strictly after the supplied instant.
func (schedule Schedule) Next(after time.Time) time.Time {
	if schedule.schedule == nil || schedule.location == nil {
		parsed, err := ParseSchedule(schedule.Expression, schedule.Timezone)
		if err != nil {
			return time.Time{}
		}
		schedule = parsed
	}
	local := after.In(schedule.location)
	// Evaluate cron against a timezone-free wall clock. Converting only the
	// selected wall time back into the IANA location avoids scheduling both
	// copies of a repeated fall-back time.
	wallAfter := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), time.UTC)
	wallNext := schedule.schedule.Next(wallAfter)
	candidate := time.Date(wallNext.Year(), wallNext.Month(), wallNext.Day(), wallNext.Hour(), wallNext.Minute(), 0, 0, schedule.location)
	candidateLocal := candidate.In(schedule.location)
	if sameWallMinute(candidateLocal, wallNext) && candidate.After(after) {
		return candidate.UTC()
	}
	// time.Date normalizes a nonexistent spring-forward wall time. Walk the
	// small transition window to the first valid local instant after it.
	start := candidate.Add(-3 * time.Hour).Truncate(time.Minute)
	for instant := start; instant.Before(candidate.Add(5 * time.Hour)); instant = instant.Add(time.Minute) {
		if !instant.After(after) {
			continue
		}
		value := instant.In(schedule.location)
		if value.Year() != wallNext.Year() || value.Month() != wallNext.Month() || value.Day() != wallNext.Day() {
			continue
		}
		if wallMinuteAtOrAfter(value, wallNext) {
			return instant.UTC()
		}
	}
	return time.Time{}
}

func sameWallMinute(value, wall time.Time) bool {
	return value.Year() == wall.Year() && value.Month() == wall.Month() && value.Day() == wall.Day() &&
		value.Hour() == wall.Hour() && value.Minute() == wall.Minute()
}

func wallMinuteAtOrAfter(value, wall time.Time) bool {
	return value.Hour()*60+value.Minute() >= wall.Hour()*60+wall.Minute()
}

func validateMinimumInterval(expression string) error {
	fields := strings.Fields(expression)
	minutes, err := expandNumericCronField(fields[0], 0, 59)
	if err != nil {
		return fmt.Errorf("invalid minute field: %w", err)
	}
	hours, err := expandNumericCronField(fields[1], 0, 23)
	if err != nil {
		return fmt.Errorf("invalid hour field: %w", err)
	}
	minutesOfDay := make([]int, 0, len(minutes)*len(hours))
	for _, hour := range hours {
		for _, minute := range minutes {
			minutesOfDay = append(minutesOfDay, hour*60+minute)
		}
	}
	sort.Ints(minutesOfDay)
	for index := range minutesOfDay {
		next := index + 1
		gap := 0
		if next < len(minutesOfDay) {
			gap = minutesOfDay[next] - minutesOfDay[index]
		} else {
			gap = 24*60 + minutesOfDay[0] - minutesOfDay[index]
		}
		if time.Duration(gap)*time.Minute < MinimumInterval {
			return fmt.Errorf("cron frequency must be at least five minutes")
		}
	}
	return nil
}

func expandNumericCronField(field string, minimum, maximum int) ([]int, error) {
	values := map[int]struct{}{}
	for _, item := range strings.Split(field, ",") {
		base, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			parsed, err := strconv.Atoi(stepText)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("invalid step %q", stepText)
			}
			step = parsed
		}
		start, end := minimum, maximum
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			startText, endText, _ := strings.Cut(base, "-")
			var err error
			start, err = strconv.Atoi(startText)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", startText)
			}
			end, err = strconv.Atoi(endText)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", endText)
			}
		default:
			parsed, err := strconv.Atoi(base)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", base)
			}
			start, end = parsed, parsed
		}
		if start < minimum || end > maximum || start > end {
			return nil, fmt.Errorf("range %q is outside %d-%d", base, minimum, maximum)
		}
		for value := start; value <= end; value += step {
			values[value] = struct{}{}
		}
	}
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Ints(out)
	return out, nil
}
