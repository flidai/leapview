package schedule

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var artifactDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ErrOccurrenceSkipped reports a terminal policy decision that has already
// been persisted by the trigger implementation. The scheduler must not
// release such an occurrence for retry.
var ErrOccurrenceSkipped = errors.New("refresh occurrence skipped")

const (
	DataVersionSourcePublish = "publish"
	DataVersionSourceRefresh = "refresh"
)

type ReconcileInput struct {
	Identity       projectgraph.ServingIdentity
	ArtifactDigest string
	Pipelines      []Definition
	Now            time.Time
}

type Occurrence struct {
	// Storage claim identity is opaque to scheduling policy but must round-trip
	// unchanged so release/attach operations remain owner- and fence-bound.
	OccurrenceID   string
	LeaseOwner     string
	LeaseRevision  int64
	LeaseExpiresAt time.Time
	Identity       projectgraph.ServingIdentity
	PipelineID     projectgraph.ResourceID
	// MatchingScheduleIDs is evidence only. It is sorted canonically and does
	// not participate in occurrence uniqueness, so overlapping expressions and
	// schedule renames cannot create duplicate executions.
	MatchingScheduleIDs []string
	SemanticModelID     projectgraph.ResourceID
	ArtifactDigest      string
	Timezone            string
	ScheduledAt         time.Time
}

type DataVersion struct {
	Identity        projectgraph.ServingIdentity
	SemanticModelID projectgraph.ResourceID
	SnapshotID      int64
	RefreshedAt     time.Time
	Source          string
	PipelineID      projectgraph.ResourceID
	RunID           string
	TargetRevision  int64
	LeaseOwner      string
	LeaseRevision   int64
}

type Repository interface {
	Reconcile(context.Context, ReconcileInput) error
	ClaimDue(context.Context, projectgraph.ServingIdentity, time.Time) ([]Occurrence, error)
	ReleaseOccurrence(context.Context, Occurrence) error
	NextRun(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID) (time.Time, bool, error)
	SaveDataVersion(context.Context, DataVersion) error
	DataVersion(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID) (DataVersion, bool, error)
}

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// Trigger atomically admits a claimed occurrence and attaches its run. A nil
// error means the occurrence is already durable; the scheduler must not write
// a second attachment after this boundary.
type Trigger func(context.Context, Occurrence) error

// ValidateScope checks that a schedule record is bound to one immutable
// project/environment/generation scope.  A serving generation is never
// inferred from a removed container or serving-state identifier.
func ValidateScope(identity projectgraph.ServingIdentity) error {
	if identity.ProjectID == "" || identity.Environment == "" {
		return errors.New("refresh serving identity project and environment are required")
	}
	return identity.Validate()
}

// ValidateArtifactDigest accepts only the canonical public artifact digest;
// callers must not trim or normalize an authored value before validating it.
func ValidateArtifactDigest(value string) error {
	if !artifactDigestPattern.MatchString(value) {
		return errors.New("artifact digest must be canonical sha256")
	}
	return nil
}

func ValidateOperationalID(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("operational identifier must be non-empty and canonical")
	}
	return nil
}

func (definition Definition) Validate() error {
	if err := definition.ID.Validate(); err != nil {
		return err
	}
	if err := definition.SemanticModelID.Validate(); err != nil {
		return err
	}
	if len(definition.Schedules) == 0 {
		if definition.Timezone != "" || definition.ConcurrencyPolicy != "" || definition.StartingDeadlineSeconds != 0 {
			return errors.New("manual-only refresh pipeline must not declare scheduling policy")
		}
		return nil
	}
	if definition.StartingDeadlineSeconds < 0 {
		return errors.New("refresh pipeline starting deadline seconds must not be negative")
	}
	if definition.Timezone == "" {
		return errors.New("refresh pipeline timezone is required when schedules exist")
	}
	if _, err := time.LoadLocation(definition.Timezone); err != nil {
		return fmt.Errorf("refresh pipeline timezone %q must be a valid IANA timezone: %w", definition.Timezone, err)
	}
	if definition.ConcurrencyPolicy != ConcurrencyForbid && definition.ConcurrencyPolicy != ConcurrencyReplace {
		return errors.New("refresh pipeline concurrency policy must be Forbid or Replace when schedules exist")
	}
	seenIDs := map[string]struct{}{}
	for _, item := range definition.Schedules {
		if err := ValidateOperationalID(item.ID); err != nil {
			return fmt.Errorf("refresh pipeline schedule id: %w", err)
		}
		if _, exists := seenIDs[item.ID]; exists {
			return fmt.Errorf("refresh pipeline schedule id %q is duplicated", item.ID)
		}
		seenIDs[item.ID] = struct{}{}
		if _, err := ParseSchedule(item.Expression, definition.Timezone); err != nil {
			return fmt.Errorf("refresh pipeline schedule %q: %w", item.ID, err)
		}
	}
	return nil
}

type Scheduler struct {
	Repository      Repository
	Clock           Clock
	Trigger         Trigger
	ResolveIdentity func(context.Context) (projectgraph.ServingIdentity, error)
}

func (scheduler Scheduler) DispatchDue(ctx context.Context) error {
	clock := scheduler.Clock
	if clock == nil {
		clock = RealClock{}
	}
	if scheduler.Repository == nil {
		return errors.New("refresh scheduler repository is required")
	}
	if scheduler.ResolveIdentity == nil {
		return errors.New("refresh scheduler identity resolver is required")
	}
	identity, err := scheduler.ResolveIdentity(ctx)
	if err != nil {
		return err
	}
	if err := ValidateScope(identity); err != nil {
		return err
	}
	if scheduler.Trigger == nil {
		return errors.New("refresh scheduler trigger is required")
	}
	occurrences, err := scheduler.Repository.ClaimDue(ctx, identity, clock.Now())
	if err != nil {
		return err
	}
	var dispatchErrors []error
	for _, occurrence := range occurrences {
		triggerErr := scheduler.Trigger(ctx, occurrence)
		if triggerErr != nil {
			if errors.Is(triggerErr, ErrOccurrenceSkipped) {
				continue
			}
			dispatchErrors = append(dispatchErrors, triggerErr)
			if releaseErr := scheduler.Repository.ReleaseOccurrence(ctx, occurrence); releaseErr != nil {
				dispatchErrors = append(dispatchErrors, releaseErr)
			}
			continue
		}
	}
	return errors.Join(dispatchErrors...)
}
