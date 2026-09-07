package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
)

func policyLifecycleTx(ctx context.Context, tx pgx.Tx, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind) (identityledger.Identity, int64, error) {
	var identity identityledger.Identity
	var storedID, storedKind, lifecycle string
	var sequence int64
	err := tx.QueryRow(ctx, `
		SELECT ri.instance_id,ri.authored_id,ri.resource_kind,ri.lifecycle_state,
		       COALESCE(ri.active_bundle_id,''),ri.tombstone_reason,
		       ri.created_at,ri.updated_at,ri.tombstoned_at,ri.restored_at,
		       COALESCE((SELECT max(h.sequence) FROM project.resource_identity_history h
		                  WHERE h.instance_id=ri.instance_id AND h.authored_id=ri.authored_id),0)
		FROM project.resource_identity ri
		WHERE ri.instance_id=$1 AND ri.authored_id=$2
		FOR UPDATE`, instanceID, authoredID.String()).Scan(
		&identity.InstanceID, &storedID, &storedKind, &lifecycle, &identity.ActiveBundleID,
		&identity.TombstoneReason, &identity.CreatedAt, &identity.UpdatedAt,
		&identity.TombstonedAt, &identity.RestoredAt, &sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return identityledger.Identity{}, 0, fmt.Errorf("%w: resource identity is missing", identityledger.ErrPolicyEvidenceConflict)
	}
	if err != nil {
		return identityledger.Identity{}, 0, fmt.Errorf("read policy lifecycle identity: %w", err)
	}
	identity.AuthoredID = projectgraph.ResourceID(storedID)
	identity.Kind = projectgraph.Kind(storedKind)
	identity.Lifecycle = identityledger.Lifecycle(lifecycle)
	if identity.InstanceID != instanceID || identity.AuthoredID != authoredID {
		return identityledger.Identity{}, 0, fmt.Errorf("%w: lifecycle identity scope mismatch", identityledger.ErrPolicyEvidenceInvalid)
	}
	if identity.Kind != kind {
		return identityledger.Identity{}, 0, fmt.Errorf("%w: lifecycle kind=%s requested=%s", identityledger.ErrKindConflict, identity.Kind, kind)
	}
	if sequence <= 0 {
		return identityledger.Identity{}, 0, fmt.Errorf("%w: lifecycle history is missing", identityledger.ErrPolicyEvidenceConflict)
	}
	return identity, sequence, nil
}

func resolvePolicyBaselineTx(ctx context.Context, tx pgx.Tx, candidate identityledger.ContractPublication, context identityledger.PolicyContext) (*identityledger.ContractPublication, error) {
	if context.BaselineKind == identityledger.PolicyBaselineGenesis {
		return nil, nil
	}
	if context.Baseline == nil {
		return nil, fmt.Errorf("%w: existing baseline is missing", identityledger.ErrPolicyEvidenceInvalid)
	}
	baseline, err := contractPublicationTx(ctx, tx, candidate.InstanceID, candidate.AuthoredID, candidate.ResourceKind, context.Baseline.VersionBaseline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: expected baseline publication is missing", identityledger.ErrPolicyEvidenceConflict)
	}
	if err != nil {
		return nil, err
	}
	actual := identityledger.PolicyPublicationIdentity{
		InstanceID: baseline.InstanceID, AuthoredID: baseline.AuthoredID, ResourceKind: baseline.ResourceKind,
		Version: baseline.Version, VersionBaseline: baseline.VersionBaseline,
		ProjectionProfile: baseline.ProjectionProfile, Digest: baseline.Digest,
	}
	if !identityledger.EqualPolicyPublicationIdentity(actual, *context.Baseline) {
		return nil, fmt.Errorf("%w: expected baseline publication identity changed", identityledger.ErrPolicyEvidenceConflict)
	}
	return &baseline, nil
}

func derivePolicyValidation(prepared identityledger.ContractPublication, baseline *identityledger.ContractPublication, sequence int64, activeBundleID string, context identityledger.PolicyContext, registry *contractversion.SemanticRegistryTypes) (identityledger.ValidationEvidence, error) {
	var (
		evidence identityledger.PolicyEvidence
		err      error
	)
	if registry == nil {
		evidence, err = identityledger.DerivePolicyEvidence(context, baseline, prepared, sequence, activeBundleID)
	} else {
		evidence, err = identityledger.DerivePolicyEvidence(context, baseline, prepared, sequence, activeBundleID, *registry)
	}
	if err != nil {
		// Preserve the historical contract publication conflict surface for
		// version transitions while retaining the narrower policy sentinel.
		return identityledger.ValidationEvidence{}, fmt.Errorf("%w: %w: %w", identityledger.ErrContractPublicationConflict, identityledger.ErrPolicyEvidenceConflict, err)
	}
	return identityledger.ValidationEvidence{Version: 1, Checks: append([]identityledger.ValidationCheck(nil), prepared.Validation.Checks...), PolicyEvidence: &evidence}, nil
}

// registryTypesForPublication obtains only the typed definitions referenced by
// the baseline/candidate pair. Access owns the registry authority and reads it
// within the publication-owned transaction; this adapter projects the validated
// snapshot into the contractversion comparison input.
func (r *Repository) registryTypesForPublication(ctx context.Context, tx pgx.Tx, prepared identityledger.ContractPublication, baseline *identityledger.ContractPublication, policyContext identityledger.PolicyContext) (*contractversion.SemanticRegistryTypes, error) {
	names, protected, err := referencedSemanticAttributes(baseline, prepared)
	if err != nil {
		return nil, fmt.Errorf("%w: referenced semantic attributes: %v", identityledger.ErrPolicyEvidenceConflict, err)
	}
	if !protected {
		return nil, nil
	}
	if r == nil || r.semanticRegistryReader == nil {
		return nil, fmt.Errorf("%w: Access semantic registry reader is required for protected SemanticModel publication", identityledger.ErrPolicyEvidenceConflict)
	}
	if policyContext.ExpectedRegistry == nil {
		return nil, fmt.Errorf("%w: expected Access registry reference is required for protected SemanticModel publication", identityledger.ErrPolicyEvidenceInvalid)
	}
	value, err := r.semanticRegistryReader(ctx, tx, prepared.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("%w: read Access semantic registry: %v", identityledger.ErrPolicyEvidenceConflict, err)
	}
	projectID := projectgraph.ResourceID(value.Control.ProjectID)
	if err := value.Validate(prepared.InstanceID, projectID); err != nil {
		return nil, fmt.Errorf("%w: Access semantic registry context: %v", identityledger.ErrPolicyEvidenceConflict, err)
	}
	expected := policyContext.ExpectedRegistry
	if expected.InstanceID != value.Control.InstanceID || expected.ProjectID.String() != value.Control.ProjectID ||
		expected.ControlRevision != value.Control.Revision || expected.Profile != value.Registry.State.Profile ||
		expected.Revision != value.Registry.State.Revision || expected.Digest != value.Registry.State.Digest {
		return nil, fmt.Errorf("%w: expected Access registry reference changed", identityledger.ErrPolicyEvidenceConflict)
	}
	types := contractversion.SemanticRegistryTypes{
		InstanceID: prepared.InstanceID, ProjectID: value.Control.ProjectID,
		ControlRevision: value.Control.Revision, Profile: value.Registry.State.Profile,
		Revision: value.Registry.State.Revision, Digest: value.Registry.State.Digest,
		Definitions: make([]contractversion.RegisteredSemanticType, 0, len(names)),
	}
	definitions := make(map[string]access.SemanticAttributeDefinition, len(value.Registry.Definitions))
	for _, definition := range value.Registry.Definitions {
		if _, duplicate := definitions[definition.Name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Access semantic attribute %q", identityledger.ErrPolicyEvidenceConflict, definition.Name)
		}
		definitions[definition.Name] = definition
	}
	for _, name := range names {
		definition, found := definitions[name]
		if !found {
			return nil, fmt.Errorf("%w: Access semantic attribute %q is not registered", identityledger.ErrPolicyEvidenceConflict, name)
		}
		types.Definitions = append(types.Definitions, contractversion.RegisteredSemanticType{
			ID: definition.ID, Name: definition.Name, Type: definition.Type,
			Shape: string(definition.Shape), Version: definition.DefinitionVersion,
			Enabled: definition.Enabled,
		})
	}
	if err := types.Validate(); err != nil {
		return nil, fmt.Errorf("%w: Access typed registry projection: %v", identityledger.ErrPolicyEvidenceConflict, err)
	}
	return &types, nil
}

func referencedSemanticAttributes(baseline *identityledger.ContractPublication, candidate identityledger.ContractPublication) ([]string, bool, error) {
	if candidate.ResourceKind != projectgraph.KindSemanticModel {
		return nil, false, nil
	}
	seen := map[string]struct{}{}
	add := func(publication identityledger.ContractPublication) error {
		names, err := contractversion.ReferencedSemanticAttributes(publication.CanonicalBytes)
		if err != nil {
			return err
		}
		for _, name := range names {
			seen[name] = struct{}{}
		}
		return nil
	}
	if baseline != nil {
		if err := add(*baseline); err != nil {
			return nil, false, err
		}
	}
	if err := add(candidate); err != nil {
		return nil, false, err
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, len(names) > 0, nil
}
