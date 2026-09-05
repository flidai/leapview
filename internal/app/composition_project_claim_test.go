package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func TestAuthorizationSnapshotInstallerUsesAccessPersistence(t *testing.T) {
	store := testStore(t)
	installer, err := authorizationSnapshotInstaller(testAccessRepository(store))
	if err != nil {
		t.Fatalf("authorizationSnapshotInstaller() error = %v", err)
	}
	if installer == nil {
		t.Fatal("authorizationSnapshotInstaller() returned nil installer")
	}
}

func TestAuthorizationSnapshotInstallerRequiresAccessPersistence(t *testing.T) {
	if _, err := authorizationSnapshotInstaller(nil); err == nil {
		t.Fatal("authorizationSnapshotInstaller() accepted a nil repository")
	}
}

func TestReadClaimedProjectFailsClosedAndChecksEnvironment(t *testing.T) {
	claim := deployment.ProjectClaim{ProjectID: "finance", Environment: "prod", ClaimedBy: "principal", ClaimedAt: time.Now().UTC()}
	for _, test := range []struct {
		name    string
		repo    deployment.ProjectClaimRepository
		env     servingstatemodule.Environment
		wantErr string
	}{
		{name: "repository error", repo: projectClaimRepositoryStub{err: errors.New("database unavailable")}, env: "prod", wantErr: "read claimed project"},
		{name: "environment mismatch", repo: projectClaimRepositoryStub{claim: claim}, env: "dev", wantErr: "does not match configured environment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectID, found, err := readClaimedProject(test.repo, test.env)(context.Background())
			if err == nil || !containsError(err, test.wantErr) {
				t.Fatalf("readClaimedProject() error = %v, want %q", err, test.wantErr)
			}
			if found || projectID != "" {
				t.Fatalf("readClaimedProject() = %q, %v after error, want empty result", projectID, found)
			}
		})
	}
}

func TestPostgresAuthoringProjectIDResolverFreshClaimedAndCorruptStates(t *testing.T) {
	claim := deployment.ProjectClaim{ProjectID: "finance", Environment: "prod", ClaimedBy: "principal", ClaimedAt: time.Now().UTC()}
	readErr := errors.New("claim database unavailable")
	stateErr := errors.New("serving state unavailable")
	for _, test := range []struct {
		name    string
		claim   projectClaimRepositoryStub
		states  authoringServingStateReaderStub
		want    projectgraph.ResourceID
		wantErr string
	}{
		{name: "fresh target", claim: projectClaimRepositoryStub{err: deployment.ErrProjectClaimNotFound}, want: ""},
		{name: "claimed target", claim: projectClaimRepositoryStub{claim: claim}, states: authoringServingStateReaderStub{scopes: []servingstatemodule.ActiveScope{{ProjectID: "finance", Environment: "prod"}}}, want: "finance"},
		{name: "active without claim", claim: projectClaimRepositoryStub{err: deployment.ErrProjectClaimNotFound}, states: authoringServingStateReaderStub{scopes: []servingstatemodule.ActiveScope{{ProjectID: "finance", Environment: "prod"}}}, wantErr: "has no durable project claim"},
		{name: "claim read error", claim: projectClaimRepositoryStub{err: readErr}, wantErr: readErr.Error()},
		{name: "serving state read error", claim: projectClaimRepositoryStub{claim: claim}, states: authoringServingStateReaderStub{err: stateErr}, wantErr: stateErr.Error()},
		{name: "claim and active scope disagree", claim: projectClaimRepositoryStub{claim: claim}, states: authoringServingStateReaderStub{scopes: []servingstatemodule.ActiveScope{{ProjectID: "marketing", Environment: "prod"}}}, wantErr: "disagrees"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := postgresAuthoringProjectIDResolver(test.claim, test.states, "instance", "prod")
			got, err := resolver(t.Context())
			if test.wantErr == "" {
				if err != nil || got != test.want {
					t.Fatalf("resolver() = %q, %v, want %q", got, err, test.want)
				}
				return
			}
			if err == nil || !containsError(err, test.wantErr) {
				t.Fatalf("resolver() error = %v, want %q", err, test.wantErr)
			}
			if got != "" {
				t.Fatalf("resolver() returned %q after error", got)
			}
		})
	}
}

func TestPostgresAuthoringProjectIDResolverIgnoresUnrelatedActiveTargets(t *testing.T) {
	claim := deployment.ProjectClaim{ProjectID: "finance", Environment: "prod", ClaimedBy: "principal", ClaimedAt: time.Now().UTC()}
	reader := targetScopedAuthoringServingStateReader{scopes: map[string]servingstatemodule.ActiveScope{
		"stale-target": {ProjectID: "marketing", Environment: "prod"},
	}}
	resolver := postgresAuthoringProjectIDResolver(projectClaimRepositoryStub{claim: claim}, reader, "instance", "prod")
	if got, err := resolver(t.Context()); err != nil || got != claim.ProjectID {
		t.Fatalf("resolver() = %q, %v, want %q despite unrelated active target", got, err, claim.ProjectID)
	}
}

type authoringServingStateReaderStub struct {
	scopes []servingstatemodule.ActiveScope
	err    error
}

type targetScopedAuthoringServingStateReader struct {
	scopes map[string]servingstatemodule.ActiveScope
	err    error
}

func (s targetScopedAuthoringServingStateReader) ActiveScopeForTarget(_ context.Context, targetID string) (servingstatemodule.ActiveScope, bool, error) {
	if s.err != nil {
		return servingstatemodule.ActiveScope{}, false, s.err
	}
	scope, ok := s.scopes[targetID]
	return scope, ok, nil
}

func (s authoringServingStateReaderStub) ListActiveScopes(context.Context) ([]servingstatemodule.ActiveScope, error) {
	return s.scopes, s.err
}

func (s authoringServingStateReaderStub) ActiveScopeForTarget(context.Context, string) (servingstatemodule.ActiveScope, bool, error) {
	if s.err != nil {
		return servingstatemodule.ActiveScope{}, false, s.err
	}
	if len(s.scopes) == 0 {
		return servingstatemodule.ActiveScope{}, false, nil
	}
	return s.scopes[0], true, nil
}

func TestBindClaimedProjectEnforcesConfiguredEnvironment(t *testing.T) {
	binder := &projectClaimBinderStub{}
	bind := bindClaimedProject(binder, "prod")
	if err := bind(context.Background(), "finance", "staging"); err == nil {
		t.Fatal("bindClaimedProject() accepted a different environment")
	}
	if binder.called {
		t.Fatal("runtime binder called for environment mismatch")
	}
	binder.err = errors.New("project binding conflict")
	if err := bind(context.Background(), "finance", "prod"); !errors.Is(err, binder.err) {
		t.Fatalf("bindClaimedProject() error = %v, want %v", err, binder.err)
	}
	if !binder.called || binder.projectID != "finance" || binder.environment != "prod" {
		t.Fatalf("runtime bind = %#v, want finance/prod", binder)
	}
}

type projectClaimRepositoryStub struct {
	claim deployment.ProjectClaim
	err   error
}

func (r projectClaimRepositoryStub) ClaimProject(context.Context, deployment.ProjectClaimInput) (deployment.ProjectClaim, error) {
	return deployment.ProjectClaim{}, errors.New("not implemented")
}

func (r projectClaimRepositoryStub) GetProjectClaim(context.Context) (deployment.ProjectClaim, error) {
	if r.err != nil {
		return deployment.ProjectClaim{}, r.err
	}
	return r.claim, nil
}

type projectClaimBinderStub struct {
	called      bool
	projectID   projectgraph.ResourceID
	environment servingstatemodule.Environment
	err         error
}

func (r *projectClaimBinderStub) BindClaimedProject(projectID projectgraph.ResourceID, environment servingstatemodule.Environment) error {
	r.called = true
	r.projectID = projectID
	r.environment = environment
	return r.err
}

func containsError(err error, want string) bool {
	return err != nil && len(want) > 0 && strings.Contains(err.Error(), want)
}

var _ deployment.ProjectClaimRepository = projectClaimRepositoryStub{}
