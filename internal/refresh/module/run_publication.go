package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshpresentation "github.com/flidai/leapview/internal/refresh/presentation"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

type RunPublicationEvidence = refreshpresentation.RunPublicationEvidence

type runPublicationReader interface {
	GetRunPublication(context.Context, string) (refreshpostgres.Publication, error)
}

// GetRunPublication reads a canonical publication through the native refresh
// repository. Keeping this as a narrow capability prevents UI readers from
// depending on the PostgreSQL adapter's other publication write operations.
func (p *postgresPublicationPersistence) GetRunPublication(ctx context.Context, publicationID string) (refreshpostgres.Publication, error) {
	if p == nil || p.repository == nil {
		return refreshpostgres.Publication{}, errors.New("refresh publication repository is not configured")
	}
	return p.repository.Publication(ctx, publicationID)
}

// RunPublication returns committed publication evidence for a root pipeline
// run within the requested project/environment scope. The deterministic
// publication row references the globally unique run ID; checking the scoped
// run first and matching its immutable base generation binds that row to the
// requested project/environment identity.
func (m *Module) RunPublication(ctx context.Context, scope refreshrun.ReadScope, runID string) (RunPublicationEvidence, bool, error) {
	if err := scope.Validate(); err != nil {
		return RunPublicationEvidence{}, false, err
	}
	if runID == "" || runID != strings.TrimSpace(runID) || len(runID) > 256 || strings.IndexFunc(runID, unicode.IsControl) >= 0 {
		return RunPublicationEvidence{}, false, errors.New("run id must be canonical")
	}

	runs, err := m.readRuns()
	if err != nil {
		return RunPublicationEvidence{}, false, err
	}
	reader, ok := m.publicationReader()
	if !ok {
		return RunPublicationEvidence{}, false, errors.New("refresh publication read persistence is not configured")
	}

	run, err := runs.GetRun(ctx, scope, runID)
	if err != nil {
		return RunPublicationEvidence{}, false, err
	}
	if run.ID != runID || run.Identity.Validate() != nil || !scope.Matches(run.Identity) {
		return RunPublicationEvidence{}, false, fmt.Errorf("run %q does not match the requested project/environment scope", runID)
	}
	if run.TargetType != refreshrun.TargetRefreshPipeline || run.ParentRunID != "" {
		return RunPublicationEvidence{}, false, nil
	}
	if run.PipelineID == "" || run.TargetID != run.PipelineID {
		return RunPublicationEvidence{}, false, fmt.Errorf("root refresh run %q has inconsistent pipeline identity", runID)
	}

	publicationID := "publication-canonical-" + runID
	publication, err := reader.GetRunPublication(ctx, publicationID)
	if errors.Is(err, refreshpostgres.ErrNotFound) {
		return RunPublicationEvidence{}, false, nil
	}
	if err != nil {
		return RunPublicationEvidence{}, false, err
	}
	if publication.PublicationID != publicationID || publication.RunID != run.ID || publication.BaseGenerationID != run.Identity.GenerationID ||
		publication.PlanDigest == "" || publication.PlanDigest != run.PlanDigest || publication.ExpectedTargetRevision != run.TargetRevision {
		return RunPublicationEvidence{}, false, fmt.Errorf("canonical publication identity does not match run %q", runID)
	}
	switch publication.State {
	case "pending", "failed", "fenced":
		return RunPublicationEvidence{}, false, nil
	case "committed":
		resultIdentity := projectgraph.ServingIdentity{
			ProjectID: run.Identity.ProjectID, Environment: run.Identity.Environment,
			GenerationID: publication.ResultGenerationID,
		}
		if publication.CommittedAt.IsZero() || publication.SnapshotID <= 0 || resultIdentity.Validate() != nil {
			return RunPublicationEvidence{}, false, fmt.Errorf("committed publication for run %q has incomplete evidence", runID)
		}
		return RunPublicationEvidence{
			CommittedAt: publication.CommittedAt.UTC(), SnapshotID: publication.SnapshotID,
			ResultGenerationID: publication.ResultGenerationID,
		}, true, nil
	default:
		return RunPublicationEvidence{}, false, fmt.Errorf("canonical publication for run %q has unsupported state %q", runID, publication.State)
	}
}

func (m *Module) publicationReader() (runPublicationReader, bool) {
	if m == nil || m.service.Publication == nil {
		return nil, false
	}
	reader, ok := m.service.Publication.(runPublicationReader)
	return reader, ok
}
