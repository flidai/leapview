package module

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

type authorizationPolicyRevisionReader interface {
	AuthorizationPolicyRevision(context.Context, access.AuthorizationPolicyScope, int64) (access.AuthorizationPolicy, error)
}

type resolvedTargetAuthorizationPolicy struct {
	revision  int64
	digest    string
	canonical string
	manifest  projectmanifest.AccessPolicy
	snapshot  accesssnapshot.AuthorizationSnapshot
}

// resolveTargetAuthorizationPolicy converts one target-owned policy revision
// into the immutable manifest/snapshot representation consumed by candidate
// planning. A positive revision selects historical state for recovery; zero
// selects the current head for ordinary planning and build revalidation.
func (service *nativeCandidateArtifactPhases) resolveTargetAuthorizationPolicy(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	environment string,
	revision int64,
	digest string,
	graph projectgraph.ProjectGraph,
	identity projectgraph.ServingIdentity,
) (resolvedTargetAuthorizationPolicy, error) {
	if service == nil {
		return resolvedTargetAuthorizationPolicy{}, errors.New("target authorization policy service is unavailable")
	}
	if service.authorizationPolicies == nil {
		// Direct package tests and historical profile providers do not constitute
		// the native PostgreSQL composition. Build rejects this configuration;
		// retain the old empty snapshot only at this internal compatibility seam.
		policy := projectmanifest.AccessPolicy{}
		snapshot, err := projectmanifest.CompileAuthorizationSnapshot(identity, graph, policy)
		if err != nil {
			return resolvedTargetAuthorizationPolicy{}, err
		}
		fingerprint, err := snapshot.Digest()
		if err != nil {
			return resolvedTargetAuthorizationPolicy{}, err
		}
		return resolvedTargetAuthorizationPolicy{digest: fingerprint, canonical: "{}", manifest: policy, snapshot: snapshot}, nil
	}
	scope := access.AuthorizationPolicyScope{TargetID: service.targetID, ProjectID: projectID.String(), Environment: environment}
	if err := access.ValidateAuthorizationPolicyScope(scope); err != nil {
		return resolvedTargetAuthorizationPolicy{}, err
	}
	var (
		policy access.AuthorizationPolicy
		err    error
	)
	if revision > 0 {
		reader, ok := service.authorizationPolicies.(authorizationPolicyRevisionReader)
		if !ok {
			return resolvedTargetAuthorizationPolicy{}, errors.New("historical target authorization policy authority is unavailable")
		}
		policy, err = reader.AuthorizationPolicyRevision(ctx, scope, revision)
	} else {
		policy, err = service.authorizationPolicies.AuthorizationPolicy(ctx, scope)
	}
	if err != nil {
		return resolvedTargetAuthorizationPolicy{}, err
	}
	computed, err := access.AuthorizationPolicyDigest(scope, policy.RoleBindings)
	if err != nil {
		return resolvedTargetAuthorizationPolicy{}, err
	}
	if policy.Scope != scope || policy.Revision <= 0 || policy.Digest != computed ||
		(revision > 0 && policy.Revision != revision) || (digest != "" && policy.Digest != digest) {
		return resolvedTargetAuthorizationPolicy{}, fmt.Errorf("target authorization policy identity does not match requested revision")
	}
	manifestPolicy, err := targetAuthorizationManifestPolicy(policy)
	if err != nil {
		return resolvedTargetAuthorizationPolicy{}, err
	}
	canonical, err := canonicalNativeServingDocument(manifestPolicy, "access policy")
	if err != nil {
		return resolvedTargetAuthorizationPolicy{}, err
	}
	snapshot, err := projectmanifest.CompileAuthorizationSnapshot(identity, graph, manifestPolicy)
	if err != nil {
		return resolvedTargetAuthorizationPolicy{}, err
	}
	return resolvedTargetAuthorizationPolicy{revision: policy.Revision, digest: policy.Digest, canonical: canonical, manifest: manifestPolicy, snapshot: snapshot}, nil
}

func targetAuthorizationManifestPolicy(policy access.AuthorizationPolicy) (projectmanifest.AccessPolicy, error) {
	result := projectmanifest.AccessPolicy{RoleBindings: make(map[string]projectmanifest.RoleBinding, len(policy.RoleBindings))}
	for _, binding := range policy.RoleBindings {
		if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
			return projectmanifest.AccessPolicy{}, err
		}
		subject := projectmanifest.Subject{Kind: string(binding.Subject.Kind)}
		switch binding.Subject.Kind {
		case access.SubjectKindPrincipal:
			subject.PrincipalID = binding.Subject.ID
		case access.SubjectKindGroup:
			subject.Group = binding.Subject.ID
		default:
			return projectmanifest.AccessPolicy{}, fmt.Errorf("unsupported target authorization subject kind %q", binding.Subject.Kind)
		}
		if _, duplicate := result.RoleBindings[binding.ID]; duplicate {
			return projectmanifest.AccessPolicy{}, fmt.Errorf("duplicate target authorization role binding %q", binding.ID)
		}
		result.RoleBindings[binding.ID] = projectmanifest.RoleBinding{
			ID: binding.ID, Name: binding.Name, Role: string(binding.Role), Subject: subject,
		}
	}
	return result, nil
}
