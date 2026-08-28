package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const resultIdentityRelationDigestDomain = "flid.resultidentity.relation.v1"

// RelationExecutionContexts derives the target-bound, relation-scoped context
// consumed by RelationExecutionDigestsByContext. Managed revisions and
// connector kinds are selected only for sources reachable from each physical
// model relation. Unknown lineage fails safe by including all source and
// connection evidence.
func (p Project) RelationExecutionContexts(revisions, bindingKinds map[string]string) (map[string]string, error) {
	return p.relationExecutionContexts(revisions, bindingKinds,
		func(value semanticmodel.Source) any { return value },
		func(value semanticmodel.Connection) any { return value },
	)
}

func (p Project) relationExecutionContexts(
	revisions, bindingKinds map[string]string,
	projectSource func(semanticmodel.Source) any,
	projectConnection func(semanticmodel.Connection) any,
) (map[string]string, error) {
	manifest := p.Manifest()
	modelNames := make(map[string]string)
	sourceNames := make(map[string]string)
	connectionNames := make(map[string]string)
	for _, resource := range p.Graph().Resources() {
		switch resource.Kind {
		case projectgraph.KindModel:
			modelNames[resource.ID.String()] = resource.ID.String()
			modelNames[resource.Name] = resource.ID.String()
		case projectgraph.KindSource:
			sourceNames[resource.ID.String()] = resource.ID.String()
			sourceNames[resource.Name] = resource.ID.String()
		case projectgraph.KindConnection:
			connectionNames[resource.ID.String()] = resource.ID.String()
			connectionNames[resource.Name] = resource.ID.String()
		}
	}
	allSources := make(map[string]any, len(manifest.Sources))
	for id, source := range manifest.Sources {
		allSources[id] = projectSource(source)
	}
	allConnections := make(map[string]any, len(manifest.Connections))
	for id, connection := range manifest.Connections {
		allConnections[id] = projectConnection(connection)
	}

	type relationRefs struct {
		sources     map[string]struct{}
		connections map[string]struct{}
		unknown     bool
	}
	var collect func(string, map[string]bool) (relationRefs, error)
	collect = func(modelID string, visiting map[string]bool) (relationRefs, error) {
		if visiting[modelID] {
			return relationRefs{}, fmt.Errorf("relation context dependency cycle at %q", modelID)
		}
		table, ok := manifest.Models[modelID]
		if !ok {
			return relationRefs{}, fmt.Errorf("relation context model %q is missing", modelID)
		}
		visiting[modelID] = true
		refs := relationRefs{sources: make(map[string]struct{}), connections: make(map[string]struct{})}
		for _, reference := range table.SourceDependencies {
			reference = strings.TrimSpace(reference)
			if reference == "" {
				continue
			}
			if sourceID := sourceNames[reference]; sourceID != "" {
				refs.sources[sourceID] = struct{}{}
			} else {
				refs.unknown = true
			}
		}
		for _, dependency := range table.ModelDependencies {
			dependencyID := modelNames[strings.TrimSpace(dependency)]
			if dependencyID == "" {
				refs.unknown = true
				continue
			}
			dependencyRefs, err := collect(dependencyID, visiting)
			if err != nil {
				return relationRefs{}, err
			}
			for source := range dependencyRefs.sources {
				refs.sources[source] = struct{}{}
			}
			for connection := range dependencyRefs.connections {
				refs.connections[connection] = struct{}{}
			}
			refs.unknown = refs.unknown || dependencyRefs.unknown
		}
		for sourceID := range refs.sources {
			source, exists := manifest.Sources[sourceID]
			if !exists {
				refs.unknown = true
				continue
			}
			connectionID := connectionNames[strings.TrimSpace(source.Connection)]
			if connectionID == "" && strings.TrimSpace(source.Connection) != "" {
				refs.unknown = true
			} else if connectionID != "" {
				refs.connections[connectionID] = struct{}{}
			}
		}
		delete(visiting, modelID)
		return refs, nil
	}

	contexts := make(map[string]string, len(manifest.Models))
	for modelID := range manifest.Models {
		refs, err := collect(modelID, map[string]bool{})
		if err != nil {
			return nil, err
		}
		sources, connections := make(map[string]any), make(map[string]any)
		if refs.unknown {
			sources, connections = allSources, allConnections
		} else {
			for sourceID := range refs.sources {
				sources[sourceID] = projectSource(manifest.Sources[sourceID])
			}
			for connectionID := range refs.connections {
				connections[connectionID] = projectConnection(manifest.Connections[connectionID])
			}
		}
		pins := make([]relationRevision, 0, len(connections))
		bindings := make(map[string]string)
		for connectionID := range connections {
			if revision, ok := revisions[connectionID]; ok {
				pins = append(pins, relationRevision{ConnectionID: connectionID, RevisionID: revision})
			}
			if kind := strings.TrimSpace(bindingKinds[connectionID]); kind != "" {
				bindings[connectionID] = kind
			}
		}
		sort.Slice(pins, func(i, j int) bool { return pins[i].ConnectionID < pins[j].ConnectionID })
		if len(bindings) == 0 {
			bindings = nil
		}
		encoded, err := json.Marshal(struct {
			Pins        []relationRevision `json:"pins"`
			Sources     map[string]any     `json:"sources"`
			Connections map[string]any     `json:"connections"`
			Bindings    map[string]string  `json:"bindings,omitempty"`
		}{Pins: pins, Sources: sources, Connections: connections, Bindings: bindings})
		if err != nil {
			return nil, fmt.Errorf("encode relation context %q: %w", modelID, err)
		}
		contexts[modelID] = string(encoded)
	}
	return contexts, nil
}

// RelationExecutionDigestsForInputs is the shared high-level API for exact
// per-relation execution identity. It prevents callers from reimplementing
// source, transitive model, revision, or connector selection.
func (p Project) RelationExecutionDigestsForInputs(revisions, bindingKinds map[string]string) (map[string]string, error) {
	contexts, err := p.RelationExecutionContexts(revisions, bindingKinds)
	if err != nil {
		return nil, err
	}
	return p.RelationExecutionDigestsByContext(contexts)
}

func (p Project) resultIdentityRelationExecutionDigestsForInputs(revisions, bindingKinds map[string]string) (map[string]string, error) {
	contexts, err := p.relationExecutionContexts(
		revisions, bindingKinds, resultIdentitySourceProjection, resultIdentityConnectionProjection,
	)
	if err != nil {
		return nil, err
	}
	return p.relationExecutionDigestsByContext(contexts, resultIdentityTableProjection, resultIdentityRelationDigest)
}

func resultIdentityRelationDigest(encoded []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(resultIdentityRelationDigestDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func resultIdentitySourceProjection(source semanticmodel.Source) any {
	fields := make(map[string]semanticmodel.SourceField, len(source.Fields))
	for name, field := range source.Fields {
		field.Description = ""
		fields[name] = field
	}
	schema := source.Schema
	schema.Columns = append([]semanticmodel.ColumnSchema(nil), schema.Columns...)
	for index := range schema.Columns {
		schema.Columns[index].Comment = ""
	}
	return struct {
		Format                string                               `json:"format"`
		Path                  string                               `json:"path"`
		Connection            string                               `json:"connection"`
		Object                string                               `json:"object"`
		LocationType          string                               `json:"locationType,omitempty"`
		Catalog               string                               `json:"catalog,omitempty"`
		SchemaName            string                               `json:"schemaName,omitempty"`
		RelationName          string                               `json:"relationName,omitempty"`
		PathLocation          any                                  `json:"pathLocation,omitempty"`
		EffectivePathLocation any                                  `json:"effectivePathLocation,omitempty"`
		SchemaMode            string                               `json:"schemaMode,omitempty"`
		Fields                map[string]semanticmodel.SourceField `json:"fields,omitempty"`
		Schema                semanticmodel.TableSchema            `json:"schema"`
	}{
		Format: source.Format, Path: source.Path, Connection: source.Connection, Object: source.Object,
		LocationType: source.LocationType, Catalog: source.Catalog, SchemaName: source.SchemaName,
		RelationName: source.RelationName, PathLocation: source.PathLocation,
		EffectivePathLocation: source.EffectivePathLocation, SchemaMode: source.SchemaMode,
		Fields: fields, Schema: schema,
	}
}

func resultIdentityConnectionProjection(connection semanticmodel.Connection) any {
	return struct {
		Kind           string                         `json:"kind"`
		Access         semanticmodel.ConnectionAccess `json:"access,omitempty"`
		Path           string                         `json:"path,omitempty"`
		Root           string                         `json:"root,omitempty"`
		Scope          string                         `json:"scope,omitempty"`
		ReaderDefaults any                            `json:"readerDefaults,omitempty"`
	}{
		Kind: connection.Kind, Access: connection.Access, Path: connection.Path,
		Root: connection.Root, Scope: connection.Scope, ReaderDefaults: connection.ReaderDefaults,
	}
}

func resultIdentityTableProjection(table semanticmodel.Table) any {
	type executionColumn struct {
		Field       string                        `json:"Field,omitempty"`
		Name        string                        `json:"Name,omitempty"`
		SourceField string                        `json:"SourceField,omitempty"`
		Type        string                        `json:"Type,omitempty"`
		Datatype    semanticmodel.LogicalDataType `json:"datatype,omitempty"`
	}
	type executionEntity struct {
		Type   string   `json:"Type,omitempty"`
		Fields []string `json:"Fields,omitempty"`
	}
	type executionDimension struct {
		Field    string                        `json:"Field,omitempty"`
		Table    string                        `json:"Table,omitempty"`
		Name     string                        `json:"Name,omitempty"`
		Type     string                        `json:"Type,omitempty"`
		Datatype semanticmodel.LogicalDataType `json:"datatype,omitempty"`
	}
	columns := make(map[string]executionColumn, len(table.Columns))
	for name, column := range table.Columns {
		columns[name] = executionColumn{
			Field: column.Field, Name: column.Name, SourceField: column.SourceField,
			Type: column.Type, Datatype: column.Datatype,
		}
	}
	entities := make(map[string]executionEntity, len(table.Entities))
	for name, entity := range table.Entities {
		entities[name] = executionEntity{Type: entity.Type, Fields: append([]string(nil), entity.Fields...)}
	}
	dimensions := make(map[string]executionDimension, len(table.Dimensions))
	for name, dimension := range table.Dimensions {
		dimensions[name] = executionDimension{
			Field: dimension.Field, Table: dimension.Table, Name: dimension.Name,
			Type: dimension.Type, Datatype: dimension.Datatype,
		}
	}
	schema := table.Schema
	schema.Columns = append([]semanticmodel.ColumnSchema(nil), schema.Columns...)
	for index := range schema.Columns {
		schema.Columns[index].Comment = ""
	}
	sources := append([]string(nil), table.SourceDependencies...)
	models := append([]string(nil), table.ModelDependencies...)
	sort.Strings(sources)
	sort.Strings(models)
	return struct {
		ModelName          string                            `json:"modelName,omitempty"`
		Execution          semanticmodel.ExecutionDefinition `json:"execution"`
		Columns            map[string]executionColumn        `json:"columns,omitempty"`
		Entities           map[string]executionEntity        `json:"entities,omitempty"`
		GrainEntity        string                            `json:"grainEntity,omitempty"`
		Dimensions         map[string]executionDimension     `json:"dimensions,omitempty"`
		Schema             semanticmodel.TableSchema         `json:"schema"`
		SourceDependencies []string                          `json:"sourceDependencies,omitempty"`
		ModelDependencies  []string                          `json:"modelDependencies,omitempty"`
	}{
		ModelName: table.ModelName, Execution: table.Execution, Columns: columns,
		Entities: entities, GrainEntity: table.GrainEntity, Dimensions: dimensions,
		Schema: schema, SourceDependencies: sources, ModelDependencies: models,
	}
}

type relationRevision struct {
	ConnectionID string `json:"connectionId"`
	RevisionID   string `json:"revisionId"`
}

// DatasetRelationEvidence maps one semantic dataset alias to the exact
// physical model relation identity already computed by the artifact contract.
type DatasetRelationEvidence struct {
	Dataset         string
	RelationID      projectgraph.ResourceID
	ExecutionDigest string
}

// SemanticModelRelationEvidence projects artifact-owned relation execution
// evidence into the aliases consumed by query planning. It is the only place
// that resolves authored Model names to graph.ResourceID values for result
// dependency derivation.
func (p Project) SemanticModelRelationEvidence(semanticModelID projectgraph.ResourceID, revisions, bindingKinds map[string]string) ([]DatasetRelationEvidence, error) {
	if err := semanticModelID.Validate(); err != nil {
		return nil, fmt.Errorf("semantic model ID: %w", err)
	}
	model := p.Manifest().SemanticModels[semanticModelID.String()]
	if model == nil {
		return nil, fmt.Errorf("semantic model %q is missing", semanticModelID)
	}
	activations, err := p.ConnectionActivations()
	if err != nil {
		return nil, err
	}
	for _, activation := range activations {
		kind := bindingKinds[activation.LogicalConnectionID]
		if kind == "" || kind != strings.TrimSpace(kind) {
			return nil, fmt.Errorf("connection %q has no canonical binding kind evidence", activation.LogicalConnectionID)
		}
		if activation.Mode != ManagedActivation {
			continue
		}
		revision := revisions[activation.LogicalConnectionID]
		if revision == "" || revision != strings.TrimSpace(revision) {
			return nil, fmt.Errorf("managed connection %q has no canonical revision evidence", activation.LogicalConnectionID)
		}
	}
	digests, err := p.resultIdentityRelationExecutionDigestsForInputs(revisions, bindingKinds)
	if err != nil {
		return nil, err
	}
	modelIDs := make(map[string]projectgraph.ResourceID)
	for _, resource := range p.Graph().Resources() {
		if resource.Kind != projectgraph.KindModel {
			continue
		}
		modelIDs[resource.ID.String()] = resource.ID
		modelIDs[resource.Name] = resource.ID
	}
	datasets := make([]string, 0, len(model.Datasets))
	for dataset := range model.Datasets {
		datasets = append(datasets, dataset)
	}
	sort.Strings(datasets)
	result := make([]DatasetRelationEvidence, 0, len(datasets))
	for _, dataset := range datasets {
		modelName := strings.TrimSpace(model.Datasets[dataset].Model)
		relationID := modelIDs[modelName]
		if relationID == "" {
			return nil, fmt.Errorf("semantic model %q dataset %q references unknown Model %q", semanticModelID, dataset, modelName)
		}
		digest := digests[relationID.String()]
		if digest == "" {
			return nil, fmt.Errorf("semantic model %q dataset %q has no relation execution digest", semanticModelID, dataset)
		}
		result = append(result, DatasetRelationEvidence{Dataset: dataset, RelationID: relationID, ExecutionDigest: digest})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("semantic model %q has no dataset relation evidence", semanticModelID)
	}
	return result, nil
}
