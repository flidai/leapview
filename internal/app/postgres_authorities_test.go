package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/release"
)

type releaseListFake struct {
	rows []release.Release
	err  error
}

func (f releaseListFake) List(context.Context, projectgraph.ResourceID) ([]release.Release, error) {
	return f.rows, f.err
}

type deploymentTargetFake struct {
	target deploymentpostgres.DeliveryTarget
	err    error
}

func (f deploymentTargetFake) Target(context.Context, string) (deploymentpostgres.DeliveryTarget, error) {
	return f.target, f.err
}

func TestPostgresAuthorityGraphValidateRejectsNilAndPartialGraphs(t *testing.T) {
	var nilGraph *PostgresAuthorityGraph
	if err := nilGraph.Validate(); err == nil || !strings.Contains(err.Error(), "graph is nil") {
		t.Fatalf("nil graph error = %v, want nil-graph rejection", err)
	}

	partial := &PostgresAuthorityGraph{}
	if err := partial.Validate(); err == nil || !strings.Contains(err.Error(), "platform bootstrap authority") {
		t.Fatalf("empty graph error = %v, want bootstrap rejection", err)
	}

	partial.Bootstrap = &platformbootstrappostgres.Repository{}
	if err := partial.Validate(); err == nil || !strings.Contains(err.Error(), "platform settings authority") {
		t.Fatalf("bootstrap-only graph error = %v, want settings rejection", err)
	}
	partial.Settings = partial.Bootstrap
	if err := partial.Validate(); err == nil || !strings.Contains(err.Error(), "operation authority") {
		t.Fatalf("bootstrap/settings graph error = %v, want operation rejection", err)
	}
}

func TestNewPostgresAuthorityGraphRejectsMissingLifecycleAndOptions(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	if _, err := NewPostgresAuthorityGraph(nil, PostgresAuthorityGraphOptions{TargetID: "target", FingerprintKey: key}); err == nil || !strings.Contains(err.Error(), "initialized runtime and maintenance pools") {
		t.Fatalf("nil lifecycle error = %v, want lifecycle rejection", err)
	}
	if _, err := NewPostgresAuthorityGraph(&postgresControlPlaneLifecycle{}, PostgresAuthorityGraphOptions{TargetID: "target", FingerprintKey: key}); err == nil || !strings.Contains(err.Error(), "initialized runtime and maintenance pools") {
		t.Fatalf("partial lifecycle error = %v, want pool rejection", err)
	}
}

func TestPostgresAuthorityGraphReadersPreserveNativeIdentity(t *testing.T) {
	latest, err := latestReleaseID(t.Context(), releaseListFake{rows: []release.Release{{ID: "release-new"}, {ID: "release-old"}}}, "project:sales")
	if err != nil || latest != "release-new" {
		t.Fatalf("latest release = %q, error = %v; want first (DESC) row", latest, err)
	}
	if _, err := latestReleaseID(t.Context(), releaseListFake{err: errors.New("list failed")}, "project:sales"); err == nil {
		t.Fatal("latest release reader swallowed repository error")
	}

	active, err := activeDeploymentID(t.Context(), deploymentTargetFake{target: deploymentpostgres.DeliveryTarget{ActiveGenerationID: "generation-1", ActivePublicationID: "publication-1"}}, "target-prod")
	if err != nil || active != "publication-1" {
		t.Fatalf("active deployment = %q, error = %v; want publication identity", active, err)
	}
	if _, err := activeDeploymentID(t.Context(), deploymentTargetFake{err: errors.New("target failed")}, "target-prod"); err == nil {
		t.Fatal("active deployment reader swallowed repository error")
	}
}

func TestPostgresAuthorityGraphDeploymentPersistenceUsesSameRepository(t *testing.T) {
	repository := deploymentpostgres.New(nil)
	persistence := &deploymentmodule.Persistence{Repository: repository}
	if !deploymentPersistenceMatches(repository, persistence) {
		t.Fatal("matching deployment repository/persistence pair was rejected")
	}
	other := deploymentpostgres.New(nil)
	if deploymentPersistenceMatches(other, persistence) {
		t.Fatal("different deployment repository identity was accepted")
	}
}

func TestPostgresAuthorityGraphRefreshAdaptersPreserveRepositoryIdentity(t *testing.T) {
	jobs := jobspostgres.New(nil)
	refresh := refreshpostgres.New(nil)
	audit := accesspostgres.New()
	jobsAdapter := refreshmodule.NewPostgresJobsAdapter(jobs, refresh)
	auditAdapter, err := refreshmodule.NewPostgresCancelAuditWriterAdapter(audit)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshJobsMatches(jobs, refresh, jobsAdapter) {
		t.Fatal("refresh jobs adapter did not retain exact Jobs and Refresh repository identities")
	}
	if !refreshCancelAuditMatches(audit, auditAdapter) {
		t.Fatal("refresh cancellation audit adapter did not retain exact Access audit identity")
	}
	if _, err := refreshmodule.NewPostgresCancelAuditWriterAdapter(nil); err == nil {
		t.Fatal("refresh cancellation audit adapter accepted a nil Access audit authority")
	}
	if refreshJobsMatches(jobspostgres.New(nil), refresh, jobsAdapter) {
		t.Fatal("refresh jobs adapter accepted a different Jobs repository")
	}
	if refreshJobsMatches(jobs, refreshpostgres.New(nil), jobsAdapter) {
		t.Fatal("refresh jobs adapter accepted a different Refresh repository")
	}
	if refreshJobsMatches(jobs, refresh, nil) || refreshCancelAuditMatches(audit, nil) {
		t.Fatal("nil refresh adapter was accepted")
	}
}
