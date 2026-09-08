package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

// AuthorizationSubjects expands one authenticated principal into the exact
// immutable subjects used by the active generation. Implementations must not
// silently fall back to principal-only authorization when group resolution
// fails.
type AuthorizationSubjects func(context.Context, string) ([]access.SubjectRef, error)

// AuthorizedProjectModelReader is the read-side capability consumed by
// exploration handoffs. The implementation authorizes RESOURCE_USE against
// the semantic-model graph node before returning any model metadata and keeps
// the model, planner, authorization snapshot, and serving identity on one
// active lease.
type AuthorizedProjectModelReader interface {
	ProjectDefinitionReader
	AuthorizedExploreModel(context.Context, projectgraph.ResourceID, string, string) (*semanticmodel.Model, *semanticquery.CompiledModel, projectgraph.ServingIdentity, error)
}

type authorizedProjectDefinitionReader struct {
	provider projectruntime.Provider
	subjects AuthorizationSubjects
}

// NewAuthorizedProjectDefinitionReader creates the active-generation reader
// used by handoff entrypoints. It returns the existing definition interface so
// callers can keep ordinary browser projections, while the concrete value
// additionally implements AuthorizedProjectModelReader.
func NewAuthorizedProjectDefinitionReader(provider projectruntime.Provider, subjects AuthorizationSubjects) ProjectDefinitionReader {
	return authorizedProjectDefinitionReader{provider: provider, subjects: subjects}
}

func (r authorizedProjectDefinitionReader) ProjectDefinitionSnapshot(ctx context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	return activeProjectDefinitionReader{provider: r.provider}.ProjectDefinitionSnapshot(ctx)
}

// AuthorizedExploreModel acquires one lease and performs the authorization
// check before reading the requested model or planner from its runtime. The
// caller receives a detached serving identity to bind any subsequent
// canonical URL handoff to the generation that was admitted here.
func (r authorizedProjectDefinitionReader) AuthorizedExploreModel(ctx context.Context, projectID projectgraph.ResourceID, actorID, modelID string) (*semanticmodel.Model, *semanticquery.CompiledModel, projectgraph.ServingIdentity, error) {
	if r.provider == nil || r.subjects == nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("authorized active model reader is unavailable")
	}
	if err := projectID.Validate(); err != nil {
		return nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("project id: %w", err)
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("actor id is required")
	}
	modelID = strings.TrimSpace(modelID)
	modelResourceID, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("semantic model id: %w", err)
	}

	lease, err := r.provider.Acquire(ctx)
	if err != nil {
		return nil, nil, projectgraph.ServingIdentity{}, err
	}
	if lease == nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active model reader returned a nil lease")
	}
	defer lease.Release()
	identity := lease.Identity()
	if err := identity.Validate(); err != nil || identity.ProjectID != projectID {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active model serving identity is unavailable")
	}
	authorizedLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active model lease has no authorization snapshot")
	}
	snapshot := authorizedLease.AuthorizationSnapshot()
	if err := snapshot.ValidateBound(); err != nil || snapshot.Identity() != identity || snapshot.Project().ProjectID() != projectID {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active model authorization snapshot is unavailable")
	}
	resource, err := access.NewResourceRef(modelResourceID, projectgraph.KindSemanticModel)
	if err != nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model authorization is unavailable")
	}
	if err := resource.ValidateAgainst(snapshot.Project()); err != nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model authorization is unavailable")
	}
	subjects, err := r.subjects(ctx, actorID)
	if err != nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model authorization is unavailable")
	}
	allowed := false
	for _, subject := range subjects {
		if err := subject.Validate(); err != nil {
			return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model authorization is unavailable")
		}
		candidate, allowErr := snapshot.Allows(subject, resource, access.CapabilityResourceUse)
		if allowErr != nil {
			return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model authorization is unavailable")
		}
		if candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model authorization is unavailable")
	}

	runtime := lease.Runtime()
	ports, ok := runtime.(interface {
		Identity() projectgraph.ServingIdentity
		SemanticModel(string) (*semanticmodel.Model, bool)
		CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
	})
	if !ok || ports == nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model definition is unavailable")
	}
	if runtimeIdentity := ports.Identity(); runtimeIdentity.Validate() != nil || runtimeIdentity != identity {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model definition is unavailable")
	}
	model, ok := ports.SemanticModel(modelResourceID.String())
	if !ok || model == nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active semantic model definition is unavailable")
	}
	compiled, ok := ports.CompiledSemanticModel(modelResourceID.String())
	if !ok || compiled == nil || !compiled.MatchesModel(model) {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("active compiled semantic model is unavailable")
	}
	return model, compiled, identity, nil
}

var _ ProjectDefinitionReader = authorizedProjectDefinitionReader{}
var _ AuthorizedProjectModelReader = authorizedProjectDefinitionReader{}
