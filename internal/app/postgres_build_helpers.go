package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/app/config"
	appobjectstore "github.com/flidai/leapview/internal/app/objectstore"
	projectsource "github.com/flidai/leapview/internal/app/projectsource"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/flidai/leapview/internal/servingstate"
)

// nativeProjectSourceComposition keeps the native source reader's authority
// set visible to composition tests. The synchronizer remains the sole
// capability injected into release.
type nativeProjectSourceComposition struct {
	Objects               platformobjectstore.ImmutableStore
	StorageSecurityDomain string
	Sources               projectsource.NativeSourceRepository
	CandidateSourceReader *projectsource.NativeCandidateSourceSynchronizer
}

type candidateApprovalServingStateReader interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
}

// candidateApprovalPermissions compiles the immutable authorization policy
// attached to the exact not-yet-active generation. This is the reviewer
// authority for a first publication: it does not depend on a preview runtime
// having been opened and never consults mutable source files.
func candidateApprovalPermissions(
	ctx context.Context,
	states candidateApprovalServingStateReader,
	objects projectbundle.ArtifactObjectReader,
	subjects func(context.Context, string) ([]access.SubjectRef, error),
	generationID, principalID string,
) (string, string, []access.PermissionPair, error) {
	if states == nil || objects == nil || subjects == nil || strings.TrimSpace(generationID) == "" || strings.TrimSpace(principalID) == "" {
		return "", "", nil, errors.New("candidate approval authorization dependencies are unavailable")
	}
	id := servingstate.ID(strings.TrimSpace(generationID))
	state, err := states.ByID(ctx, id)
	if err != nil {
		return "", "", nil, fmt.Errorf("read candidate approval generation: %w", err)
	}
	if state.ID != id || state.ProjectID.Validate() != nil || strings.TrimSpace(string(state.Environment)) == "" || state.Status != servingstate.StatusValidated {
		return "", "", nil, errors.New("candidate approval generation identity is invalid")
	}
	artifact, err := states.ArtifactByServingState(ctx, id)
	if err != nil {
		return "", "", nil, fmt.Errorf("read candidate approval artifact: %w", err)
	}
	if artifact.ServingStateID != id || artifact.Digest != state.Digest {
		return "", "", nil, errors.New("candidate approval artifact differs from its generation")
	}
	compiled, err := (projectbundle.ServingArtifactLoader{Objects: objects}).LoadCompiled(ctx, artifact, "")
	if err != nil {
		return "", "", nil, fmt.Errorf("load candidate approval artifact: %w", err)
	}
	if compiled.BundleDigest != state.ProjectDigest {
		return "", "", nil, errors.New("candidate approval compiled source bundle differs from its generation")
	}
	identity, err := projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID))
	if err != nil {
		return "", "", nil, fmt.Errorf("bind candidate approval identity: %w", err)
	}
	snapshot, err := projectmodule.CompileAuthorizationSnapshotJSON(identity, compiled.Graph, state.AccessPolicyJSON)
	if err != nil {
		return "", "", nil, fmt.Errorf("compile candidate approval policy: %w", err)
	}
	// Subject membership remains durable and current, exactly like the active
	// authorization path. The generation freezes policy; disabling a principal
	// or removing a group membership must revoke approval immediately.
	resolvedSubjects, err := subjects(ctx, principalID)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve candidate approval subjects: %w", err)
	}
	permissions, err := snapshot.EffectiveTypedPermissions(resolvedSubjects)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve candidate approval permissions: %w", err)
	}
	return state.ProjectID.String(), string(state.Environment), permissions, nil
}

// resolvePostgresSealedActiveState resolves the sealed runtime's authoritative
// active generation through the public deployment target port. A clean
// PostgreSQL installation has no delivery_target row until the first plan is
// admitted; that absence is the normal unbound/no-active state and must map to
// servingstate.ErrNotFound so runtimehost can remain administrable without
// fabricating an active delivery. Other authority failures stay fail-closed.
func resolvePostgresSealedActiveState(ctx context.Context, delivery deployment.DeliveryTargetResolver, targetID string) (servingstate.ID, error) {
	if delivery == nil || strings.TrimSpace(targetID) == "" {
		return "", errors.New("PostgreSQL sealed active-state target authority is unavailable")
	}
	target, err := delivery.ResolveDeliveryTarget(ctx, targetID)
	if err != nil {
		if errors.Is(err, deployment.ErrNotFound) {
			return "", servingstate.ErrNotFound
		}
		return "", err
	}
	if strings.TrimSpace(target.ActiveGenerationID) == "" {
		return "", servingstate.ErrNotFound
	}
	return servingstate.ID(target.ActiveGenerationID), nil
}

// composeNativeProjectSource wires the process-bound immutable object store
// to the PostgreSQL project authority. The caller owns transaction lifecycle
// through begin; this helper never opens a second database or derives a
// filesystem-backed project repository.
func composeNativeProjectSource(
	ctx context.Context,
	cfg config.Config,
	instanceID string,
	environment string,
	begin projectsource.BeginFunc,
	sources projectsource.NativeSourceRepository,
) (nativeProjectSourceComposition, error) {
	if begin == nil || sources == nil {
		return nativeProjectSourceComposition{}, errors.New("native project source composition requires PostgreSQL begin and project authorities")
	}
	objects, storageDomain, err := appobjectstore.New(ctx, cfg, instanceID, environment)
	if err != nil {
		return nativeProjectSourceComposition{}, fmt.Errorf("construct native project object store: %w", err)
	}
	compiler := projectsource.Compiler{}
	reader, err := projectsource.NewNativeCandidateSourceSynchronizer(projectsource.NativeCandidateSourceConfig{
		Begin:                 begin,
		Sources:               sources,
		Objects:               objects,
		Compiler:              compiler,
		StorageSecurityDomain: storageDomain,
	})
	if err != nil {
		return nativeProjectSourceComposition{}, fmt.Errorf("construct native candidate source synchronizer: %w", err)
	}
	return nativeProjectSourceComposition{
		Objects: objects, StorageSecurityDomain: storageDomain,
		Sources: sources, CandidateSourceReader: reader,
	}, nil
}

func dashboardPrewarmConfig(cfg config.Config) dashboardmodule.PrewarmConfig {
	var ids []string
	if cfg.DashboardPrewarmPublicationIDs != "" {
		ids = strings.Split(cfg.DashboardPrewarmPublicationIDs, ",")
	}
	return dashboardmodule.PrewarmConfig{PublicationIDs: ids, MaxPublications: cfg.DashboardPrewarmMaxPublications, MaxTargets: cfg.DashboardPrewarmMaxTargets, ExecutionDeadline: cfg.DashboardPrewarmDeadline, Concurrency: cfg.DashboardPrewarmConcurrency}
}
