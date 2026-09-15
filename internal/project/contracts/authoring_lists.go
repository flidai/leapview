package contracts

import (
	"encoding/json"

	"github.com/flidai/leapview/internal/platform/authoringlists"
)

var sourceCollections = []authoringlists.Collection{{Path: "fields", Identity: "name"}}
var modelCollections = []authoringlists.Collection{
	{Path: "entities", Identity: "name"}, {Path: "fields", Identity: "name"},
}
var semanticCollections = []authoringlists.Collection{
	{Path: "datasets", Identity: "name"},
	{Path: "datasets.*.dimensions", Identity: "name"},
	{Path: "datasets.*.metrics", Identity: "name"},
	{Path: "accessGrants", Identity: "name"},
	{Path: "relationships", Identity: "name"},
	{Path: "dimensions", Identity: "name"},
	{Path: "dimensions.*.bindings", Identity: "dataset"},
	{Path: "filters", Identity: "name", Wrapper: "definition"},
	{Path: "metrics", Identity: "name"},
}

// IndexedAuthoringJSON exposes the compiler's keyed view to contract
// projection code. Public source JSON and generated schemas remain list-based.
func IndexedAuthoringJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var collections []authoringlists.Collection
	switch value.(type) {
	case Source:
		collections = sourceCollections
	case Model:
		collections = modelCollections
	case SemanticModel:
		collections = semanticCollections
	default:
		return encoded, nil
	}
	return authoringlists.Rewrite(encoded, resourceCollections(collections), false)
}

// NamedSemanticAuthoringJSON converts an indexed native SemanticModel export
// into the canonical authored list shape before YAML encoding.
func NamedSemanticAuthoringJSON(indexed []byte) ([]byte, error) {
	return authoringlists.Rewrite(indexed, resourceCollections(semanticCollections), true)
}

func resourceCollections(collections []authoringlists.Collection) []authoringlists.Collection {
	prefixed := make([]authoringlists.Collection, len(collections))
	for index, collection := range collections {
		collection.Path = "spec." + collection.Path
		prefixed[index] = collection
	}
	return prefixed
}

type sourceSpecIndexed SourceSpec
type modelSpecIndexed ModelSpec
type semanticModelSpecIndexed SemanticModelSpec

func (value SourceSpec) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(sourceSpecIndexed(value))
	if err != nil {
		return nil, err
	}
	return authoringlists.Rewrite(raw, sourceCollections, true)
}
func (value *SourceSpec) UnmarshalJSON(raw []byte) error {
	indexed, err := authoringlists.Rewrite(raw, sourceCollections, false)
	if err != nil {
		return err
	}
	return json.Unmarshal(indexed, (*sourceSpecIndexed)(value))
}
func (value ModelSpec) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(modelSpecIndexed(value))
	if err != nil {
		return nil, err
	}
	return authoringlists.Rewrite(raw, modelCollections, true)
}
func (value *ModelSpec) UnmarshalJSON(raw []byte) error {
	indexed, err := authoringlists.Rewrite(raw, modelCollections, false)
	if err != nil {
		return err
	}
	return json.Unmarshal(indexed, (*modelSpecIndexed)(value))
}
func (value SemanticModelSpec) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(semanticModelSpecIndexed(value))
	if err != nil {
		return nil, err
	}
	return authoringlists.Rewrite(raw, semanticCollections, true)
}
func (value *SemanticModelSpec) UnmarshalJSON(raw []byte) error {
	indexed, err := authoringlists.Rewrite(raw, semanticCollections, false)
	if err != nil {
		return err
	}
	return json.Unmarshal(indexed, (*semanticModelSpecIndexed)(value))
}
