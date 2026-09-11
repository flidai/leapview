package materialize

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// Test the complete protected runtime path with lifecycle identities that are
// valid snapshots, rather than mutating a revision field in isolation. The
// fixture supplies the real FAI-639 policy and FAI-641 planner; the runtime
// then derives and consumes the protected dependency through the existing
// result cache.
func TestProtectedCacheLifecycleRotationChangesDependencyAndMisses(t *testing.T) {
	runtime, _, _ := protectedConsumerFixtureWithModelModifier(t, true, func(model *semanticmodel.Model) {
		// Authored SQL has no portable relation revision in this cache slice. A
		// materialized relation is the eligible protected path under test.
		model.Tables["orders"] = clearExecutionSQL(model.Tables["orders"])
	})
	installSemanticCacheLifecycleEvidence(t, runtime)
	request := semanticCacheLifecycleRequest()
	binding := semanticCacheLifecycleBinding()

	initial := semanticCacheLifecycleStateFor(t, 1, 1, true, 1, semanticCacheLifecycleAssignment(t, "assignment-account", 1, 1, false))
	currentInitial := initial
	oldConsumer := newSemanticCacheLifecycleConsumer(t, runtime.planner, initial, &currentInitial)
	oldGovernor := semanticConsumerTestGovernor{consumer: oldConsumer, binding: binding}

	first, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), oldGovernor), request)
	if err != nil {
		t.Fatalf("initial protected query: %v", err)
	}
	second, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), oldGovernor), request)
	if err != nil {
		t.Fatalf("initial protected cache hit: %v", err)
	}
	if first.CacheOutcome != dataquery.CacheMiss || second.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("initial cache outcomes = (%q, %q), want miss then hit", first.CacheOutcome, second.CacheOutcome)
	}
	oldDependency := semanticCacheLifecycleDependency(t, runtime, oldConsumer, binding, request)
	oldPlan, err := oldConsumer.Planner().PlanRows(semanticquery.RowRequest{Dataset: "orders", Dimensions: []semanticquery.Field{{Field: "orders.id", Alias: "id"}}})
	if err != nil {
		t.Fatalf("initial admitted plan: %v", err)
	}

	// The repository treats a same-value write as an idempotent replay. Model a
	// legitimate out-and-back history instead: value 1/version 1/control 1 ->
	// value 2/version 2/control 2 -> value 1/version 3/control 3. The
	// intermediate snapshot need not be cached, but composing it verifies that
	// the final snapshot is the result of real lifecycle transitions.
	intermediate := semanticCacheLifecycleStateFor(t, 1, 2, true, 1, semanticCacheLifecycleAssignmentForValue(t, "assignment-account", 1, 2, 2, false))
	currentIntermediate := intermediate
	if _, err := newSemanticCacheLifecycleConsumerChecked(t, runtime.planner, intermediate, &currentIntermediate); err != nil {
		t.Fatalf("compose intermediate value update: %v", err)
	}
	roundTrip := semanticCacheLifecycleStateFor(t, 1, 3, true, 1, semanticCacheLifecycleAssignmentForValue(t, "assignment-account", 1, 3, 1, false))
	currentInitial = roundTrip
	if _, err := oldConsumer.CacheIdentity(); err == nil {
		t.Fatal("old consumer accepted a valid control/assignment rotation")
	}
	if err := oldConsumer.ValidatePlan(oldPlan); err == nil {
		t.Fatal("old admitted plan survived a valid control/assignment rotation")
	}
	if _, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), oldGovernor), request); err == nil {
		t.Fatal("old consumer executed after a valid control/assignment rotation")
	}

	currentRoundTrip := roundTrip
	roundTripConsumer := newSemanticCacheLifecycleConsumer(t, runtime.planner, roundTrip, &currentRoundTrip)
	roundTripDependency := semanticCacheLifecycleDependency(t, runtime, roundTripConsumer, binding, request)
	assertLifecycleDependencyRotated(t, oldDependency, roundTripDependency, true)
	assertLifecycleControlIdentityChanged(t, oldDependency, roundTripDependency)
	roundTripResult, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), semanticConsumerTestGovernor{consumer: roundTripConsumer, binding: binding}), request)
	if err != nil {
		t.Fatalf("fresh consumer after valid out-and-back assignment history: %v", err)
	}
	if roundTripResult.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("fresh consumer after valid out-and-back assignment history = %q, want miss after previous hit", roundTripResult.CacheOutcome)
	}

	// Tombstoning without a replacement removes the effective attribute. A
	// fresh protected consumer may be composed, but its governed plan must be
	// denied rather than resurrecting the previously cached allowed result.
	tombstoned := semanticCacheLifecycleAssignmentForValue(t, "assignment-account", 1, 4, 1, true)
	tombstoneOnly := semanticCacheLifecycleStateFor(t, 1, 4, true, 1, tombstoned)
	currentRoundTrip = tombstoneOnly
	if _, err := roundTripConsumer.CacheIdentity(); err == nil {
		t.Fatal("consumer accepted a tombstone transition")
	}
	currentTombstoneOnly := tombstoneOnly
	tombstoneOnlyConsumer := newSemanticCacheLifecycleConsumer(t, runtime.planner, tombstoneOnly, &currentTombstoneOnly)
	if _, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), semanticConsumerTestGovernor{consumer: tombstoneOnlyConsumer, binding: binding}), request); err == nil {
		t.Fatal("tombstone-only consumer reused a previously allowed cached result")
	}

	// Tombstone the old incarnation and create a new assignment with the same
	// effective value. Both the assignment identity and control digest change.
	newAssignment := semanticCacheLifecycleAssignmentForValue(t, "assignment-account-new", 1, 1, 1, false)
	tombstoneState := semanticCacheLifecycleStateFor(t, 1, 5, true, 1, tombstoned, newAssignment)
	currentReplacement := tombstoneState
	replacementConsumer := newSemanticCacheLifecycleConsumer(t, runtime.planner, tombstoneState, &currentReplacement)
	replacementDependency := semanticCacheLifecycleDependency(t, runtime, replacementConsumer, binding, request)
	assertLifecycleDependencyRotated(t, roundTripDependency, replacementDependency, true)
	assertLifecycleControlIdentityChanged(t, roundTripDependency, replacementDependency)
	replacementResult, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), semanticConsumerTestGovernor{consumer: replacementConsumer, binding: binding}), request)
	if err != nil {
		t.Fatalf("fresh consumer after tombstone/new assignment: %v", err)
	}
	if replacementResult.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("fresh consumer after tombstone/new assignment = %q, want miss", replacementResult.CacheOutcome)
	}

	// Disabling a definition while its durable assignment remains active is a
	// valid control-plane lifecycle transition, but the resulting authority
	// snapshot is unusable for execution. The fresh consumer must fail closed;
	// it must not reuse the prior allowed dependency.
	disabled := semanticCacheLifecycleStateFor(t, 2, 5, false, 2, tombstoned, newAssignment)
	currentReplacement = disabled
	if _, err := replacementConsumer.CacheIdentity(); err == nil {
		t.Fatal("consumer accepted a disabled-definition authority snapshot")
	}
	if _, err := newSemanticCacheLifecycleConsumerChecked(t, runtime.planner, disabled, &disabled); err == nil {
		t.Fatal("fresh consumer composed from a disabled-definition snapshot")
	}

	// Re-enabling the definition is a new registry identity. The active
	// assignment remains the new incarnation and the real planner/cache path
	// must miss the old tombstone-era entry.
	reenabled := semanticCacheLifecycleStateFor(t, 3, 5, true, 3, tombstoned, newAssignment)
	currentReenabled := reenabled
	reenabledConsumer := newSemanticCacheLifecycleConsumer(t, runtime.planner, reenabled, &currentReenabled)
	reenabledDependency := semanticCacheLifecycleDependency(t, runtime, reenabledConsumer, binding, request)
	assertLifecycleDependencyRotated(t, replacementDependency, reenabledDependency, false)
	reenabledResult, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), semanticConsumerTestGovernor{consumer: reenabledConsumer, binding: binding}), request)
	if err != nil {
		t.Fatalf("fresh consumer after disable/re-enable: %v", err)
	}
	if reenabledResult.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("fresh consumer after disable/re-enable = %q, want miss", reenabledResult.CacheOutcome)
	}
}

// Test the protected cache path with the FAI-637 trusted-claim mapping
// lifecycle. A replay returns the same durable mapping identity and therefore
// keeps the cache address; a tombstone invalidates the old consumer, while a
// new mapping incarnation with the same effective value gets a fresh address.
func TestProtectedCacheTrustedClaimMappingLifecycleRotatesIdentity(t *testing.T) {
	runtime, _, _ := protectedConsumerFixtureWithModelModifier(t, true, func(model *semanticmodel.Model) {
		// Authored SQL has no portable relation revision in this cache slice. A
		// materialized relation is the eligible protected path under test.
		model.Tables["orders"] = clearExecutionSQL(model.Tables["orders"])
	})
	installSemanticCacheLifecycleEvidence(t, runtime)
	request := semanticCacheLifecycleRequest()
	binding := semanticCacheLifecycleBinding()

	createdMapping := semanticCacheLifecycleMapping(t, "mapping-account", 1, false)
	created := semanticCacheLifecycleTrustedClaimStateFor(t, 1, 1, true, 1, createdMapping)
	current := created
	consumer := newSemanticCacheLifecycleConsumer(t, runtime.planner, created, &current)
	governor := semanticConsumerTestGovernor{consumer: consumer, binding: binding}

	createdResult, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), governor), request)
	if err != nil {
		t.Fatalf("trusted-claim mapping create query: %v", err)
	}
	if createdResult.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("trusted-claim mapping create cache outcome = %q, want miss", createdResult.CacheOutcome)
	}
	createdDependency := semanticCacheLifecycleDependency(t, runtime, consumer, binding, request)

	// FAI-637 treats an active same-identity write as an idempotent replay. It
	// returns the current row and control snapshot, so the protected identity
	// and cache address remain unchanged.
	replayed := created
	current = replayed
	replayDependency := semanticCacheLifecycleDependency(t, runtime, consumer, binding, request)
	if createdDependency.Digest() != replayDependency.Digest() {
		t.Fatal("trusted-claim mapping replay rotated a protected cache identity")
	}
	replayResult, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), governor), request)
	if err != nil {
		t.Fatalf("trusted-claim mapping replay query: %v", err)
	}
	if replayResult.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("trusted-claim mapping replay cache outcome = %q, want hit", replayResult.CacheOutcome)
	}

	tombstoned := semanticCacheLifecycleMapping(t, createdMapping.ID, createdMapping.MappingVersion+1, true)
	tombstoneState := semanticCacheLifecycleTrustedClaimStateFor(t, 1, 2, true, 1, tombstoned)
	current = tombstoneState
	if _, err := consumer.CacheIdentity(); err == nil {
		t.Fatal("consumer accepted a trusted-claim mapping tombstone")
	}
	if _, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), governor), request); err == nil {
		t.Fatal("old consumer executed after trusted-claim mapping tombstone")
	}

	currentTombstone := tombstoneState
	tombstoneConsumer := newSemanticCacheLifecycleConsumer(t, runtime.planner, tombstoneState, &currentTombstone)
	if _, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), semanticConsumerTestGovernor{consumer: tombstoneConsumer, binding: binding}), request); err == nil {
		t.Fatal("tombstoned trusted-claim mapping reused a previously allowed cache result")
	}

	// Remapping requires a tombstone followed by a new mapping. Keep the
	// source, claim, definition, and effective value the same, but use a new
	// durable mapping ID/incarnation. The FAI-637 control and claim-evidence
	// identities must rotate, forcing a protected cache miss.
	newMapping := semanticCacheLifecycleMapping(t, "mapping-account-new", 1, false)
	replacement := semanticCacheLifecycleTrustedClaimStateFor(t, 1, 3, true, 1, tombstoned, newMapping)
	currentReplacement := replacement
	replacementConsumer := newSemanticCacheLifecycleConsumer(t, runtime.planner, replacement, &currentReplacement)
	replacementDependency := semanticCacheLifecycleDependency(t, runtime, replacementConsumer, binding, request)
	assertLifecycleDependencyRotated(t, createdDependency, replacementDependency, true)
	assertLifecycleControlIdentityChanged(t, createdDependency, replacementDependency)
	createdAccess, replacementAccess := createdDependency.SemanticAccess(), replacementDependency.SemanticAccess()
	if createdAccess.TrustedClaimEvidenceDigest == "" || replacementAccess.TrustedClaimEvidenceDigest == "" ||
		createdAccess.TrustedClaimEvidenceDigest == replacementAccess.TrustedClaimEvidenceDigest {
		t.Fatalf("trusted-claim evidence identity did not rotate: created=%q replacement=%q", createdAccess.TrustedClaimEvidenceDigest, replacementAccess.TrustedClaimEvidenceDigest)
	}
	replacementResult, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), semanticConsumerTestGovernor{consumer: replacementConsumer, binding: binding}), request)
	if err != nil {
		t.Fatalf("trusted-claim mapping replacement query: %v", err)
	}
	if replacementResult.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("trusted-claim mapping replacement cache outcome = %q, want miss", replacementResult.CacheOutcome)
	}
}

type semanticCacheLifecycleState struct {
	registry             access.SemanticAttributeRegistrySnapshot
	control              access.SemanticAttributeControlSnapshot
	values               []access.EffectiveSemanticAttribute
	trustedClaimEvidence access.SemanticAttributeClaimEvidence
}

type semanticCacheLifecycleClaimsVerifier struct {
	claims trustedclaims.VerifiedClaims
}

func (verifier semanticCacheLifecycleClaimsVerifier) SourceKind() trustedclaims.SourceKind {
	return trustedclaims.SourceOIDC
}

func (verifier semanticCacheLifecycleClaimsVerifier) Verify(context.Context, []byte) (trustedclaims.VerifiedClaims, error) {
	return verifier.claims, nil
}

func semanticCacheLifecycleMapping(t *testing.T, id string, version int64, tombstoned bool) access.TrustedClaimMapping {
	t.Helper()
	mapping := access.TrustedClaimMapping{
		ID: id, SourceKind: access.TrustedClaimSourceOIDC, Provider: "identity-provider", Issuer: "https://issuer.example", Audience: "leapview", Claim: "accountIds",
		DefinitionID: "def-account", DefinitionName: "accountIds", DefinitionVersion: 1, Type: semanticvalue.TypeInteger, Shape: access.SemanticAttributeList,
		MappingVersion: version, Tombstoned: tombstoned,
	}
	if tombstoned {
		mapping.TombstonedAt = "2026-01-01T00:00:00Z"
	}
	return mapping
}

func semanticCacheLifecycleTrustedClaimStateFor(t *testing.T, registryRevision, controlRevision int64, enabled bool, definitionVersion int64, mappings ...access.TrustedClaimMapping) semanticCacheLifecycleState {
	t.Helper()
	state := semanticCacheLifecycleStateFor(t, registryRevision, controlRevision, enabled, definitionVersion)
	state.control.Mappings = append([]access.TrustedClaimMapping(nil), mappings...)
	var err error
	state.control.State.Digest, err = access.SemanticAttributeControlDigest(state.control.Assignments, state.control.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		return state
	}
	definition := access.SemanticAttributeDefinition{
		ID: "def-account", Name: "accountIds", Type: semanticvalue.TypeInteger, Shape: access.SemanticAttributeList,
		Profile: semanticvalue.Profile, DefinitionVersion: definitionVersion, LifecycleState: access.SemanticAttributeActive, Enabled: true,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
	}
	for index := len(mappings) - 1; index >= 0; index-- {
		mapping := mappings[index]
		if mapping.Tombstoned {
			continue
		}
		values, valueDigest, valueErr := access.CanonicalSemanticAttributeValues(definition, []int64{1})
		if valueErr != nil {
			t.Fatal(valueErr)
		}
		state.values = []access.EffectiveSemanticAttribute{{
			DefinitionID: mapping.DefinitionID, DefinitionName: mapping.DefinitionName, DefinitionVersion: definitionVersion,
			Type: mapping.Type, Shape: mapping.Shape, CanonicalValues: values, ValueDigest: valueDigest, Source: "trusted_claim",
		}}
		now := semanticCacheLifecycleClaimEvaluatedAt
		claims := trustedclaims.VerifiedClaims{
			Provider: mapping.Provider, Issuer: mapping.Issuer, Audience: mapping.Audience, Subject: "alice",
			IssuedAt: now.Add(-time.Minute), ExpiresAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), TokenFingerprint: strings.Repeat("a", 64),
			Claims: []trustedclaims.Claim{{Name: mapping.Claim, Value: []int64{1}}},
		}
		envelope, verifyErr := trustedclaims.Verify(t.Context(), trustedclaims.NewRawEvidence(trustedclaims.SourceOIDC, []byte("signed-evidence")), semanticCacheLifecycleClaimsVerifier{claims: claims}, trustedclaims.VerifyOptions{Now: now})
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		state.trustedClaimEvidence, err = access.NewSemanticAttributeClaimEvidence("instance-1", "alice", state.control, state.values, envelope, now)
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	return state
}

var semanticCacheLifecycleClaimEvaluatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func semanticCacheLifecycleAssignment(t *testing.T, id string, definitionVersion, assignmentVersion int64, tombstoned bool) access.SemanticAttributeAssignment {
	return semanticCacheLifecycleAssignmentForValue(t, id, definitionVersion, assignmentVersion, 1, tombstoned)
}

func semanticCacheLifecycleAssignmentForValue(t *testing.T, id string, definitionVersion, assignmentVersion, value int64, tombstoned bool) access.SemanticAttributeAssignment {
	t.Helper()
	definition := access.SemanticAttributeDefinition{
		ID: "def-account", Name: "accountIds", Type: semanticvalue.TypeInteger, Shape: access.SemanticAttributeList,
		Profile: semanticvalue.Profile, DefinitionVersion: definitionVersion, LifecycleState: access.SemanticAttributeActive, Enabled: true,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
	}
	values, valueDigest, err := access.CanonicalSemanticAttributeValues(definition, []int64{value})
	if err != nil {
		t.Fatal(err)
	}
	assignment := access.SemanticAttributeAssignment{
		ID: id, DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definitionVersion,
		Type: definition.Type, Shape: definition.Shape, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"},
		CanonicalValues: values, ValueDigest: valueDigest, AssignmentVersion: assignmentVersion,
		Tombstoned: tombstoned,
	}
	if tombstoned {
		assignment.TombstonedAt = "2026-01-01T00:00:00Z"
	}
	return assignment
}

func semanticCacheLifecycleStateFor(t *testing.T, registryRevision, controlRevision int64, enabled bool, definitionVersion int64, assignments ...access.SemanticAttributeAssignment) semanticCacheLifecycleState {
	t.Helper()
	definition := access.SemanticAttributeDefinition{
		ID: "def-account", Name: "accountIds", Type: semanticvalue.TypeInteger, Shape: access.SemanticAttributeList,
		Profile: semanticvalue.Profile, DefinitionVersion: definitionVersion, Enabled: enabled,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
	}
	if enabled {
		definition.LifecycleState = access.SemanticAttributeActive
	} else {
		definition.LifecycleState = access.SemanticAttributeDisabled
		definition.DisabledAt = "2026-01-01T00:00:00Z"
	}
	registryDigest, err := access.SemanticAttributeRegistryDigest(semanticvalue.Profile, []access.SemanticAttributeDefinition{definition})
	if err != nil {
		t.Fatal(err)
	}
	registry := access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: registryRevision, Digest: registryDigest}, Definitions: []access.SemanticAttributeDefinition{definition}}
	controlDigest, err := access.SemanticAttributeControlDigest(assignments, nil)
	if err != nil {
		t.Fatal(err)
	}
	control := access.SemanticAttributeControlSnapshot{State: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: controlRevision, Digest: controlDigest}, Assignments: assignments}
	state := semanticCacheLifecycleState{registry: registry, control: control}
	if enabled {
		for index := len(assignments) - 1; index >= 0; index-- {
			assignment := assignments[index]
			if assignment.Tombstoned {
				continue
			}
			state.values = []access.EffectiveSemanticAttribute{{
				DefinitionID: assignment.DefinitionID, DefinitionName: assignment.DefinitionName, DefinitionVersion: definitionVersion,
				Type: assignment.Type, Shape: assignment.Shape, CanonicalValues: append([]string(nil), assignment.CanonicalValues...), ValueDigest: assignment.ValueDigest, Source: "direct",
			}}
			break
		}
	}
	return state
}

func newSemanticCacheLifecycleConsumer(t *testing.T, planner *semanticquery.Planner, pinned semanticCacheLifecycleState, current *semanticCacheLifecycleState) *semanticquery.SemanticAccessConsumer {
	t.Helper()
	consumer, err := newSemanticCacheLifecycleConsumerChecked(t, planner, pinned, current)
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

func newSemanticCacheLifecycleConsumerChecked(t *testing.T, planner *semanticquery.Planner, pinned semanticCacheLifecycleState, current *semanticCacheLifecycleState) (*semanticquery.SemanticAccessConsumer, error) {
	t.Helper()
	pinnedSnapshot := semanticCacheLifecycleSnapshot(t, pinned)
	return semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "sales", Generation: "generation-1", PrincipalID: "alice",
		PublicationPolicy: semanticCacheTestPublicationPolicy("instance-1", "sales"),
		Authority: func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
			state := *current
			return pinnedSnapshot, semanticquery.SemanticAccessAuthority{InstanceID: "instance-1", Registry: state.registry, Control: state.control, ObservedAt: time.Now().UTC()}, nil
		},
	})
}

func semanticCacheLifecycleSnapshot(t *testing.T, state semanticCacheLifecycleState) semanticquery.SemanticAccessAttributeSnapshot {
	t.Helper()
	attributeDigest, err := semanticquery.EffectiveSemanticAttributeDigest(state.values)
	if err != nil {
		t.Fatal(err)
	}
	var directEvidence access.SemanticAttributeDirectEvidence
	hasDirect := false
	for _, attribute := range state.values {
		if attribute.Source == "direct" || attribute.Source == "direct+trusted_claim" {
			hasDirect = true
			break
		}
	}
	if hasDirect {
		directEvidence, err = access.NewSemanticAttributeDirectEvidence("instance-1", "alice", []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "alice"}}, state.control, state.values)
		if err != nil {
			t.Fatal(err)
		}
	}
	return semanticquery.SemanticAccessAttributeSnapshot{
		InstanceID: "instance-1", PrincipalID: "alice", ActorID: "alice", Registry: state.registry, Control: state.control,
		EffectiveAttributes: state.values, EffectiveAttributeDigest: attributeDigest, DirectAssignmentEvidence: directEvidence,
		TrustedClaimEvidence: state.trustedClaimEvidence,
	}
}

func installSemanticCacheLifecycleEvidence(t *testing.T, runtime *Runtime) {
	t.Helper()
	modelDigest, err := semanticquery.SemanticModelDigest(runtime.model)
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := projectgraph.NewResourceID(runtime.modelID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.dependencyEvidence, err = resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: modelID, SemanticModelDigest: modelDigest,
		DatasetRelations:   []resultidentity.DatasetRelation{{Dataset: "orders", Relation: resultidentity.RelationRevision{RelationID: "model:orders", RevisionDigest: materializeTestDigest('b')}}},
		BindingFingerprint: materializeTestDigest('c'), RuntimeDigest: materializeTestDigest('d'), CapabilityDigest: materializeTestDigest('e'),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func semanticCacheLifecycleRequest() dataquery.Query {
	return dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", PrincipalID: "alice", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
		EffectivePolicyFingerprint: materializeTestDigest('f'),
	}
}

func semanticCacheLifecycleBinding() semanticquery.SemanticAccessConsumerBinding {
	return semanticquery.SemanticAccessConsumerBinding{InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", Generation: "generation-1", ModelID: "sales", PrincipalID: "alice"}
}

func semanticCacheLifecycleDependency(t *testing.T, runtime *Runtime, consumer *semanticquery.SemanticAccessConsumer, binding semanticquery.SemanticAccessConsumerBinding, request dataquery.Query) resultidentity.Dependency {
	t.Helper()
	ctx := semanticquery.WithSemanticAccessConsumer(context.Background(), consumer, binding)
	planned, err := runtime.planOwnedArrowQueryContext(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := runtime.semanticCacheIdentity(ctx, request, consumer)
	if err != nil {
		t.Fatal(err)
	}
	resultIdentity, err := planned.plan.ResultIdentity()
	if err != nil {
		t.Fatal(err)
	}
	dependency, ok := runtime.dependencyForProtectedProjection(resultIdentity.Dependencies, identity)
	if !ok {
		t.Fatal("protected dependency was not reusable")
	}
	return dependency
}

func assertLifecycleDependencyRotated(t *testing.T, previous, current resultidentity.Dependency, effectiveValuesUnchanged bool) {
	t.Helper()
	if previous.Digest() == current.Digest() {
		t.Fatal("protected dependency digest did not change after lifecycle rotation")
	}
	previousAccess, currentAccess := previous.SemanticAccess(), current.SemanticAccess()
	if previousAccess == nil || currentAccess == nil {
		t.Fatal("lifecycle dependency lost protected semantic identity")
	}
	if effectiveValuesUnchanged && previousAccess.EffectiveAttributeDigest != currentAccess.EffectiveAttributeDigest {
		t.Fatalf("effective attribute digest changed unexpectedly: %q != %q", previousAccess.EffectiveAttributeDigest, currentAccess.EffectiveAttributeDigest)
	}
}

func assertLifecycleControlIdentityChanged(t *testing.T, previous, current resultidentity.Dependency) {
	t.Helper()
	previousAccess, currentAccess := previous.SemanticAccess(), current.SemanticAccess()
	if previousAccess == nil || currentAccess == nil {
		t.Fatal("lifecycle dependency lost protected semantic identity")
	}
	if previousAccess.ControlRevision == currentAccess.ControlRevision || previousAccess.ControlDigest == currentAccess.ControlDigest {
		t.Fatalf("control identity did not rotate: previous=(%d,%q) current=(%d,%q)", previousAccess.ControlRevision, previousAccess.ControlDigest, currentAccess.ControlRevision, currentAccess.ControlDigest)
	}
}
