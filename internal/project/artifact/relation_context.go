package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/sourcedataidentity"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

const resultIdentityRelationDigestDomain = "flid.resultidentity.relation.v1"

type relationReferenceSet struct {
	sources     map[string]struct{}
	connections map[string]struct{}
	unknown     bool
	incomplete  bool
}

type relationReferenceProjection uint8

const (
	legacyRelationReferences relationReferenceProjection = iota
	resultIdentityRelationReferences
)

// relationReferences is the single artifact-owned lineage implementation with
// two explicit projections. The legacy projection preserves the historical
// deployment/materialization contract based on SourceDependencies. Result
// identity additionally consumes direct-source and persisted SQL-analysis
// evidence so missing lineage cannot authorize result reuse.
func (p Project) relationReferences(projection relationReferenceProjection) (map[string]relationReferenceSet, error) {
	manifest := p.Manifest()
	modelNames := make(map[string]string)
	sourceNames := make(map[string]string)
	connectionNames := make(map[string]string)
	allSources := make(map[string]struct{}, len(manifest.Sources))
	allConnections := make(map[string]struct{}, len(manifest.Connections))
	for _, resource := range p.Graph().Resources() {
		switch resource.Kind {
		case projectgraph.KindModel:
			modelNames[resource.ID.String()] = resource.ID.String()
			modelNames[resource.Name] = resource.ID.String()
		case projectgraph.KindSource:
			sourceNames[resource.ID.String()] = resource.ID.String()
			sourceNames[resource.Name] = resource.ID.String()
			if projection == resultIdentityRelationReferences {
				addRelationReferenceAlias(sourceNames, projectmanifest.RuntimeSourceAlias(resource.Name), resource.ID.String())
			}
			allSources[resource.ID.String()] = struct{}{}
		case projectgraph.KindConnection:
			connectionNames[resource.ID.String()] = resource.ID.String()
			connectionNames[resource.Name] = resource.ID.String()
			allConnections[resource.ID.String()] = struct{}{}
		}
	}

	var collect func(string, map[string]bool) (relationReferenceSet, error)
	collect = func(modelID string, visiting map[string]bool) (relationReferenceSet, error) {
		if visiting[modelID] {
			return relationReferenceSet{}, fmt.Errorf("relation context dependency cycle at %q", modelID)
		}
		table, ok := manifest.Models[modelID]
		if !ok {
			return relationReferenceSet{}, fmt.Errorf("relation context model %q is missing", modelID)
		}
		visiting[modelID] = true
		refs := relationReferenceSet{sources: make(map[string]struct{}), connections: make(map[string]struct{})}
		sourceDependencies := table.SourceDependencies
		modelDependencies := table.ModelDependencies
		primarySource := ""
		if projection == resultIdentityRelationReferences {
			primarySource = table.Execution.Source
			refs.incomplete = !completePersistedSQLLineage(table, sourceNames, modelNames)
		}
		for _, reference := range uniqueManifestReferences(primarySource, sourceDependencies, nil) {
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
		for _, dependency := range modelDependencies {
			dependencyID := modelNames[strings.TrimSpace(dependency)]
			if dependencyID == "" {
				refs.unknown = true
				continue
			}
			dependencyRefs, err := collect(dependencyID, visiting)
			if err != nil {
				return relationReferenceSet{}, err
			}
			for source := range dependencyRefs.sources {
				refs.sources[source] = struct{}{}
			}
			for connection := range dependencyRefs.connections {
				refs.connections[connection] = struct{}{}
			}
			refs.unknown = refs.unknown || dependencyRefs.unknown
			refs.incomplete = refs.incomplete || dependencyRefs.incomplete
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

	result := make(map[string]relationReferenceSet, len(manifest.Models))
	for modelID := range manifest.Models {
		refs, err := collect(modelID, map[string]bool{})
		if err != nil {
			return nil, err
		}
		if refs.unknown {
			refs.sources = cloneStringSet(allSources)
			refs.connections = cloneStringSet(allConnections)
		}
		result[modelID] = refs
	}
	return result, nil
}

func addRelationReferenceAlias(aliases map[string]string, alias, id string) {
	if alias == "" {
		return
	}
	if existing, ok := aliases[alias]; ok && existing != id {
		aliases[alias] = ""
		return
	}
	aliases[alias] = id
}

func completePersistedSQLLineage(table semanticmodel.Table, sourceNames, modelNames map[string]string) bool {
	if strings.TrimSpace(table.Execution.SQL) == "" {
		return true
	}
	evidence := table.SQLAnalysisEvidence
	if evidence == nil || !evidence.Validated || strings.TrimSpace(table.Execution.Source) != "" {
		return false
	}
	evidenceSources, sourcesOK := canonicalRelationReferences(evidence.SourceRefs, sourceNames)
	evidenceModels, modelsOK := canonicalRelationReferences(evidence.ModelRefs, modelNames)
	dependencySources, sourceDependenciesOK := canonicalRelationReferences(table.SourceDependencies, sourceNames)
	dependencyModels, modelDependenciesOK := canonicalRelationReferences(table.ModelDependencies, modelNames)
	if !sourcesOK || !modelsOK || !sourceDependenciesOK || !modelDependenciesOK || len(evidenceSources)+len(evidenceModels) == 0 {
		return false
	}
	return equalRelationReferenceSets(evidenceSources, dependencySources) && equalRelationReferenceSets(evidenceModels, dependencyModels)
}

func canonicalRelationReferences(values []string, aliases map[string]string) (map[string]struct{}, bool) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		id := aliases[strings.TrimSpace(value)]
		if id == "" {
			return nil, false
		}
		result[id] = struct{}{}
	}
	return result, true
}

func equalRelationReferenceSets(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, ok := right[value]; !ok {
			return false
		}
	}
	return true
}

func cloneStringSet(values map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for value := range values {
		result[value] = struct{}{}
	}
	return result
}

// RelationExecutionContexts derives the target-bound, relation-scoped context
// consumed by RelationExecutionDigestsByContext. Managed revisions and
// connector kinds are selected only for sources reachable from each physical
// model relation. Unknown lineage fails safe by including all source and
// connection evidence.
func (p Project) RelationExecutionContexts(revisions, bindingKinds map[string]string) (map[string]string, error) {
	return p.relationExecutionContexts(revisions, bindingKinds, nil, legacyRelationReferences,
		func(value semanticmodel.Source) any { return value },
		func(value semanticmodel.Connection) any { return value },
	)
}

func (p Project) relationExecutionContexts(
	revisions, bindingKinds map[string]string,
	sourceDataEvidence map[projectgraph.ResourceID]sourcedataidentity.Evidence,
	referenceProjection relationReferenceProjection,
	projectSource func(semanticmodel.Source) any,
	projectConnection func(semanticmodel.Connection) any,
) (map[string]string, error) {
	manifest := p.Manifest()
	references, err := p.relationReferences(referenceProjection)
	if err != nil {
		return nil, err
	}

	contexts := make(map[string]string, len(manifest.Models))
	for modelID := range manifest.Models {
		refs := references[modelID]
		sources, connections := make(map[string]any), make(map[string]any)
		for sourceID := range refs.sources {
			sources[sourceID] = projectSource(manifest.Sources[sourceID])
		}
		for connectionID := range refs.connections {
			connections[connectionID] = projectConnection(manifest.Connections[connectionID])
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
		var sourceIdentity []relationSourceDataEvidence
		for sourceID := range refs.sources {
			resourceID := projectgraph.ResourceID(sourceID)
			evidence, ok := sourceDataEvidence[resourceID]
			if !ok || !evidence.Available() || evidence.SourceID() != resourceID {
				continue
			}
			sourceIdentity = append(sourceIdentity, relationSourceDataEvidence{
				SourceID: sourceID, EquivalenceDigest: evidence.EquivalenceDigest(),
			})
		}
		sort.Slice(sourceIdentity, func(i, j int) bool { return sourceIdentity[i].SourceID < sourceIdentity[j].SourceID })
		if len(bindings) == 0 {
			bindings = nil
		}
		encoded, err := json.Marshal(struct {
			Pins               []relationRevision           `json:"pins"`
			SourceDataEvidence []relationSourceDataEvidence `json:"sourceDataEvidence,omitempty"`
			Sources            map[string]any               `json:"sources"`
			Connections        map[string]any               `json:"connections"`
			Bindings           map[string]string            `json:"bindings,omitempty"`
		}{
			Pins: pins, SourceDataEvidence: sourceIdentity, Sources: sources,
			Connections: connections, Bindings: bindings,
		})
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

func (p Project) resultIdentityRelationExecutionDigestsForInputs(sourceDataEvidence map[projectgraph.ResourceID]sourcedataidentity.Evidence, bindingKinds map[string]string) (map[string]string, error) {
	contexts, err := p.relationExecutionContexts(
		nil, bindingKinds, sourceDataEvidence, resultIdentityRelationReferences,
		resultIdentitySourceProjection, resultIdentityConnectionProjection,
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

type relationSourceDataEvidence struct {
	SourceID          string `json:"sourceId"`
	EquivalenceDigest string `json:"equivalenceDigest"`
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
// dependency derivation. Datasets whose complete source-data or connector
// evidence is unavailable are omitted; consumers must treat that absence as a
// cache-reuse bypass while allowing normal execution to proceed.
func (p Project) SemanticModelRelationEvidence(semanticModelID projectgraph.ResourceID, sourceDataEvidence map[projectgraph.ResourceID]sourcedataidentity.Evidence, bindingKinds map[string]string) ([]DatasetRelationEvidence, error) {
	if err := semanticModelID.Validate(); err != nil {
		return nil, fmt.Errorf("semantic model ID: %w", err)
	}
	model := p.Manifest().SemanticModels[semanticModelID.String()]
	if model == nil {
		return nil, fmt.Errorf("semantic model %q is missing", semanticModelID)
	}
	references, err := p.relationReferences(resultIdentityRelationReferences)
	if err != nil {
		return nil, err
	}
	digests, err := p.resultIdentityRelationExecutionDigestsForInputs(sourceDataEvidence, bindingKinds)
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
		refs, ok := references[relationID.String()]
		if !ok {
			return nil, fmt.Errorf("semantic model %q dataset %q relation %q has no lineage projection", semanticModelID, dataset, relationID)
		}
		if !completeRelationSourceDataEvidence(refs, sourceDataEvidence, bindingKinds) {
			continue
		}
		digest := digests[relationID.String()]
		if digest == "" {
			return nil, fmt.Errorf("semantic model %q dataset %q has no relation execution digest", semanticModelID, dataset)
		}
		result = append(result, DatasetRelationEvidence{Dataset: dataset, RelationID: relationID, ExecutionDigest: digest})
	}
	return result, nil
}

func completeRelationSourceDataEvidence(refs relationReferenceSet, sourceDataEvidence map[projectgraph.ResourceID]sourcedataidentity.Evidence, bindingKinds map[string]string) bool {
	if refs.incomplete || len(refs.sources) == 0 || len(refs.connections) == 0 {
		return false
	}
	for connectionID := range refs.connections {
		kind := bindingKinds[connectionID]
		if kind == "" || kind != strings.TrimSpace(kind) {
			return false
		}
	}
	for sourceID := range refs.sources {
		resourceID := projectgraph.ResourceID(sourceID)
		evidence, ok := sourceDataEvidence[resourceID]
		if !ok || !evidence.Available() || evidence.SourceID() != resourceID {
			return false
		}
	}
	return true
}
