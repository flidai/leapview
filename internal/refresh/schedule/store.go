package schedule

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var artifactDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

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
	Identity        projectgraph.ServingIdentity
	PipelineID      projectgraph.ResourceID
	SemanticModelID projectgraph.ResourceID
	ArtifactDigest  string
	ScheduledAt     time.Time
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
	AttachRun(context.Context, Occurrence, string) error
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

type Trigger func(context.Context, Occurrence) (string, error)

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
		runID, triggerErr := scheduler.Trigger(ctx, occurrence)
		if triggerErr != nil {
			dispatchErrors = append(dispatchErrors, triggerErr)
			if releaseErr := scheduler.Repository.ReleaseOccurrence(ctx, occurrence); releaseErr != nil {
				dispatchErrors = append(dispatchErrors, releaseErr)
			}
			continue
		}
		if err := scheduler.Repository.AttachRun(ctx, occurrence, runID); err != nil {
			dispatchErrors = append(dispatchErrors, err)
		}
	}
	return errors.Join(dispatchErrors...)
}
