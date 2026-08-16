package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func TestReadClaimedProjectUsesTypedNotFoundForFreshInstall(t *testing.T) {
	store := testStore(t)
	repository := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})

	projectID, found, err := readClaimedProject(repository, "dev")(context.Background())
	if err != nil {
		t.Fatalf("readClaimedProject() error = %v", err)
	}
	if found || projectID != "" {
		t.Fatalf("readClaimedProject() = %q, %v, want empty unclaimed result", projectID, found)
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

func TestResolveClaimedProjectIDRequiresDurableClaim(t *testing.T) {
	scopes := []servingstatemodule.ActiveScope{{ProjectID: "finance", Environment: "prod"}}
	if _, err := resolveClaimedProjectID(scopes, "prod", "", false); err == nil {
		t.Fatal("resolveClaimedProjectID() accepted active scope without durable claim")
	}
	if got, err := resolveClaimedProjectID(nil, "prod", "", false); err != nil || got != "" {
		t.Fatalf("fresh unclaimed resolution = %q, %v, want empty success", got, err)
	}
	if got, err := resolveClaimedProjectID(scopes, "prod", "finance", true); err != nil || got != "finance" {
		t.Fatalf("restart claim resolution = %q, %v, want finance", got, err)
	}
	if _, err := resolveClaimedProjectID(scopes, "prod", "marketing", true); err == nil {
		t.Fatal("resolveClaimedProjectID() accepted conflicting durable claim")
	}
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
