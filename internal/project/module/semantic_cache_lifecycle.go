package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identitymodule "github.com/flidai/leapview/internal/project/identityledger/module"
)

type semanticCacheControlReader interface {
	ReadAuthorizationControlRevision(context.Context, string) (access.AuthorizationControlRevision, error)
}

// SemanticCacheLifecycleReader adapts the existing ledger and Access readers
// for an activation-bound cache consumer. It neither evaluates authorization
// nor chooses an activation binding: the caller must retain the revision that
// produced its leased authorization snapshot, not bless a fresh revision here.
// Production semantic activation remains independently capability-gated.
func SemanticCacheLifecycleReader(ledger identitymodule.LifecycleEvidenceReader, control semanticCacheControlReader, binding resultidentity.SemanticLifecycle, authorization access.AuthorizationControlRevision) (func(context.Context) (resultidentity.SemanticLifecycle, error), error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if binding.ProjectionProfile != contractprojection.Profile {
		return nil, fmt.Errorf("semantic cache projection profile is unsupported")
	}
	if _, err := contractversion.SemverBaseline(binding.PublicationVersion); err != nil {
		return nil, err
	}
	if err := authorization.Validate(); err != nil {
		return nil, err
	}
	if ledger == nil || control == nil {
		return nil, fmt.Errorf("semantic cache lifecycle authorities are unavailable")
	}
	if authorization.InstanceID != binding.InstanceID || authorization.ProjectID != binding.ProjectID.String() || authorization.Revision != binding.AuthorizationRevision {
		return nil, fmt.Errorf("semantic cache binding differs from activation authorization revision")
	}
	return func(ctx context.Context) (resultidentity.SemanticLifecycle, error) {
		before, err := control.ReadAuthorizationControlRevision(ctx, binding.InstanceID)
		if err != nil {
			return resultidentity.SemanticLifecycle{}, err
		}
		if before.InstanceID != binding.InstanceID || before.ProjectID != authorization.ProjectID || before.Revision <= 0 {
			return resultidentity.SemanticLifecycle{}, fmt.Errorf("semantic cache authorization control scope is invalid")
		}
		if before.Revision != authorization.Revision {
			return resultidentity.SemanticLifecycle{}, fmt.Errorf("semantic cache authorization revision differs from activation")
		}
		evidence, err := ledger.ReadLifecycleEvidence(ctx, binding.InstanceID, binding.AuthoredID, binding.ResourceKind, binding.PublicationVersion)
		if err != nil {
			return resultidentity.SemanticLifecycle{}, err
		}
		identity, publication := evidence.Identity, evidence.Publication
		if identity.InstanceID != binding.InstanceID || identity.AuthoredID != binding.AuthoredID || identity.Kind != binding.ResourceKind || identity.Lifecycle != identitymodule.LifecycleActive {
			return resultidentity.SemanticLifecycle{}, fmt.Errorf("semantic cache resource is unavailable")
		}
		if publication.InstanceID != identity.InstanceID || publication.AuthoredID != identity.AuthoredID || publication.ResourceKind != identity.Kind {
			return resultidentity.SemanticLifecycle{}, fmt.Errorf("semantic cache publication scope is invalid")
		}
		// Authorities retain separate ownership. Re-read the monotonic control
		// revision to detect a role/grant mutation crossing the ledger read.
		after, err := control.ReadAuthorizationControlRevision(ctx, binding.InstanceID)
		if err != nil {
			return resultidentity.SemanticLifecycle{}, err
		}
		if before != after {
			return resultidentity.SemanticLifecycle{}, fmt.Errorf("semantic cache authorization changed during lifecycle read")
		}
		current := resultidentity.SemanticLifecycle{
			ProjectID:  projectgraph.ResourceID(after.ProjectID),
			InstanceID: identity.InstanceID, AuthoredID: identity.AuthoredID, ResourceKind: identity.Kind,
			Sequence: evidence.Sequence, ActiveBundleID: identity.ActiveBundleID,
			PublicationVersion: publication.Version, ProjectionProfile: publication.ProjectionProfile, PublicationDigest: publication.Digest,
			AuthorizationRevision: after.Revision,
		}
		if err := current.Validate(); err != nil {
			return resultidentity.SemanticLifecycle{}, err
		}
		if current != binding {
			return resultidentity.SemanticLifecycle{}, fmt.Errorf("semantic cache lifecycle differs from activation")
		}
		return current, nil
	}, nil
}
