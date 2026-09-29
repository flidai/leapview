package module

import (
	"context"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

type runPublicationTestRuns struct {
	RunPersistence
	run refreshrun.RunRecord
	err error
}

func (r runPublicationTestRuns) GetRun(context.Context, refreshrun.ReadScope, string) (refreshrun.RunRecord, error) {
	return r.run, r.err
}

type runPublicationTestStore struct {
	refreshrun.CanonicalPublicationUnitOfWork
	publication refreshpostgres.Publication
	err         error
	requestedID string
}

func (s *runPublicationTestStore) GetRunPublication(_ context.Context, id string) (refreshpostgres.Publication, error) {
	s.requestedID = id
	return s.publication, s.err
}

func TestModuleRunPublicationRequiresCommittedScopedEvidence(t *testing.T) {
	committedAt := time.Date(2026, time.September, 22, 12, 30, 0, 0, time.UTC)
	projectID := projectgraph.ResourceID("project:analytics")
	identity := projectgraph.ServingIdentity{ProjectID: projectID, Environment: "prod", GenerationID: "generation:base"}
	scope := refreshrun.ReadScope{ProjectID: projectID, Environment: "prod"}
	baseRun := refreshrun.RunRecord{
		ID: "run-analytics-1", Identity: identity, TargetType: refreshrun.TargetRefreshPipeline,
		PipelineID: "pipeline:analytics", TargetID: "pipeline:analytics", TargetRevision: 3,
		PlanDigest: "sha256:plan", Status: refreshrun.RunStatusSucceeded,
	}
	basePublication := refreshpostgres.Publication{
		PublicationInput: refreshpostgres.PublicationInput{
			PublicationID: "publication-canonical-run-analytics-1", RunID: baseRun.ID,
			BaseGenerationID: identity.GenerationID, ResultGenerationID: "generation:result", PlanDigest: baseRun.PlanDigest,
			ExpectedTargetRevision: baseRun.TargetRevision, SnapshotID: 47,
		},
		State: "committed", CommittedAt: committedAt,
	}

	tests := []struct {
		name           string
		scope          refreshrun.ReadScope
		run            refreshrun.RunRecord
		publication    refreshpostgres.Publication
		publicationErr error
		wantFound      bool
		wantErr        bool
	}{
		{name: "committed publication", scope: scope, run: baseRun, publication: basePublication, wantFound: true},
		{name: "successful run without publication", scope: scope, run: baseRun, publicationErr: refreshpostgres.ErrNotFound},
		{name: "pending publication", scope: scope, run: baseRun, publication: func() refreshpostgres.Publication {
			p := basePublication
			p.State = "pending"
			p.SnapshotID = 0
			p.CommittedAt = time.Time{}
			return p
		}()},
		{name: "fenced publication", scope: scope, run: baseRun, publication: func() refreshpostgres.Publication {
			p := basePublication
			p.State = "fenced"
			return p
		}()},
		{name: "run scope mismatch", scope: refreshrun.ReadScope{ProjectID: projectID, Environment: "dev"}, run: baseRun, publication: basePublication, wantErr: true},
		{name: "publication belongs to another run", scope: scope, run: baseRun, publication: func() refreshpostgres.Publication { p := basePublication; p.RunID = "run-other"; return p }(), wantErr: true},
		{name: "publication belongs to another generation", scope: scope, run: baseRun, publication: func() refreshpostgres.Publication {
			p := basePublication
			p.BaseGenerationID = "generation:other"
			return p
		}(), wantErr: true},
		{name: "publication plan differs from run", scope: scope, run: baseRun, publication: func() refreshpostgres.Publication { p := basePublication; p.PlanDigest = "sha256:other"; return p }(), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &runPublicationTestStore{publication: tt.publication, err: tt.publicationErr}
			module := &Module{
				runs:    runPublicationTestRuns{run: tt.run},
				service: refreshrun.Service{Publication: store},
			}

			got, found, err := module.RunPublication(t.Context(), tt.scope, baseRun.ID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RunPublication error = %v, wantErr %v", err, tt.wantErr)
			}
			if found != tt.wantFound {
				t.Fatalf("RunPublication found = %v, want %v", found, tt.wantFound)
			}
			if tt.wantFound {
				if !got.CommittedAt.Equal(committedAt) || got.SnapshotID != 47 || got.ResultGenerationID != "generation:result" {
					t.Fatalf("RunPublication evidence = %#v", got)
				}
				if store.requestedID != "publication-canonical-"+baseRun.ID {
					t.Fatalf("publication ID read = %q, want deterministic canonical ID", store.requestedID)
				}
			} else if got != (RunPublicationEvidence{}) {
				t.Fatalf("missing evidence = %#v, want zero value", got)
			}
		})
	}
}

func TestModuleRunPublicationRejectsNonRootRunWithoutReadingPublication(t *testing.T) {
	projectID := projectgraph.ResourceID("project:analytics")
	run := refreshrun.RunRecord{
		ID: "child-run", Identity: projectgraph.ServingIdentity{ProjectID: projectID, Environment: "prod", GenerationID: "generation:base"},
		TargetType: refreshrun.TargetModel, ParentRunID: "pipeline-run", Status: refreshrun.RunStatusSucceeded,
	}
	store := &runPublicationTestStore{}
	module := &Module{
		runs:    runPublicationTestRuns{run: run},
		service: refreshrun.Service{Publication: store},
	}

	_, found, err := module.RunPublication(t.Context(), refreshrun.ReadScope{ProjectID: projectID, Environment: "prod"}, run.ID)
	if err != nil || found {
		t.Fatalf("child RunPublication = found %v, err %v; want no evidence", found, err)
	}
	if store.requestedID != "" {
		t.Fatalf("publication read ID = %q, want no publication read for child run", store.requestedID)
	}
}

func TestModuleRunPublicationValidatesScopeAndRunID(t *testing.T) {
	projectID := projectgraph.ResourceID("project:analytics")
	store := &runPublicationTestStore{err: refreshpostgres.ErrNotFound}
	module := &Module{service: refreshrun.Service{Publication: store}}
	if _, _, err := module.RunPublication(t.Context(), refreshrun.ReadScope{}, "run-1"); err == nil {
		t.Fatal("RunPublication with invalid scope succeeded")
	}

	module.runs = runPublicationTestRuns{}
	if _, _, err := module.RunPublication(t.Context(), refreshrun.ReadScope{ProjectID: projectID, Environment: "prod"}, " run-1"); err == nil {
		t.Fatal("RunPublication with noncanonical run ID succeeded")
	}
}
