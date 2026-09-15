package contractprojection

import (
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ReferenceContext binds authored symbolic references to the explicit IDs in
// one validated project graph. IDs are looked up first, matching the compiler
// boundary; names are only a project-local fallback and are never converted
// into IDs by hashing, path inference, or kind prefixes.
//
// The maps are private and the graph package returns defensive resource
// copies, so a context cannot be changed through either input or output
// values after construction.
type ReferenceContext struct {
	byID            map[projectgraph.ResourceID]projectgraph.Resource
	byName          map[string]projectgraph.Resource
	modelFieldTypes map[projectgraph.ResourceID]map[string]string
}

// NewReferenceContext builds a resolver from a validated ProjectGraph. The
// graph is revalidated at this boundary so a zero or hand-built graph cannot
// silently become publication authority if the graph implementation changes
// its construction guarantees later.
func NewReferenceContext(graph projectgraph.ProjectGraph) (ReferenceContext, error) {
	resources := graph.Resources()
	if err := projectgraph.Validate(resources, graph.Edges()); err != nil {
		return ReferenceContext{}, fmt.Errorf("reference context: graph is not validated: %w", err)
	}
	context := ReferenceContext{
		byID:   make(map[projectgraph.ResourceID]projectgraph.Resource, len(resources)),
		byName: make(map[string]projectgraph.Resource, len(resources)),
	}
	for _, resource := range resources {
		if _, exists := context.byID[resource.ID]; exists {
			return ReferenceContext{}, fmt.Errorf("reference context: duplicate resource id %q", resource.ID)
		}
		if _, exists := context.byName[resource.Name]; exists {
			return ReferenceContext{}, fmt.Errorf("reference context: ambiguous resource name %q", resource.Name)
		}
		context.byID[resource.ID] = resource
		context.byName[resource.Name] = resource
	}
	return context, nil
}

// WithModelFieldTypes adds resolved logical field types for semantic contract
// projection. These come from the validated physical Models after discovery;
// the returned context is detached from both the caller and the original.
func (context ReferenceContext) WithModelFieldTypes(reference string, fields map[string]string) (ReferenceContext, error) {
	id, err := context.ResolveReference(reference, projectgraph.KindModel)
	if err != nil {
		return ReferenceContext{}, err
	}
	clone := context
	clone.modelFieldTypes = make(map[projectgraph.ResourceID]map[string]string, len(context.modelFieldTypes)+1)
	for modelID, values := range context.modelFieldTypes {
		copyValues := make(map[string]string, len(values))
		for name, datatype := range values {
			copyValues[name] = datatype
		}
		clone.modelFieldTypes[modelID] = copyValues
	}
	copyFields := make(map[string]string, len(fields))
	for name, datatype := range fields {
		if !validProjectionIdentifier(name) || !validProjectionDatatypeName(datatype) {
			return ReferenceContext{}, fmt.Errorf("model %q has invalid field type for %q", reference, name)
		}
		copyFields[name] = datatype
	}
	clone.modelFieldTypes[id] = copyFields
	return clone, nil
}

// ResolveReference resolves one authored reference and verifies its expected
// graph kind. The method intentionally accepts both explicit IDs and
// symbolic names because authored contracts permit either; its result is
// always the explicit stable ResourceID.
func (context ReferenceContext) ResolveReference(reference string, expected projectgraph.Kind) (projectgraph.ResourceID, error) {
	reference = strings.TrimSpace(reference)
	resource, ok := context.byID[projectgraph.ResourceID(reference)]
	if !ok {
		resource, ok = context.byName[reference]
	}
	if !ok {
		return "", fmt.Errorf("reference %q is missing", reference)
	}
	if expected != "" && resource.Kind != expected {
		return "", fmt.Errorf("reference %q resolves to %s, want %s", reference, resource.Kind, expected)
	}
	return resource.ID, nil
}
