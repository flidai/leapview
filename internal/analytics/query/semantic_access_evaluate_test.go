package query

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/trustedclaims"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticAccessClaimsVerifier struct {
	claims trustedclaims.VerifiedClaims
}

func (verifier semanticAccessClaimsVerifier) SourceKind() trustedclaims.SourceKind {
	return trustedclaims.SourceOIDC
}

func (verifier semanticAccessClaimsVerifier) Verify(context.Context, []byte) (trustedclaims.VerifiedClaims, error) {
	return verifier.claims, nil
}

func semanticAccessTrustedEnvelope(t *testing.T, subject string, now time.Time) trustedclaims.Envelope {
	t.Helper()
	claims := trustedclaims.VerifiedClaims{Provider: "identity-provider", Issuer: "https://issuer.example", Audience: "leapview",
		Subject: subject, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
		TokenFingerprint: strings.Repeat("a", 64), Claims: []trustedclaims.Claim{{Name: "regions", Value: []string{"east", "west"}}}}
	envelope, err := trustedclaims.Verify(t.Context(), trustedclaims.NewRawEvidence(trustedclaims.SourceOIDC, []byte("signed-evidence")),
		semanticAccessClaimsVerifier{claims: claims}, trustedclaims.VerifyOptions{Now: now})
	if err != nil {
		t.Fatalf("verify trusted claims: %v", err)
	}
	return envelope
}

func TestEvaluateSemanticAccessMatchesGrantsAndBuildsTypedPredicates(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	snapshot, authority := semanticAccessSnapshot(t, attributes)
	decision, err := EvaluateSemanticAccess(policy, snapshot, authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range decision.GrantResults {
		if !result.Allowed {
			t.Fatalf("grant %q unexpectedly denied: %#v", result.Name, decision.GrantResults)
		}
	}
	dataset, ok := decision.Dataset("orders")
	if !ok || !dataset.Allowed || dataset.Predicate == nil || dataset.Predicate.Kind != planir.PredicateAnd || len(dataset.Predicate.Children) != 6 {
		t.Fatalf("dataset decision = %#v", dataset)
	}
	wantKinds := map[string]planir.PredicateKind{
		"orders.account_id": planir.PredicateIn, "orders.amount": planir.PredicateCompare, "orders.approved": planir.PredicateCompare,
		"orders.occurred_at": planir.PredicateCompare, "orders.order_date": planir.PredicateCompare, "orders.region": planir.PredicateIn,
	}
	for _, predicate := range dataset.Predicate.Children {
		if predicate.Kind != wantKinds[predicate.Field] {
			t.Fatalf("predicate for %q = %#v", predicate.Field, predicate)
		}
		if predicate.Field == "orders.account_id" && (len(predicate.Values) != 2 || predicate.Values[1].NumberText != "9007199254740993" || predicate.Values[1].NumberKind != planir.NumberInteger) {
			t.Fatalf("integer predicate lost exactness: %#v", predicate)
		}
		if predicate.Field == "orders.amount" && (predicate.Value.NumberText != "9007199254740993.125" || predicate.Value.NumberKind != planir.NumberDecimal) {
			t.Fatalf("decimal predicate lost exactness: %#v", predicate)
		}
	}
	for _, name := range []string{"revenue", "doubledRevenue", "averageRevenue"} {
		metric, ok := decision.Metric(name)
		if !ok || !metric.Allowed {
			t.Fatalf("metric %q decision = %#v", name, metric)
		}
	}
	if !strings.HasPrefix(decision.IdentityDigest, "sha256:") || len(decision.FilterEvidence) != 6 {
		t.Fatalf("decision identity/evidence = %q %#v", decision.IdentityDigest, decision.FilterEvidence)
	}
}

func TestEvaluateSemanticAccessIsIndependentOfAttributeOrdering(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	firstSnapshot, firstAuthority := semanticAccessSnapshot(t, attributes)
	first, err := EvaluateSemanticAccess(policy, firstSnapshot, firstAuthority)
	if err != nil {
		t.Fatal(err)
	}
	secondSnapshot, secondAuthority := semanticAccessSnapshot(t, reverseEffectiveAttributes(attributes))
	second, err := EvaluateSemanticAccess(policy, secondSnapshot, secondAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if first.IdentityDigest != second.IdentityDigest || first.EffectiveAttributeDigest != second.EffectiveAttributeDigest || !reflect.DeepEqual(first.GrantResults, second.GrantResults) {
		t.Fatalf("evaluation depends on presentation order: %#v != %#v", first, second)
	}
}

func TestEvaluateSemanticAccessMissingAttributeDeniesWithoutAdminBypass(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	filtered := attributes[:0]
	for _, attribute := range attributes {
		if attribute.DefinitionName != "regions" {
			filtered = append(filtered, attribute)
		}
	}
	snapshot, authority := semanticAccessSnapshotForPrincipal(t, "administrator", filtered)
	decision, err := EvaluateSemanticAccess(policy, snapshot, authority)
	if err != nil {
		t.Fatal(err)
	}
	dataset, _ := decision.Dataset("orders")
	if dataset.Allowed || dataset.Predicate != nil || !reflect.DeepEqual(dataset.DeniedDatasets, []string{"orders"}) {
		t.Fatalf("missing attribute did not fail closed: %#v", dataset)
	}
	metric, _ := decision.Metric("revenue")
	if metric.Allowed || !reflect.DeepEqual(metric.DeniedDatasets, []string{"orders"}) {
		t.Fatalf("protected metric did not inherit dataset denial: %#v", metric)
	}
}

func TestEvaluateSemanticAccessRequiredGrantsUseLogicalAND(t *testing.T) {
	model := semanticAccessTestModel(t)
	grant := model.AccessPolicy.AccessGrants["canViewSales"]
	grant.AllowedValues = append([]semanticmodel.SemanticAccessLiteral(nil), grant.AllowedValues...)
	grant.AllowedValues[0].Text = "engineering"
	grant.AllowedValues = grant.AllowedValues[:1]
	model.AccessPolicy.AccessGrants["engineeringOnly"] = grant
	dataset := model.AccessPolicy.Datasets["orders"]
	dataset.RequiredAccessGrants = []string{"engineeringOnly", "canViewSales"}
	model.AccessPolicy.Datasets["orders"] = dataset
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	decision, err := EvaluateSemanticAccess(policy, snapshot, authority)
	if err != nil {
		t.Fatal(err)
	}
	orders, _ := decision.Dataset("orders")
	if orders.Allowed || !reflect.DeepEqual(orders.DeniedGrants, []string{"engineeringOnly"}) {
		t.Fatalf("conflicting required grants did not use AND: %#v", orders)
	}
}

func TestEvaluateSemanticAccessDeniedMembersDoNotExposeDatasetPredicates(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	for index := range attributes {
		if attributes[index].DefinitionName != "accountIds" {
			continue
		}
		definition := semanticAccessDefinitions()[0]
		var err error
		attributes[index].CanonicalValues, attributes[index].ValueDigest, err = access.CanonicalSemanticAttributeValues(definition, []int{8})
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, authority := semanticAccessSnapshot(t, attributes)
	decision, err := EvaluateSemanticAccess(policy, snapshot, authority)
	if err != nil {
		t.Fatal(err)
	}
	dimension, ok := decision.Dimension("region", "orders")
	if !ok || dimension.Allowed || dimension.Predicate != nil || len(dimension.DatasetPredicates) != 0 {
		t.Fatalf("denied dimension retained predicates: %#v", dimension)
	}
	metric, ok := decision.Metric("revenue")
	if !ok || metric.Allowed || metric.Predicate != nil || len(metric.DatasetPredicates) != 0 {
		t.Fatalf("denied metric retained predicates: %#v", metric)
	}
}

func TestEvaluateSemanticAccessRejectsStaleRegistryAndControlSnapshots(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	cases := []struct {
		name   string
		change func(*SemanticAccessAttributeSnapshot, *SemanticAccessAuthority)
	}{
		{name: "captured registry", change: func(snapshot *SemanticAccessAttributeSnapshot, _ *SemanticAccessAuthority) {
			snapshot.Registry.State.Revision--
			snapshot.Registry.Definitions = replaceDefinition(snapshot.Registry.Definitions, "regions", func(value *access.SemanticAttributeDefinition) { value.Metadata.DisplayName = "changed" })
			snapshot.Registry.State.Digest, _ = access.SemanticAttributeRegistryDigest(snapshot.Registry.State.Profile, snapshot.Registry.Definitions)
		}},
		{name: "current registry", change: func(_ *SemanticAccessAttributeSnapshot, authority *SemanticAccessAuthority) {
			authority.Registry.State.Revision++
			authority.Registry.Definitions = replaceDefinition(authority.Registry.Definitions, "regions", func(value *access.SemanticAttributeDefinition) { value.Metadata.DisplayName = "changed" })
			authority.Registry.State.Digest, _ = access.SemanticAttributeRegistryDigest(authority.Registry.State.Profile, authority.Registry.Definitions)
		}},
		{name: "current control", change: func(_ *SemanticAccessAttributeSnapshot, authority *SemanticAccessAuthority) {
			authority.Control.State.Revision++
		}},
		{name: "captured control", change: func(snapshot *SemanticAccessAttributeSnapshot, _ *SemanticAccessAuthority) {
			snapshot.Control.State.Revision--
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			snapshot, authority := semanticAccessSnapshot(t, attributes)
			test.change(&snapshot, &authority)
			_, err := EvaluateSemanticAccess(policy, snapshot, authority)
			requireSemanticAccessError(t, err, "stale or inconsistent")
		})
	}
}

func TestEvaluateSemanticAccessRejectsCrossInstanceSnapshot(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	snapshot.InstanceID = "instance-2"
	_, err := EvaluateSemanticAccess(policy, snapshot, authority)
	requireSemanticAccessError(t, err, "target instance")

	snapshot, authority = semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	authority.InstanceID = "instance-2"
	_, err = EvaluateSemanticAccess(policy, snapshot, authority)
	requireSemanticAccessError(t, err, "current authority target instance")
}

func TestEvaluateSemanticAccessRejectsUnverifiedAuthorityContents(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	snapshot, authority := semanticAccessSnapshot(t, attributes)
	snapshot.Registry.Definitions[0].Metadata.DisplayName = "tampered"
	_, err := EvaluateSemanticAccess(policy, snapshot, authority)
	requireSemanticAccessError(t, err, "captured registry identity")

	snapshot, authority = semanticAccessSnapshot(t, attributes)
	snapshot.Control.Mappings = []access.TrustedClaimMapping{{ID: "tampered"}}
	_, err = EvaluateSemanticAccess(policy, snapshot, authority)
	requireSemanticAccessError(t, err, "control state is corrupt")

	snapshot, authority = semanticAccessSnapshot(t, attributes)
	authority.ObservedAt = time.Time{}
	_, err = EvaluateSemanticAccess(policy, snapshot, authority)
	requireSemanticAccessError(t, err, "observation time")

	snapshot, authority = semanticAccessSnapshot(t, attributes)
	snapshot.Control.Assignments[0].DefinitionName = "wrongName"
	authority.Control = snapshot.Control
	_, err = EvaluateSemanticAccess(policy, snapshot, authority)
	requireSemanticAccessError(t, err, "does not match registry definition")
}

func TestEvaluateSemanticAccessRequiresDirectAssignmentEvidence(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	snapshot, authority := semanticAccessSnapshot(t, attributes)
	snapshot.DirectAssignmentEvidence = access.SemanticAttributeDirectEvidence{}
	_, err := EvaluateSemanticAccess(policy, snapshot, authority)
	requireSemanticAccessError(t, err, "direct assignment evidence")

	snapshot, authority = semanticAccessSnapshot(t, attributes)
	definition := semanticAccessDefinitions()[0]
	snapshot.Control.Assignments[0].CanonicalValues, snapshot.Control.Assignments[0].ValueDigest, err =
		access.CanonicalSemanticAttributeValues(definition, []int{8})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Control.State.Digest, err = access.SemanticAttributeControlDigest(snapshot.Control.Assignments, snapshot.Control.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	authority.Control = snapshot.Control
	_, err = EvaluateSemanticAccess(policy, snapshot, authority)
	requireSemanticAccessError(t, err, "direct assignment evidence")
}

func TestSemanticAccessObjectDecisionRetainsEveryRequiredDatasetPredicate(t *testing.T) {
	orders := planir.Predicate{Kind: planir.PredicateCompare, Field: "orders.region", Operator: "=", Value: planir.Literal{Kind: planir.LiteralString, String: "west"}}
	customers := planir.Predicate{Kind: planir.PredicateIn, Field: "customers.account_id", Values: []planir.Literal{{Kind: planir.LiteralNumber, NumberText: "7", NumberKind: planir.NumberInteger}}}
	datasets := map[string]SemanticAccessObjectDecision{
		"orders":    {Name: "orders", Dataset: "orders", Allowed: true, Predicate: &orders},
		"customers": {Name: "customers", Dataset: "customers", Allowed: true, Predicate: &customers},
	}
	object := SemanticAccessObjectDecision{Name: "customerRevenue", Dataset: "orders", Allowed: true}
	inheritDatasetDecisions(&object, []string{"customers", "orders"}, datasets)
	if !object.Allowed || len(object.DatasetPredicates) != 2 || object.DatasetPredicates["orders"].Field != "orders.region" ||
		object.DatasetPredicates["customers"].Field != "customers.account_id" {
		t.Fatalf("joined decision lost governed predicates: %#v", object)
	}
	clone := cloneSemanticAccessObjectDecision(object)
	mutated := clone.DatasetPredicates["customers"]
	mutated.Values[0].NumberText = "8"
	clone.DatasetPredicates["customers"] = mutated
	if object.DatasetPredicates["customers"].Values[0].NumberText != "7" {
		t.Fatal("cloned decision aliases a dataset predicate")
	}
}

func TestEvaluateSemanticAccessBindsClaimDerivedAttributesToVerifiedEvidence(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	for index := range attributes {
		if attributes[index].DefinitionName == "regions" {
			attributes[index].Source = "trusted_claim"
		}
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	snapshot, authority := semanticAccessSnapshot(t, attributes)
	envelope := semanticAccessTrustedEnvelope(t, snapshot.PrincipalID, now)
	mapping := access.TrustedClaimMapping{ID: "mapping-regions", SourceKind: access.TrustedClaimSourceOIDC,
		Provider: envelope.Provider(), Issuer: envelope.Issuer(), Audience: envelope.Audience(), Claim: "regions",
		DefinitionID: "def-regions", DefinitionName: "regions", DefinitionVersion: 1, Type: semanticvalue.TypeString,
		Shape: access.SemanticAttributeList, MappingVersion: 1}
	control := semanticAccessControl(snapshot.Control.Assignments, []access.TrustedClaimMapping{mapping})
	snapshot.Control = control
	authority.Control = control
	authority.ObservedAt = now
	directEvidence, err := access.NewSemanticAttributeDirectEvidence(snapshot.InstanceID, snapshot.PrincipalID,
		[]access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: snapshot.PrincipalID}}, control, attributes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.DirectAssignmentEvidence = directEvidence
	evidence, err := access.NewSemanticAttributeClaimEvidence(snapshot.InstanceID, snapshot.PrincipalID, control, attributes, envelope, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.TrustedClaimEvidence = evidence
	decision, err := EvaluateSemanticAccess(policy, snapshot, authority)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(decision.TrustedClaimEvidenceDigest, "sha256:") {
		t.Fatalf("trusted claim evidence digest = %q", decision.TrustedClaimEvidenceDigest)
	}

	missing := snapshot
	missing.TrustedClaimEvidence = access.SemanticAttributeClaimEvidence{}
	_, err = EvaluateSemanticAccess(policy, missing, authority)
	requireSemanticAccessError(t, err, "does not match")

	expiredAuthority := authority
	expiredAuthority.ObservedAt = now.Add(time.Minute)
	_, err = EvaluateSemanticAccess(policy, snapshot, expiredAuthority)
	requireSemanticAccessError(t, err, "expired")

	wrongSubject := snapshot
	wrongSubject.PrincipalID = "principal-2"
	_, err = EvaluateSemanticAccess(policy, wrongSubject, authority)
	requireSemanticAccessError(t, err, "does not match")

	tampered := snapshot
	tampered.EffectiveAttributes = append([]access.EffectiveSemanticAttribute(nil), snapshot.EffectiveAttributes...)
	for index := range tampered.EffectiveAttributes {
		if tampered.EffectiveAttributes[index].DefinitionName == "regions" {
			tampered.EffectiveAttributes[index].CanonicalValues = []string{"north"}
			definition := semanticAccessDefinitions()[6]
			values, digest, valueErr := access.CanonicalSemanticAttributeValues(definition, []string{"north"})
			if valueErr != nil {
				t.Fatal(valueErr)
			}
			tampered.EffectiveAttributes[index].CanonicalValues = values
			tampered.EffectiveAttributes[index].ValueDigest = digest
		}
	}
	tampered.EffectiveAttributeDigest, err = EffectiveSemanticAttributeDigest(tampered.EffectiveAttributes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EvaluateSemanticAccess(policy, tampered, authority)
	requireSemanticAccessError(t, err, "does not match")
}

func TestEvaluateSemanticAccessRejectsTamperedEffectiveState(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	base := semanticAccessEffective(t, semanticAccessDefinitions())
	cases := []struct {
		name   string
		change func([]access.EffectiveSemanticAttribute, *SemanticAccessAttributeSnapshot)
		want   string
	}{
		{name: "claimed digest", change: func(_ []access.EffectiveSemanticAttribute, snapshot *SemanticAccessAttributeSnapshot) {
			snapshot.EffectiveAttributeDigest = "sha256:" + strings.Repeat("c", 64)
		}, want: "digest mismatch"},
		{name: "value", change: func(values []access.EffectiveSemanticAttribute, _ *SemanticAccessAttributeSnapshot) {
			values[0].CanonicalValues[0] = "8"
		}, want: "value digest mismatch"},
		{name: "version", change: func(values []access.EffectiveSemanticAttribute, snapshot *SemanticAccessAttributeSnapshot) {
			values[0].DefinitionVersion++
			digest, digestErr := EffectiveSemanticAttributeDigest(values)
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			snapshot.EffectiveAttributeDigest = digest
		}, want: "direct assignment evidence"},
		{name: "source", change: func(values []access.EffectiveSemanticAttribute, _ *SemanticAccessAttributeSnapshot) {
			values[0].Source = "browser"
		}, want: "source is invalid"},
		{name: "duplicate id", change: func(values []access.EffectiveSemanticAttribute, _ *SemanticAccessAttributeSnapshot) {
			values[1].DefinitionID = values[0].DefinitionID
		}, want: "duplicate effective attribute"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			values := make([]access.EffectiveSemanticAttribute, len(base))
			for index, value := range base {
				values[index] = value
				values[index].CanonicalValues = append([]string(nil), value.CanonicalValues...)
			}
			snapshot, authority := semanticAccessSnapshot(t, values)
			test.change(values, &snapshot)
			snapshot.EffectiveAttributes = values
			_, err := EvaluateSemanticAccess(policy, snapshot, authority)
			requireSemanticAccessError(t, err, test.want)
		})
	}
}

func TestEffectiveSemanticAttributeDigestRejectsEmptyOversizedAndUnorderedLists(t *testing.T) {
	definition := semanticAccessDefinitions()[0]
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	attribute := access.EffectiveSemanticAttribute{DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: 1,
		Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct"}

	unordered := attribute
	unordered.CanonicalValues = []string{"2", "1"}
	_, err = EffectiveSemanticAttributeDigest([]access.EffectiveSemanticAttribute{unordered})
	requireSemanticAccessError(t, err, "canonically ordered")

	empty := attribute
	empty.CanonicalValues = nil
	_, err = EffectiveSemanticAttributeDigest([]access.EffectiveSemanticAttribute{empty})
	requireSemanticAccessError(t, err, "cardinality")

	oversized := attribute
	oversized.CanonicalValues = make([]string, semanticvalue.MaxSetValues+1)
	_, err = EffectiveSemanticAttributeDigest([]access.EffectiveSemanticAttribute{oversized})
	requireSemanticAccessError(t, err, "cardinality")
}
