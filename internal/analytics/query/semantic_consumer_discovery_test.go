package query

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func newDiscoveryConsumer(t *testing.T, snapshot SemanticAccessAttributeSnapshot, authority SemanticAccessAuthority) *SemanticAccessConsumer {
	t.Helper()
	planner, err := NewCompiledPlanner(semanticAccessTestModel(t))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID, Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
		return snapshot, authority, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

func TestSemanticConsumerDiscoverySortedDetachedAndTransitive(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	assets, err := consumer.Assets()
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(assets)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, asset := range assets {
		found[asset.Kind] = true
		if asset.PolicyIdentity.PolicyDigest == "" || len(asset.DependencyDatasets) == 0 {
			t.Fatalf("unqualified discovery: %#v", asset)
		}
		if asset.Kind == "metric" && asset.Name == "doubledRevenue" && len(asset.RequiredAccessGrants) != 3 {
			t.Fatalf("lost transitive grants: %#v", asset)
		}
	}
	for _, kind := range []string{"dataset", "dimension", "metric"} {
		if !found[kind] {
			t.Fatalf("missing %s discovery", kind)
		}
	}
	assets[0].Name = "mutated"
	assets[0].DependencyDatasets[0] = "mutated"
	if len(assets[0].RequiredAccessGrants) > 0 {
		assets[0].RequiredAccessGrants[0] = "mutated"
	}
	again, err := consumer.Assets()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(again)
	if !bytes.Equal(original, encoded) {
		t.Fatal("caller mutated authoritative discovery")
	}
	snapshot.Registry.Definitions = reverseDefinitions(snapshot.Registry.Definitions)
	authority.Registry.Definitions = reverseDefinitions(authority.Registry.Definitions)
	snapshot.EffectiveAttributes = reverseEffectiveAttributes(snapshot.EffectiveAttributes)
	reordered := newDiscoveryConsumer(t, snapshot, authority)
	other, err := reordered.Assets()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(other)
	if !bytes.Equal(original, encoded) {
		t.Fatal("authority ordering changed discovery")
	}
}

func TestSemanticConsumerDeniedAssetsAreNotDiscoverableOrDirectlyUsable(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, nil)
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	assets, err := consumer.Assets()
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range assets {
		if asset.Name == "orders" || asset.Name == "revenue" {
			t.Fatalf("denied asset discovered: %#v", asset)
		}
	}
	for _, target := range []SemanticAccessTarget{{Dataset: "orders"}, {Metric: "revenue"}, {Dataset: "orders", Dimension: "region"}} {
		if err := consumer.Authorize(target); err == nil {
			t.Fatalf("denied direct target admitted: %#v", target)
		}
	}
}

func TestSemanticConsumerTombstonedAssignmentCannotAuthorizeDiscovery(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	snapshot.Control.Assignments[0].Tombstoned = true
	snapshot.Control.State.Digest, _ = access.SemanticAttributeControlDigest(snapshot.Control.Assignments, snapshot.Control.Mappings)
	authority.Control = snapshot.Control
	planner, err := NewCompiledPlanner(semanticAccessTestModel(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID, Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
		return snapshot, authority, nil
	}})
	if err == nil {
		t.Fatal("tombstoned assignment evidence admitted")
	}
}

func TestSemanticConsumerRejectsInvalidAndDuplicateAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*access.SemanticAttributeRegistrySnapshot)
	}{
		{"disabled", func(r *access.SemanticAttributeRegistrySnapshot) {
			r.Definitions[0].Enabled = false
			r.Definitions[0].LifecycleState = access.SemanticAttributeDisabled
			r.Definitions[0].DisabledAt = semanticAccessObservedAt
		}},
		{"duplicate", func(r *access.SemanticAttributeRegistrySnapshot) {
			r.Definitions = append(r.Definitions, r.Definitions[0])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, current := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
			test.change(&snapshot.Registry)
			snapshot.Registry.State.Digest, _ = access.SemanticAttributeRegistryDigest(snapshot.Registry.State.Profile, snapshot.Registry.Definitions)
			current.Registry = snapshot.Registry
			planner, err := NewCompiledPlanner(semanticAccessTestModel(t))
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID, Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
				return snapshot, current, nil
			}})
			if err == nil {
				t.Fatal("unusable registry authority accepted")
			}
		})
	}
}

func TestSemanticConsumerFilterOwnershipDoesNotUseGrantNameCollisions(t *testing.T) {
	model := semanticAccessTestModel(t)
	model.AccessPolicy.AccessGrants["regions"] = semanticmodel.SemanticAccessGrantSpec{
		UserAttribute: "department",
		AllowedValues: []semanticmodel.SemanticAccessLiteral{{Kind: semanticmodel.SemanticAccessString, Text: "sales"}},
	}
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assets, err := consumer.Assets()
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range assets {
		if asset.Kind != "dataset" || asset.Name != "orders" {
			continue
		}
		foundFilterOwner := false
		for _, owner := range asset.Ownership {
			if owner.Attribute == "regions" {
				foundFilterOwner = true
				if owner.DefinitionID != "def-regions" {
					t.Fatalf("filter ownership used colliding grant definition: %#v", owner)
				}
			}
		}
		if !foundFilterOwner {
			t.Fatal("filter-only ownership metadata was omitted")
		}
		return
	}
	t.Fatal("orders dataset was not discovered")
}
