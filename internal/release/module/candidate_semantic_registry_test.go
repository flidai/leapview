package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type candidateRegistryFixture struct {
	value  access.SemanticRegistryContext
	reads  int
	change bool
}

func (r *candidateRegistryFixture) ReadSemanticRegistry(context.Context, string) (access.SemanticRegistryContext, error) {
	r.reads++
	value := r.value
	if r.change && r.reads > 1 {
		value.Registry.State.Revision++
	}
	return value, nil
}

func TestCandidateSemanticRegistryQualification(t *testing.T) {
	for _, name := range []string{"valid", "missing", "stale", "cross-instance", "cross-project", "wrong-type"} {
		t.Run(name, func(t *testing.T) {
			registry := &candidateRegistryFixture{value: access.SemanticRegistryContext{
				Control: access.AuthorizationControlRevision{InstanceID: "instance:one", ProjectID: "project:one", Revision: 1},
				Registry: access.SemanticAttributeRegistrySnapshot{
					State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
					Definitions: []access.SemanticAttributeDefinition{{ID: "definition:region", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true}},
				},
			}}
			service := &candidateArtifactService{instanceID: "instance:one", semanticRegistry: registry}
			switch name {
			case "missing":
				service.semanticRegistry = nil
			case "stale":
				registry.change = true
			case "cross-instance":
				registry.value.Control.InstanceID = "instance:two"
			case "cross-project":
				registry.value.Control.ProjectID = "project:two"
			case "wrong-type":
				registry.value.Registry.Definitions[0].Type = semanticvalue.TypeBoolean
			}
			model := &semanticmodel.Model{
				Name:         "semantic:orders",
				Tables:       map[string]semanticmodel.Table{"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"id": {Type: "integer", Datatype: semanticmodel.DataTypeInteger}}, Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}}, GrainEntity: "order"}},
				Datasets:     map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders", RequiredAccessGrants: []string{"region_access"}}},
				AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{"region_access": {UserAttribute: "region", AllowedValues: []any{"us"}}},
			}
			value, err := service.compileCandidateSemanticAccess(t.Context(), "project:one", map[string]*semanticmodel.Model{"semantic:orders": model})
			if name == "valid" {
				if err != nil || value == nil || value.Registry.State.Revision != 1 {
					t.Fatalf("valid candidate registry: value=%+v err=%v", value, err)
				}
			} else if err == nil {
				t.Fatalf("%s registry accepted", name)
			}
		})
	}
}
