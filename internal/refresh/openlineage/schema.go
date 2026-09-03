package openlineage

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The schema resources in schema/ are immutable copies of the OpenLineage
// 1.52.0 release and the LeapView facet contracts.  Keep the release and
// peeled commit next to the checksums so a schema update is an intentional,
// reviewable change.
const (
	OpenLineageSchemaRelease = "1.52.0"
	OpenLineageSchemaCommit  = "cfd47d6f3e1b13167136b2508768c94a2351af23"
)

// SchemaArtifact records the source URL and bytes checksum of one embedded
// schema resource.  It is returned by PinnedOpenLineageSchemaProvenance as a
// copy, so callers cannot mutate package-owned provenance.
type SchemaArtifact struct {
	Name   string
	URL    string
	SHA256 string
}

var pinnedSchemaArtifacts = [...]SchemaArtifact{
	{Name: "OpenLineage.json", URL: SchemaURL, SHA256: "69f68bee00b9beac88a87059c0102410e7bb05f3f43c46d02a0409831eceb0d2"},
	{Name: "SchemaDatasetFacet.json", URL: SchemaFacetSchemaURL, SHA256: "50236a779aa64baa0bad0055391838bd22fcb36ce667c41d60ada80915e899b6"},
	{Name: "DatasetVersionDatasetFacet.json", URL: VersionFacetSchemaURL, SHA256: "91f57b67bc6d639bb6d600e26b827dac551e3520a233d1785d65870da263dc34"},
	{Name: "DataQualityAssertionsDatasetFacet.json", URL: QualityAssertionsSchemaURL, SHA256: "65c785790728ccf3f1fe326a0a064a91934b242807a015c2c3e99bde913abc56"},
	{Name: "DataQualityMetricsDatasetFacet.json", URL: QualityMetricsSchemaURL, SHA256: "0e2025096eb5317605a5d9dcd97d1d9d607e1af6d100e305de6299c88be8170a"},
	{Name: "InputStatisticsInputDatasetFacet.json", URL: InputStatisticsSchemaURL, SHA256: "39229ddd334bea16e35942e01cd59b7acb178d50688d4227894e01f39da8a669"},
	{Name: "OutputStatisticsOutputDatasetFacet.json", URL: OutputStatisticsSchemaURL, SHA256: "d89f01440d08e10bb6e4a95c437f736e70034058f97d4955a35e1cda70c9d37e"},
	{Name: "ColumnLineageDatasetFacet.json", URL: ColumnLineageSchemaURL, SHA256: "04d96b4985ca32470c7f9d73163da76678fb160bc8d80cbbc5abf312e9537466"},
	{Name: "NominalTimeRunFacet.json", URL: NominalTimeFacetSchemaURL, SHA256: "d9520e5731c7d1d25e1372730859b4efc986a9d4bd2315fe63c946957fe343b6"},
	{Name: "ParentRunFacet.json", URL: ParentRunFacetSchemaURL, SHA256: "efdd06ef87537849f1ec7984cc2e36cbdcea40083635c20fd9af1934ba601d57"},
	{Name: "LeapViewPipelineJobFacet.json", URL: PipelineFacetSchemaURL, SHA256: "1610bcf3c6446ca22fd756fbb66ee2a00825f5e9cc2aace92bcfa77436c23799"},
	{Name: "LeapViewInvocationRunFacet.json", URL: InvocationFacetSchemaURL, SHA256: "6b56bff2e4c334cd93803b00f1f2151f7b2798228acd528addc821b8c7905e81"},
}

// PinnedOpenLineageSchemaProvenance returns the immutable schema manifest.
func PinnedOpenLineageSchemaProvenance() []SchemaArtifact {
	return append([]SchemaArtifact(nil), pinnedSchemaArtifacts[:]...)
}

// schemaFiles deliberately has no URL loader.  Every resource needed by the
// selected schemas is registered from this package-owned embed FS before
// compilation, making validation deterministic and offline.
//
//go:embed schema/OpenLineage.json schema/facets/*.json
var schemaFiles embed.FS

type schemaDescriptor struct {
	key      string
	resource string
	location string
	url      string
}

const (
	datasetSchemaFacetKey      = "schema"
	datasetVersionFacetKey     = "version"
	qualityAssertionsFacetKey  = "dataQualityAssertions"
	qualityMetricsFacetKey     = "dataQualityMetrics"
	inputStatisticsFacetKey    = "inputStatistics"
	outputStatisticsFacetKey   = "outputStatistics"
	columnLineageFacetKey      = "columnLineage"
	nominalTimeFacetKey        = "nominalTime"
	parentRunFacetKey          = "parent"
	leapViewPipelineFacetKey   = PipelineFacetKey
	leapViewInvocationFacetKey = InvocationFacetKey
)

var standardSchemaDescriptors = [...]schemaDescriptor{
	{key: datasetSchemaFacetKey, resource: "SchemaDatasetFacet.json", location: SchemaFacetSchemaURL + "#/$defs/SchemaDatasetFacet", url: SchemaFacetSchemaURL},
	{key: datasetVersionFacetKey, resource: "DatasetVersionDatasetFacet.json", location: VersionFacetSchemaURL + "#/$defs/DatasetVersionDatasetFacet", url: VersionFacetSchemaURL},
	{key: qualityAssertionsFacetKey, resource: "DataQualityAssertionsDatasetFacet.json", location: QualityAssertionsSchemaURL + "#/$defs/DataQualityAssertionsDatasetFacet", url: QualityAssertionsSchemaURL},
	{key: qualityMetricsFacetKey, resource: "DataQualityMetricsDatasetFacet.json", location: QualityMetricsSchemaURL + "#/$defs/DataQualityMetricsDatasetFacet", url: QualityMetricsSchemaURL},
	{key: inputStatisticsFacetKey, resource: "InputStatisticsInputDatasetFacet.json", location: InputStatisticsSchemaURL + "#/$defs/InputStatisticsInputDatasetFacet", url: InputStatisticsSchemaURL},
	{key: outputStatisticsFacetKey, resource: "OutputStatisticsOutputDatasetFacet.json", location: OutputStatisticsSchemaURL + "#/$defs/OutputStatisticsOutputDatasetFacet", url: OutputStatisticsSchemaURL},
	{key: columnLineageFacetKey, resource: "ColumnLineageDatasetFacet.json", location: ColumnLineageSchemaURL + "#/$defs/ColumnLineageDatasetFacet", url: ColumnLineageSchemaURL},
	{key: nominalTimeFacetKey, resource: "NominalTimeRunFacet.json", location: NominalTimeFacetSchemaURL + "#/$defs/NominalTimeRunFacet", url: NominalTimeFacetSchemaURL},
	{key: parentRunFacetKey, resource: "ParentRunFacet.json", location: ParentRunFacetSchemaURL + "#/$defs/ParentRunFacet", url: ParentRunFacetSchemaURL},
}

var customSchemaDescriptors = [...]schemaDescriptor{
	{key: leapViewPipelineFacetKey, resource: "LeapViewPipelineJobFacet.json", location: PipelineFacetSchemaURL, url: PipelineFacetSchemaURL},
	{key: leapViewInvocationFacetKey, resource: "LeapViewInvocationRunFacet.json", location: InvocationFacetSchemaURL, url: InvocationFacetSchemaURL},
}

type schemaSet struct {
	event    *jsonschema.Schema
	standard map[string]*jsonschema.Schema
	custom   map[string]*jsonschema.Schema
}

var (
	schemaSetOnce sync.Once
	schemas       *schemaSet
	schemaErr     error
)

func pinnedSchemas() (*schemaSet, error) {
	schemaSetOnce.Do(func() {
		schemas, schemaErr = compilePinnedSchemas()
	})
	return schemas, schemaErr
}

func compilePinnedSchemas() (*schemaSet, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()

	if err := addEmbeddedResource(compiler, "schema/OpenLineage.json", SchemaURL); err != nil {
		return nil, err
	}
	for _, descriptor := range standardSchemaDescriptors {
		if err := addEmbeddedResource(compiler, "schema/facets/"+descriptor.resource, descriptor.url); err != nil {
			return nil, err
		}
	}
	for _, descriptor := range customSchemaDescriptors {
		if err := addEmbeddedResource(compiler, "schema/facets/"+descriptor.resource, descriptor.url); err != nil {
			return nil, err
		}
	}

	event, err := compiler.Compile(SchemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile OpenLineage event schema: %w", err)
	}
	set := &schemaSet{event: event, standard: make(map[string]*jsonschema.Schema, len(standardSchemaDescriptors)), custom: make(map[string]*jsonschema.Schema, len(customSchemaDescriptors))}
	for _, descriptor := range standardSchemaDescriptors {
		compiled, err := compiler.Compile(descriptor.location)
		if err != nil {
			return nil, fmt.Errorf("compile %s schema: %w", descriptor.key, err)
		}
		set.standard[descriptor.key] = compiled
	}
	for _, descriptor := range customSchemaDescriptors {
		compiled, err := compiler.Compile(descriptor.location)
		if err != nil {
			return nil, fmt.Errorf("compile %s schema: %w", descriptor.key, err)
		}
		set.custom[descriptor.key] = compiled
	}
	return set, nil
}

func addEmbeddedResource(compiler *jsonschema.Compiler, path, resourceURL string) error {
	raw, err := schemaFiles.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read embedded schema %s: %w", path, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode embedded schema %s: %w", path, err)
	}
	if err := compiler.AddResource(resourceURL, doc); err != nil {
		return fmt.Errorf("register embedded schema %s: %w", path, err)
	}
	return nil
}

// ValidateEvent validates the JSON representation of a complete OpenLineage
// event, including all selected standard and LeapView-owned facets.
func ValidateEvent(event Event) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal OpenLineage event: %w", err)
	}
	return ValidateJSON(raw)
}

// ValidateJSON validates one complete OpenLineage event.  It never fetches
// schemas at runtime and accepts no trailing JSON values.
func ValidateJSON(raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode OpenLineage event JSON: %w", err)
	}
	set, err := pinnedSchemas()
	if err != nil {
		return err
	}
	if err := set.event.Validate(doc); err != nil {
		return fmt.Errorf("OpenLineage event schema validation failed: %w", err)
	}
	return validateEventFacets(set, doc)
}

func validateEventFacets(set *schemaSet, doc any) error {
	event, ok := doc.(map[string]any)
	if !ok {
		return fmt.Errorf("OpenLineage event must be an object")
	}
	if schemaURL, ok := event["schemaURL"].(string); !ok || schemaURL != SchemaURL {
		return fmt.Errorf("OpenLineage schemaURL drift: got %q, want %q", stringValue(event["schemaURL"]), SchemaURL)
	}

	if run, ok := objectValue(event["run"]); ok {
		if err := validateFacetContainer(set, run, "run.facets"); err != nil {
			return err
		}
	}
	if job, ok := objectValue(event["job"]); ok {
		if err := validateFacetContainer(set, job, "job.facets"); err != nil {
			return err
		}
	}
	for i, item := range arrayValue(event["inputs"]) {
		dataset, ok := objectValue(item)
		if !ok {
			continue
		}
		if err := validateFacetContainer(set, dataset, fmt.Sprintf("inputs[%d].facets", i)); err != nil {
			return err
		}
		if err := validateFacetContainer(set, dataset, fmt.Sprintf("inputs[%d].inputFacets", i)); err != nil {
			return err
		}
	}
	for i, item := range arrayValue(event["outputs"]) {
		dataset, ok := objectValue(item)
		if !ok {
			continue
		}
		if err := validateFacetContainer(set, dataset, fmt.Sprintf("outputs[%d].facets", i)); err != nil {
			return err
		}
		if err := validateFacetContainer(set, dataset, fmt.Sprintf("outputs[%d].outputFacets", i)); err != nil {
			return err
		}
	}
	return nil
}

func validateFacetContainer(set *schemaSet, parent map[string]any, path string) error {
	// The caller passes a complete path for diagnostics.  Facet field names
	// are selected from the suffix to keep traversal independent of route.
	field := path[strings.LastIndexByte(path, '.')+1:]
	value, exists := parent[field]
	if !exists || value == nil {
		return nil
	}
	facets, ok := value.(map[string]any)
	if !ok {
		return nil // the core schema has already reported the type error
	}
	keys := make([]string, 0, len(facets))
	for key := range facets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		facet := facets[key]
		if descriptor, known := standardDescriptor(key); known {
			if err := validateFacetURL(facet, descriptor.url, path, key); err != nil {
				return err
			}
			if err := set.standard[key].Validate(facet); err != nil {
				return fmt.Errorf("%s.%s schema validation failed: %w", path, key, err)
			}
			continue
		}
		if strings.HasPrefix(key, "leapView_") {
			descriptor, known := customDescriptor(key)
			if !known {
				return fmt.Errorf("unknown LeapView facet %q at %s", key, path)
			}
			if err := validateFacetURL(facet, descriptor.url, path, key); err != nil {
				return err
			}
			if err := validateCustomFacetFields(facet, key, path); err != nil {
				return err
			}
			if err := set.custom[key].Validate(facet); err != nil {
				return fmt.Errorf("%s.%s schema validation failed: %w", path, key, err)
			}
		}
	}
	return nil
}

var customFacetFields = map[string]map[string]struct{}{
	PipelineFacetKey: {
		"_deleted": {}, "_producer": {}, "_schemaURL": {}, "pipelineId": {},
		"projectId": {}, "environment": {}, "semanticModelId": {}, "generationId": {},
		"planDigest": {}, "selectionDigest": {}, "executionDigest": {},
		"provenanceDigest": {}, "governanceDigest": {}, "evidenceDigest": {},
		"qualificationChecks": {},
	},
	InvocationFacetKey: {
		"_deleted": {}, "_producer": {}, "_schemaURL": {}, "leapViewRunId": {},
		"invocationSource": {}, "matchingScheduleIds": {}, "startingDeadlineSeconds": {},
		"concurrencyPolicy": {},
	},
}

func validateCustomFacetFields(value any, key, path string) error {
	facet, ok := objectValue(value)
	if !ok {
		return nil // the schema validator emits the type error
	}
	allowed := customFacetFields[key]
	keys := make([]string, 0, len(facet))
	for field := range facet {
		keys = append(keys, field)
	}
	sort.Strings(keys)
	for _, field := range keys {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown field %q in LeapView facet %q at %s", field, key, path)
		}
	}
	return nil
}

func standardDescriptor(key string) (schemaDescriptor, bool) {
	for _, descriptor := range standardSchemaDescriptors {
		if descriptor.key == key {
			return descriptor, true
		}
	}
	return schemaDescriptor{}, false
}

func customDescriptor(key string) (schemaDescriptor, bool) {
	for _, descriptor := range customSchemaDescriptors {
		if descriptor.key == key {
			return descriptor, true
		}
	}
	return schemaDescriptor{}, false
}

func validateFacetURL(value any, want, path, key string) error {
	facet, ok := objectValue(value)
	if !ok {
		return nil // the schema validator emits the type error
	}
	got, ok := facet["_schemaURL"].(string)
	if !ok || got != want {
		return fmt.Errorf("%s.%s schema URL drift: got %q, want %q", path, key, stringValue(facet["_schemaURL"]), want)
	}
	return nil
}

func objectValue(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func arrayValue(value any) []any {
	array, _ := value.([]any)
	return array
}

func stringValue(value any) string {
	stringValue, _ := value.(string)
	return stringValue
}
