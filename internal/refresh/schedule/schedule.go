package schedule

import (
	"fmt"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/robfig/cron/v3"
)

// Argo's CronWorkflow uses Kubernetes' five-field robfig/cron profile.  The
// parser is intentionally descriptor-aware so the documented @daily-style
// macros are accepted, while @every (and embedded timezone descriptors) are
// rejected by ParseSchedule below.
var argoParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

const (
	// These values deliberately use the authored Argo/Kubernetes spelling.
	ConcurrencyForbid  = "Forbid"
	ConcurrencyReplace = "Replace"
	// Verbose aliases make call sites self-documenting.
	ConcurrencyPolicyForbid  = ConcurrencyForbid
	ConcurrencyPolicyReplace = ConcurrencyReplace
)

// Definition is the deployable description of one semantic-model refresh
// pipeline.  Scheduling policy is pipeline-wide: individual schedules carry
// only an evidence ID and cron expression.
type Definition struct {
	ID              projectgraph.ResourceID
	Name            string
	SemanticModelID projectgraph.ResourceID
	// SelectionDigest is computed from the canonical authored selection before
	// the compiler resolves its name to a canonical resource ID.
	SelectionDigest         string
	Timezone                string
	StartingDeadlineSeconds int64
	ConcurrencyPolicy       string
	Schedules               []Schedule
}

// Schedule is a named cron expression. Timezone is supplied by its parent
// Definition; the unexported location/parser are hydrated by ParseSchedule.
type Schedule struct {
	ID         string
	Expression string

	location *time.Location
	schedule cron.Schedule
	timezone string
}

// ParseSchedule validates an Argo v4.0.8-compatible cron expression against
// an explicit IANA timezone. Expressions are normalized for whitespace but
// otherwise retain their authored spelling for deterministic evidence.
func ParseSchedule(expression, timezone string) (Schedule, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return Schedule{}, fmt.Errorf("cron expression is required")
	}
	upperExpression := strings.ToUpper(expression)
	if strings.Contains(upperExpression, "TZ=") || strings.Contains(upperExpression, "CRON_TZ=") ||
		strings.Contains(upperExpression, "TZ =") || strings.Contains(upperExpression, "CRON_TZ =") {
		return Schedule{}, fmt.Errorf("cron timezone declarations are not supported; set pipeline timezone")
	}
	if strings.HasPrefix(strings.ToLower(expression), "@every") {
		return Schedule{}, fmt.Errorf("@every cron descriptor is not supported")
	}
	if strings.HasPrefix(expression, "@") {
		// Parse through the descriptor-enabled parser below so the error names
		// an unsupported macro when appropriate.
		lower := strings.ToLower(expression)
		switch lower {
		case "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly":
		default:
			return Schedule{}, fmt.Errorf("unsupported cron descriptor %q", expression)
		}
	} else {
		// Argo's profile is exactly five fields. robfig accepts descriptors only
		// when explicitly enabled above; reject six-field/seconds expressions.
		if len(strings.Fields(expression)) != 5 {
			return Schedule{}, fmt.Errorf("cron must be a five-field expression")
		}
		expression = strings.Join(strings.Fields(expression), " ")
	}
	if timezone == "" {
		return Schedule{}, fmt.Errorf("timezone is required and must be a valid IANA timezone")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Schedule{}, fmt.Errorf("timezone %q must be a valid IANA timezone: %w", timezone, err)
	}
	parsed, err := argoParser.Parse("CRON_TZ=" + timezone + " " + expression)
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid Argo cron expression %q: %w", expression, err)
	}
	return Schedule{Expression: expression, location: location, schedule: parsed, timezone: timezone}, nil
}

// hydrate verifies parser state on copied values. Serialized authored schedule
// values must be reparsed by the compiler with their parent pipeline timezone.
func (schedule Schedule) hydrate() (Schedule, bool) {
	if schedule.schedule != nil && schedule.location != nil {
		return schedule, true
	}
	return schedule, false
}

// Next returns the first scheduled instant strictly after after. Local wall
// times are resolved against the IANA location explicitly: nonexistent
// spring-forward matches are skipped, while both absolute instants in a
// fall-back fold are emitted in order.
func (schedule Schedule) Next(after time.Time) time.Time {
	var ok bool
	schedule, ok = schedule.hydrate()
	if !ok {
		return time.Time{}
	}
	return schedule.schedule.Next(after.UTC()).UTC()
}
