package authoring

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

var (
	ErrGenerationSuperseded = errors.New("dashboard revalidation generation was superseded")
	ErrRevalidationConflict = errors.New("dashboard revalidation evidence compare-and-swap conflict")
)

// NewRevalidationAttemptID returns an opaque identifier for one immutable
// revalidation attempt. Attempts are intentionally distinct even when a
// caller retries the same dashboard and generation with the same clock
// value.
func NewRevalidationAttemptID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate revalidation attempt ID: %w", err)
	}
	return id.String(), nil
}

func ValidateRevalidationAttemptID(value string) error {
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("revalidation attempt ID must be a canonical lowercase UUIDv7")
	}
	id, err := uuid.Parse(value)
	if err != nil || id.Version() != 7 || id.String() != value {
		return fmt.Errorf("revalidation attempt ID must be a canonical lowercase UUIDv7")
	}
	return nil
}

// RevalidationGeneration is the immutable graph and authorization snapshot
// that a generation activation made visible. ChangedIDs are canonical graph
// ResourceIDs; selection never uses symbolic names or source paths.
type RevalidationGeneration struct {
	Identity      projectgraph.ServingIdentity
	Graph         projectgraph.ProjectGraph
	Authorization accesssnapshot.AuthorizationSnapshot
	ChangedIDs    []projectgraph.ResourceID
}

func (g RevalidationGeneration) Validate() error {
	if err := g.Identity.Validate(); err != nil {
		return fmt.Errorf("revalidation identity: %w", err)
	}
	if err := g.Graph.Validate(); err != nil {
		return fmt.Errorf("revalidation graph: %w", err)
	}
	if g.Identity.ProjectID != g.Graph.ProjectID() {
		return fmt.Errorf("revalidation identity project %q does not match graph %q", g.Identity.ProjectID, g.Graph.ProjectID())
	}
	if err := g.Authorization.Validate(g.Graph); err != nil {
		return fmt.Errorf("revalidation authorization snapshot: %w", err)
	}
	if g.Authorization.Identity() != g.Identity {
		return fmt.Errorf("revalidation authorization identity does not match generation")
	}
	for _, id := range g.ChangedIDs {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("revalidation changed resource id: %w", err)
		}
	}
	return nil
}

type RevalidationCommit struct {
	AttemptID        string
	Generation       RevalidationGeneration
	Dashboard        DashboardLifecycle
	AuthoredRevision Revision
	PriorCompilation CompiledRevisionToken
	Compilation      CompiledRevision
	DependencyIDs    []projectgraph.ResourceID
	AttemptedAt      time.Time
}

type RevalidationFailureInput struct {
	AttemptID        string
	Generation       RevalidationGeneration
	Dashboard        DashboardLifecycle
	AuthoredRevision Revision
	PriorCompilation CompiledRevisionToken
	DependencyIDs    []projectgraph.ResourceID
	Failure          RevalidationFailure
}

// RevalidationStore is the transaction boundary. Commit must atomically
// insert immutable compiled/attempt evidence and advance only the published
// pointer whose authored revision and prior compilation still match. Failure
// recording must not alter the published pointer.
type RevalidationStore interface {
	List(context.Context, projectgraph.ResourceID) ([]DashboardLifecycle, error)
	GetRevision(context.Context, projectgraph.ResourceID, DashboardID, RevisionID) (Revision, error)
	CommitRevalidation(context.Context, RevalidationCommit) error
	RecordRevalidationFailure(context.Context, RevalidationFailureInput) error
}

// RevalidationCompiler compiles one retained authored document against the
// exact generation graph and authorization snapshot. Implementations may use a
// runtime lease, but must return evidence bound to Generation.Identity.
type RevalidationCompiler interface {
	Compile(context.Context, RevalidationGeneration, Revision) (CompiledRevision, error)
}

type RevalidationResultStatus string

const (
	RevalidationSkipped    RevalidationResultStatus = "skipped"
	RevalidationSucceeded  RevalidationResultStatus = "succeeded"
	RevalidationFailed     RevalidationResultStatus = "failed"
	RevalidationSuperseded RevalidationResultStatus = "superseded"
)

type RevalidationResult struct {
	DashboardID  DashboardID
	Status       RevalidationResultStatus
	Dependencies []projectgraph.ResourceID
	Failure      *RevalidationFailure
	Err          error
}

type GenerationRevalidator struct {
	store    RevalidationStore
	compiler RevalidationCompiler
	clock    func() time.Time
}

func NewGenerationRevalidator(store RevalidationStore, compiler RevalidationCompiler, now func() time.Time) (*GenerationRevalidator, error) {
	if store == nil || compiler == nil {
		return nil, fmt.Errorf("dashboard revalidation store and compiler are required")
	}
	if now == nil {
		now = time.Now
	}
	return &GenerationRevalidator{store: store, compiler: compiler, clock: now}, nil
}

// GenerationActivated revalidates only instance-managed published dashboards
// whose graph dependency closure intersects ChangedIDs. Each dashboard is
// isolated: a failed compile records actionable failure evidence and does not
// prevent unrelated dashboards from advancing.
func (r *GenerationRevalidator) GenerationActivated(ctx context.Context, generation RevalidationGeneration) ([]RevalidationResult, error) {
	if r == nil || r.store == nil || r.compiler == nil {
		return nil, fmt.Errorf("dashboard revalidation service is not configured")
	}
	if err := generation.Validate(); err != nil {
		return nil, err
	}
	lifecycles, err := r.store.List(ctx, generation.Identity.ProjectID)
	if err != nil {
		return nil, err
	}
	affected := generation.Graph.AffectedDashboards(generation.ChangedIDs)
	affectedSet := make(map[DashboardID]struct{}, len(affected))
	for _, id := range affected {
		affectedSet[id] = struct{}{}
	}
	results := make([]RevalidationResult, 0, len(lifecycles))
	for _, lifecycle := range lifecycles {
		if lifecycle.Status != LifecycleStatusPublished || lifecycle.Published == nil {
			continue
		}
		if resource, ok := generation.Graph.Resource(lifecycle.ID); ok && resource.Provenance.Origin == "project" {
			// Project-managed dashboards are owned by the immutable artifact and
			// remain read-only to this instance authoring projection.
			continue
		}
		if _, ok := generation.Graph.Resource(lifecycle.ID); !ok && dependencyClosureIntersects(generation.Graph, lifecycle.SemanticModel, generation.ChangedIDs) {
			// Instance-managed dashboards may not be materialized as graph nodes;
			// their exact semantic-model ResourceID still gives us a selective
			// dependency boundary.
			affectedSet[lifecycle.ID] = struct{}{}
		}
		if _, ok := affectedSet[lifecycle.ID]; !ok {
			continue
		}
		dependencies := generation.Graph.Dependencies(lifecycle.ID)
		if _, ok := generation.Graph.Resource(lifecycle.ID); !ok {
			dependencies = append([]projectgraph.ResourceID{lifecycle.ID}, generation.Graph.Dependencies(lifecycle.SemanticModel)...)
			sort.Slice(dependencies, func(i, j int) bool { return dependencies[i] < dependencies[j] })
		}
		result := RevalidationResult{DashboardID: lifecycle.ID, Dependencies: dependencies}
		if dependencyErr := validateDashboardDependency(generation.Graph, lifecycle); dependencyErr != nil {
			results = append(results, r.recordFailure(ctx, generation, lifecycle, authoringRevisionZero(lifecycle), dependencies, "INVALID_DEPENDENCY", dependencyErr))
			continue
		}
		revision, loadErr := r.store.GetRevision(ctx, lifecycle.ProjectID, lifecycle.ID, lifecycle.Published.Revision.RevisionID)
		if loadErr != nil {
			result = r.recordFailure(ctx, generation, lifecycle, authoringRevisionZero(lifecycle), dependencies, "REVISION_UNAVAILABLE", loadErr)
			results = append(results, result)
			continue
		}
		compiled, compileErr := r.compiler.Compile(ctx, generation, revision)
		if compileErr == nil {
			compileErr = validateRecompiledEvidence(generation, lifecycle, revision, compiled)
		}
		if compileErr != nil {
			results = append(results, r.recordFailure(ctx, generation, lifecycle, revision, dependencies, "REVALIDATION_FAILED", compileErr))
			continue
		}
		attemptedAt := r.clock().UTC()
		if attemptedAt.IsZero() {
			attemptedAt = time.Now().UTC()
		}
		attemptID, attemptErr := NewRevalidationAttemptID()
		if attemptErr != nil {
			return results, attemptErr
		}
		commitErr := r.store.CommitRevalidation(ctx, RevalidationCommit{AttemptID: attemptID, Generation: generation, Dashboard: lifecycle, AuthoredRevision: revision, PriorCompilation: lifecycle.Published.Compilation, Compilation: compiled, DependencyIDs: dependencies, AttemptedAt: attemptedAt})
		if errors.Is(commitErr, ErrGenerationSuperseded) {
			result.Status, result.Err = RevalidationSuperseded, commitErr
		} else if commitErr != nil {
			result.Status, result.Err = RevalidationFailed, commitErr
		} else {
			result.Status = RevalidationSucceeded
		}
		results = append(results, result)
	}
	return results, nil
}

func validateDashboardDependency(project projectgraph.ProjectGraph, lifecycle DashboardLifecycle) error {
	if dashboard, ok := project.Resource(lifecycle.ID); ok && dashboard.Kind != projectgraph.KindDashboard {
		return fmt.Errorf("dashboard ResourceID %q has the wrong graph kind", lifecycle.ID)
	}
	model, ok := project.Resource(lifecycle.SemanticModel)
	if !ok || model.Kind != projectgraph.KindSemanticModel {
		return fmt.Errorf("semantic model ResourceID %q is absent or has the wrong graph kind", lifecycle.SemanticModel)
	}
	for _, dependency := range project.Dependencies(lifecycle.ID) {
		if dependency == lifecycle.SemanticModel {
			return nil
		}
	}
	return fmt.Errorf("dashboard %q has no graph dependency edge to semantic model %q", lifecycle.ID, lifecycle.SemanticModel)
}

func dependencyClosureIntersects(project projectgraph.ProjectGraph, root projectgraph.ResourceID, changed []projectgraph.ResourceID) bool {
	changedSet := make(map[projectgraph.ResourceID]struct{}, len(changed))
	for _, id := range changed {
		changedSet[id] = struct{}{}
	}
	for _, dependency := range project.Dependencies(root) {
		if _, ok := changedSet[dependency]; ok {
			return true
		}
	}
	return false
}

func (r *GenerationRevalidator) recordFailure(ctx context.Context, generation RevalidationGeneration, lifecycle DashboardLifecycle, revision Revision, dependencies []projectgraph.ResourceID, code string, cause error) RevalidationResult {
	failedAt := r.clock().UTC()
	if failedAt.IsZero() {
		failedAt = time.Now().UTC()
	}
	failure := RevalidationFailure{Identity: generation.Identity, DependencyIDs: dependencies, Code: code, Message: strings.TrimSpace(cause.Error()), FailedAt: failedAt}
	attemptID, attemptErr := NewRevalidationAttemptID()
	result := RevalidationResult{DashboardID: lifecycle.ID, Status: RevalidationFailed, Dependencies: dependencies, Failure: &failure, Err: cause}
	if attemptErr != nil {
		result.Err = errors.Join(result.Err, attemptErr)
		return result
	}
	input := RevalidationFailureInput{AttemptID: attemptID, Generation: generation, Dashboard: lifecycle, AuthoredRevision: revision, PriorCompilation: lifecycle.Published.Compilation, DependencyIDs: dependencies, Failure: failure}
	if err := r.store.RecordRevalidationFailure(ctx, input); err != nil {
		result.Err = errors.Join(cause, err)
	}
	return result
}

func authoringRevisionZero(lifecycle DashboardLifecycle) Revision {
	if lifecycle.Published == nil {
		return Revision{}
	}
	return Revision{DashboardID: lifecycle.ID, ID: lifecycle.Published.Revision.RevisionID, Number: lifecycle.Published.Revision.Number, ContentHash: lifecycle.Published.Revision.ContentHash}
}

func validateRecompiledEvidence(generation RevalidationGeneration, lifecycle DashboardLifecycle, revision Revision, compiled CompiledRevision) error {
	if err := compiled.Validate(); err != nil {
		return err
	}
	if compiled.ProjectID != generation.Identity.ProjectID || compiled.DashboardID != lifecycle.ID {
		return fmt.Errorf("compiled evidence identity does not match generation dashboard")
	}
	if compiled.AuthoredRevision != revision.Token() || compiled.AuthoredRevision != lifecycle.Published.Revision {
		return fmt.Errorf("compiled evidence authored revision is stale")
	}
	if compiled.SemanticModelID != lifecycle.SemanticModel || compiled.Definition.SemanticModel != lifecycle.SemanticModel.String() {
		return fmt.Errorf("compiled evidence semantic model does not match dashboard ResourceID")
	}
	if compiled.SemanticIdentity != generation.Identity {
		return fmt.Errorf("compiled evidence serving identity does not match activated generation")
	}
	return nil
}

// SortResults gives callers a deterministic order when stores return rows in
// database order that is not guaranteed by the underlying engine.
func SortResults(results []RevalidationResult) {
	sort.Slice(results, func(i, j int) bool { return results[i].DashboardID < results[j].DashboardID })
}
