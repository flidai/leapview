package contractpublication

import (
	"errors"
	"reflect"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPolicyEvidenceBindsExactDirectAffectedResource(t *testing.T) {
	baseline := semanticPublication(t, "1.0.0", `["east"]`)
	candidate := semanticPublication(t, "1.1.0", `["east","west"]`)
	evidence, err := DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	want := directAffectedResources(candidate.Identity())
	if !equalAffectedResources(evidence.AffectedResources, want) {
		t.Fatalf("affected resources = %#v, want %#v", evidence.AffectedResources, want)
	}
	detached := evidence.Affected()
	detached[0].AuthoredID = "semantic-model:detached"
	if !equalAffectedResources(evidence.AffectedResources, want) {
		t.Fatalf("affected-resource accessor mutated evidence: %#v", evidence.AffectedResources)
	}

	for name, mutate := range map[string]func(*PolicyEvidence){
		"missing": func(value *PolicyEvidence) { value.AffectedResources = nil },
		"extra": func(value *PolicyEvidence) {
			value.AffectedResources = append(value.AffectedResources, value.AffectedResources[0])
		},
		"different resource": func(value *PolicyEvidence) {
			value.AffectedResources[0].AuthoredID = "semantic-model:other"
		},
		"different scope": func(value *PolicyEvidence) {
			value.AffectedResources[0].Scope = "consumer-graph"
		},
	} {
		t.Run(name, func(t *testing.T) {
			tampered := evidence.Clone()
			mutate(&tampered)
			digest, digestErr := tampered.computeDigest()
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			tampered.EvidenceDigest = digest
			if err := tampered.Validate(); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("tampered affected-resource evidence error = %v", err)
			}
		})
	}
}

func TestWideningApprovalDigestBindsAffectedResourceEvidence(t *testing.T) {
	baseline := semanticPublication(t, "1.0.0", `["east"]`)
	candidate := semanticPublication(t, "1.1.0", `["east","west"]`)
	policy, err := DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	approval, err := PrepareWideningApproval(policy, "principal:reviewer", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	tampered := policy.Clone()
	tampered.AffectedResources[0].Scope = "consumer-graph"
	digest, err := tampered.computeDigest()
	if err != nil {
		t.Fatal(err)
	}
	tampered.EvidenceDigest = digest
	if err := ValidateAdmission(PolicyContext{BaselineKind: BaselineExisting, Existing: &baseline}, candidate, tampered, &approval, now); err == nil {
		t.Fatal("approval accepted altered affected-resource evidence")
	}
}

func TestDirectAffectedResourceSeedQualifiesTransitiveDashboardConsumers(t *testing.T) {
	candidate := testPublication(t, "1.0.0", false)
	evidence, err := DeriveGenesisPolicyEvidence(candidate)
	if err != nil {
		t.Fatal(err)
	}
	direct := evidence.Affected()
	if len(direct) != 1 || direct[0].AuthoredID != "source:orders" || direct[0].Scope != PolicyAffectedResourceScope {
		t.Fatalf("direct affected-resource seed = %#v", direct)
	}

	resources := []projectgraph.Resource{
		{ID: "dashboard:direct", Kind: projectgraph.KindDashboard, Name: "direct"},
		{ID: "dashboard:transitive", Kind: projectgraph.KindDashboard, Name: "transitive"},
		{ID: "dashboard:unrelated", Kind: projectgraph.KindDashboard, Name: "unrelated"},
		{ID: "model:b", Kind: projectgraph.KindModel, Name: "b"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "source:unrelated", Kind: projectgraph.KindSource, Name: "unrelated_source"},
	}
	edges := []projectgraph.Edge{
		{From: "dashboard:direct", To: "source:orders", Relation: "reads_source"},
		{From: "dashboard:transitive", To: "model:b", Relation: "queries_model"},
		{From: "model:b", To: "source:orders", Relation: "reads_source"},
		{From: "dashboard:unrelated", To: "source:unrelated", Relation: "reads_source"},
	}
	project, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}

	// Repeating the direct seed models duplicate change input without mutating
	// immutable publication evidence. The graph owner deduplicates the input and
	// returns direct and transitive dashboard consumers in canonical ID order.
	changed := []projectgraph.ResourceID{direct[0].AuthoredID, direct[0].AuthoredID}
	want := []projectgraph.ResourceID{"dashboard:direct", "dashboard:transitive"}
	for attempt := 0; attempt < 5; attempt++ {
		if got := project.AffectedDashboards(changed); !reflect.DeepEqual(got, want) {
			t.Fatalf("attempt %d affected dashboards = %v, want %v", attempt, got, want)
		}
	}

	if _, err := projectgraph.NewProjectGraph(resources, append(edges, edges[0])); !errors.Is(err, projectgraph.ErrDuplicateEdge) {
		t.Fatalf("duplicate dependency error = %v, want %v", err, projectgraph.ErrDuplicateEdge)
	}
	cyclic := []projectgraph.Edge{
		{From: "dashboard:transitive", To: "model:b"},
		{From: "model:b", To: "source:orders"},
		{From: "source:orders", To: "dashboard:transitive"},
	}
	if _, err := projectgraph.NewProjectGraph(resources, cyclic); !errors.Is(err, projectgraph.ErrCycle) {
		t.Fatalf("cyclic dependency error = %v, want %v", err, projectgraph.ErrCycle)
	}
}
