package http

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectview "github.com/flidai/leapview/internal/project"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestBuildDataExplorerProjectionOmitsProtectedModelWithoutConsumer(t *testing.T) {
	model := explorerSemanticAccessProjectionModel()
	model.AccessPolicy.AccessGrants = map[string]semanticmodel.SemanticAccessGrantSpec{
		"view_sales": {UserAttribute: "department"},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	project := projectmanifest.ResourceManifest{
		Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"]},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales"},
	}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{}, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled})
	if len(projection.SemanticModels) != 0 || projection.SelectedSemanticModel != nil {
		t.Fatalf("protected semantic projection = %#v/%#v, want omitted without consumer", projection.SemanticModels, projection.SelectedSemanticModel)
	}
}

func TestBuildDataExplorerProjectionOmitsProtectedModelWithNoEffectiveDatasetAssignments(t *testing.T) {
	model := explorerSemanticAccessProjectionModel()
	literal, err := semanticmodel.NewSemanticAccessLiteral("sales")
	if err != nil {
		t.Fatal(err)
	}
	model.AccessPolicy.AccessGrants = map[string]semanticmodel.SemanticAccessGrantSpec{
		"view_sales": {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal}},
	}
	model.AccessPolicy.Datasets = map[string]semanticmodel.SemanticDatasetAccessSpec{
		"orders": {RequiredAccessGrants: []string{"view_sales"}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	consumer := newNoEffectiveDatasetConsumer(t, compiled)
	project := projectmanifest.ResourceManifest{
		Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"]},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{}, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}, map[string]*semanticquery.SemanticAccessConsumer{"semantic:sales": consumer})
	if len(projection.SemanticModels) != 0 || projection.SelectedSemanticModel != nil {
		t.Fatalf("protected semantic projection = %#v/%#v, want omitted with no effective dataset assignment", projection.SemanticModels, projection.SelectedSemanticModel)
	}
}

func newNoEffectiveDatasetConsumer(t *testing.T, compiled *semanticquery.CompiledModel) *semanticquery.SemanticAccessConsumer {
	t.Helper()
	definition := access.SemanticAttributeDefinition{
		ID: "def-department", Name: "department", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
	}
	registryDigest, err := access.SemanticAttributeRegistryDigest(semanticvalue.Profile, []access.SemanticAttributeDefinition{definition})
	if err != nil {
		t.Fatal(err)
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: registryDigest},
		Definitions: []access.SemanticAttributeDefinition{definition},
	}
	controlDigest, err := access.SemanticAttributeControlDigest(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	control := access.SemanticAttributeControlSnapshot{
		State: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: controlDigest},
	}
	attributeDigest, err := semanticquery.EffectiveSemanticAttributeDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := semanticquery.SemanticAccessAttributeSnapshot{
		InstanceID: "instance-1", PrincipalID: "principal-1", ActorID: "principal-1", Registry: registry, Control: control,
		EffectiveAttributeDigest: attributeDigest,
	}
	authority := semanticquery.SemanticAccessAuthority{InstanceID: "instance-1", Registry: registry, Control: control, ObservedAt: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)}
	consumer, err := semanticquery.NewSemanticAccessDiscovery(compiled, semanticquery.SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic:sales", Generation: "generation-1", PrincipalID: "principal-1",
		Authority: func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

func TestExplorerProtectedProjectionFiltersPhysicalAndAuxiliaryFields(t *testing.T) {
	model := explorerSemanticAccessProjectionModel()
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	access := &explorerSemanticAccess{
		datasets:   map[string]bool{"orders": true},
		dimensions: map[string]map[string]bool{"region": {"orders": true}},
		metrics:    map[string]bool{},
	}
	fields := explorerFields(model, "orders", dataExploreState{}, compiled, access)
	// Both selectable forms of the authorized field are intentional: the
	// physical rows reference and the governed semantic Analyze reference.
	if len(fields) != 2 || fields[0].ID != "orders.region" || fields[1].ID != "region" {
		t.Fatalf("protected fields = %#v, want only authorized physical and semantic region references", fields)
	}
	datasets := explorerDatasets(model, compiled, access)
	if len(datasets) != 1 || datasets[0].FieldCount != 1 {
		t.Fatalf("protected datasets = %#v, want one authorized field", datasets)
	}
	if len(datasets[0].Entities) != 0 || datasets[0].GrainEntity != "" || len(datasets[0].GrainFields) != 0 {
		t.Fatalf("protected auxiliary entity metadata = %#v, want hidden unauthorized grain", datasets[0])
	}
}

func explorerSemanticAccessProjectionModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
				GrainEntity: "order",
				Columns: map[string]semanticmodel.ModelColumn{
					"id": {Name: "id", Type: "integer"}, "region": {Name: "region", Type: "string"}, "secret": {Name: "secret", Type: "string"},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"id": {Type: "number", Datatype: semanticmodel.DataTypeInteger}, "region": {Type: "string", Datatype: semanticmodel.DataTypeString}, "secret": {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"region": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.region"}}},
			"secret": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.secret"}}},
		},
	}
}
