package app

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

type firstSourceCredentialTargetReader interface {
	Target(context.Context, string) (deploymentpostgres.DeliveryTarget, error)
}

// This scope belongs only to customer credential services. Normal connection
// catalogs, token issuance, publication and lifecycle retain their own ports.
type firstSourceCredentialServiceScope struct {
	authority       firstSourceCredentialAuthority
	targets         firstSourceCredentialTargetReader
	admissions      credentialmodule.FirstSourceAdmissionReader
	activeProject   func(context.Context) (projectgraph.ResourceID, error)
	activeAuthorize connectionAuthorization
}

func (s firstSourceCredentialServiceScope) CurrentProject(ctx context.Context) (projectgraph.ResourceID, error) {
	unpublished, err := s.unpublished(ctx)
	if err != nil {
		return "", err
	}
	if !unpublished {
		if s.activeProject == nil {
			return "", access.ErrForbidden
		}
		return s.activeProject(ctx)
	}
	if typednil.IsNil(s.admissions) {
		return "", access.ErrForbidden
	}
	admission, err := s.admissions.AdmissionForTarget(ctx, s.authority.targetID)
	if err != nil {
		return "", err
	}
	principal, found := accessmodule.PrincipalFromContext(ctx)
	if !found {
		return "", access.ErrForbidden
	}
	resource := credentialmodule.ValidationResource{
		ScopeKind: "connection", TargetID: s.authority.targetID, Environment: s.authority.environment,
		ProjectID: admission.Intent.ProjectID, ResourceID: admission.Intent.ConnectionID,
	}
	var project projectgraph.ResourceID
	err = s.authority.WithAuthorization(ctx, principal.ID, resource, access.ActionConnectionManage, func(_ context.Context, _ pgx.Tx, current credentialmodule.FirstSourceAdmission) error {
		project = projectgraph.ResourceID(current.Intent.ProjectID)
		return nil
	})
	if err != nil {
		return "", err
	}
	return project, nil
}

func (s firstSourceCredentialServiceScope) AuthorizeConnection(ctx context.Context, actor, project, connection string, action access.Action) (bool, error) {
	unpublished, err := s.unpublished(ctx)
	if err != nil {
		return false, err
	}
	if !unpublished {
		if s.activeAuthorize == nil {
			return false, access.ErrForbidden
		}
		return s.activeAuthorize(ctx, actor, project, connection, action)
	}
	// Only draft metadata Get/List reaches this port's read mapping. Exact
	// manage authorizes these credential-owned records; no ordinary connection
	// read grant, catalog permission or token ceiling is changed.
	if action == access.ActionConnectionRead {
		action = access.ActionConnectionManage
	}
	resource := credentialmodule.ValidationResource{ScopeKind: "connection", TargetID: s.authority.targetID, Environment: s.authority.environment, ProjectID: project, ResourceID: connection}
	err = s.authority.WithAuthorization(ctx, actor, resource, action, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error { return nil })
	return err == nil, err
}

func (s firstSourceCredentialServiceScope) unpublished(ctx context.Context) (bool, error) {
	if ctx == nil {
		return false, access.ErrForbidden
	}
	if !s.authority.production {
		return false, nil
	}
	if typednil.IsNil(s.targets) {
		return false, access.ErrForbidden
	}
	target, err := s.targets.Target(ctx, s.authority.targetID)
	if errors.Is(err, deploymentpostgres.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if target.TargetID != s.authority.targetID || target.Environment != s.authority.environment {
		return false, access.ErrForbidden
	}
	// Any active pointer selects normal snapshot authority, including a runtime
	// that is still warming or failed to acquire. The fenced admission repeats
	// the empty-pointer check before an unpublished decision is returned.
	return target.ActiveGenerationID == "" && target.ActivePublicationID == "", nil
}
