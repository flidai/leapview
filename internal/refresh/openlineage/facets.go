package openlineage

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	catalogstats "github.com/flidai/leapview/internal/analytics/catalogstats"
	planir "github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractpublication"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

// These aliases keep the refresh exporter at the existing evidence
// boundaries. They intentionally do not introduce an OpenLineage identity,
// digest, or canonicalization authority.
type ContractPublication = contractpublication.ContractPublication
type GateEvidence = release.GateEvidence
type DatasetStatistics = catalogstats.Table
type PhysicalLineage = planir.PhysicalLineage

type datasetDirection uint8

const (
	datasetInput datasetDirection = iota + 1
	datasetOutput
)

type datasetProjector struct {
	namespace    string
	publications map[string]publicationSchema
	statistics   map[string]DatasetStatistics
	lineage      map[string][]PhysicalLineage
	quality      map[string]datasetQuality
}

type publicationSchema struct {
	version string
	fields  []schemaField
	checks  map[string]publishedCheck
}

type schemaField struct {
	name     string
	typeName string
}

type publishedCheck struct {
	id       string
	typeName string
	field    string
	fields   []string
	values   []string
	to       string
	minimum  *int64
	maximum  *int64
	severity string
}

type datasetQuality struct {
	checks []qualityCheck
}

type qualityCheck struct {
	evidence release.GateCheckEvidence
	authored publishedCheck
}

// newDatasetProjector prepares the immutable evidence used by one event. It
// receives both directions so evidence can be checked against the complete
// event scope before any dataset is emitted.
func newDatasetProjector(p Pipeline, r PipelineRun, namespace string, inputs, outputs []string) (*datasetProjector, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil, fmt.Errorf("OpenLineage dataset namespace is required")
	}

	scope := make(map[string]struct{})
	addScope := func(names []string) {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" {
				scope[name] = struct{}{}
			}
		}
	}
	// A child run may select a subset of its parent's scope. Include the
	// pipeline scope when checking evidence so valid parent publications and
	// gate records remain usable by that child event.
	addScope(p.SourceInputs)
	addScope(p.MaterializationScope)
	addScope(inputs)
	addScope(outputs)

	projector := &datasetProjector{
		namespace:    namespace,
		publications: make(map[string]publicationSchema, len(p.ContractPublications)),
		statistics:   make(map[string]DatasetStatistics, len(r.Statistics)),
		lineage:      make(map[string][]PhysicalLineage, len(r.ColumnLineage)),
		quality:      make(map[string]datasetQuality),
	}
	eventInputs := datasetNameSet(inputs)

	for _, publication := range p.ContractPublications {
		id := publication.AuthoredID.String()
		if id == "" {
			return nil, fmt.Errorf("contract publication has empty authored id")
		}
		if _, exists := scope[id]; !exists {
			return nil, fmt.Errorf("contract publication %q is outside pipeline dataset scope", id)
		}
		if _, exists := projector.publications[id]; exists {
			return nil, fmt.Errorf("duplicate contract publication %q", id)
		}
		decoded, err := decodeContractPublication(publication)
		if err != nil {
			return nil, err
		}
		projector.publications[id] = decoded
	}

	for rawName, stats := range r.Statistics {
		name := strings.TrimSpace(rawName)
		if rawName != name {
			return nil, fmt.Errorf("dataset statistics name %q is not canonical", rawName)
		}
		if name == "" {
			return nil, fmt.Errorf("dataset statistics has empty dataset name")
		}
		if _, exists := scope[name]; !exists {
			return nil, fmt.Errorf("dataset statistics %q is outside pipeline dataset scope", name)
		}
		if err := validateDatasetStatistics(stats); err != nil {
			return nil, fmt.Errorf("dataset statistics %q: %w", name, err)
		}
		if _, exists := projector.statistics[name]; exists {
			// A Go map cannot contain duplicate keys, but keeping this guard
			// makes the invariant explicit if the source representation changes.
			return nil, fmt.Errorf("duplicate dataset statistics %q", name)
		}
		projector.statistics[name] = stats
	}

	for rawName, entries := range r.ColumnLineage {
		name := strings.TrimSpace(rawName)
		if rawName != name {
			return nil, fmt.Errorf("column lineage name %q is not canonical", rawName)
		}
		if name == "" {
			return nil, fmt.Errorf("column lineage has empty dataset name")
		}
		if _, exists := scope[name]; !exists {
			return nil, fmt.Errorf("column lineage %q is outside pipeline dataset scope", name)
		}
		if !containsName(outputs, name) {
			return nil, fmt.Errorf("column lineage %q is not an output dataset", name)
		}
		canonical, err := canonicalLineage(name, entries, eventInputs, projector.publications)
		if err != nil {
			return nil, fmt.Errorf("column lineage %q: %w", name, err)
		}
		if len(canonical) > 0 {
			projector.lineage[name] = canonical
		}
	}

	if r.GateEvidence != nil {
		canonical, err := r.GateEvidence.Canonical()
		if err != nil {
			return nil, fmt.Errorf("gate evidence: %w", err)
		}
		if r.GateEvidence.Digest != "" && r.GateEvidence.Digest != canonical.Digest {
			return nil, fmt.Errorf("gate evidence digest mismatch")
		}
		for _, source := range canonical.Sources {
			if _, exists := scope[source.ID]; !exists {
				return nil, fmt.Errorf("gate source evidence %q is outside pipeline dataset scope", source.ID)
			}
		}
		for _, check := range canonical.Checks {
			if _, exists := scope[check.ResourceID]; !exists {
				return nil, fmt.Errorf("gate check evidence %q is outside pipeline dataset scope", check.Identity)
			}
			if !containsName(outputs, check.ResourceID) {
				return nil, fmt.Errorf("gate check evidence %q is not an output dataset", check.Identity)
			}
			value := projector.quality[check.ResourceID]
			switch check.Outcome {
			case release.GateSuccess, release.GateWarning, release.GateBlocking:
			default:
				return nil, fmt.Errorf("gate check evidence %q cannot be represented as an OpenLineage assertion", check.Identity)
			}
			const marker = "\x00authored\x00"
			prefix := check.ResourceID + marker
			if !strings.HasPrefix(check.Identity, prefix) || len(check.Identity) == len(prefix) {
				return nil, fmt.Errorf("gate check evidence %q has no stable authored identity", check.Identity)
			}
			id := strings.TrimPrefix(check.Identity, prefix)
			publication, exists := projector.publications[check.ResourceID]
			if !exists {
				return nil, fmt.Errorf("gate check evidence %q has no published contract", check.Identity)
			}
			authored, exists := publication.checks[id]
			if !exists || authored.typeName != check.Kind || authored.severity != check.Severity {
				return nil, fmt.Errorf("gate check evidence %q disagrees with published contract", check.Identity)
			}
			projected := qualityCheck{evidence: check, authored: authored}
			value.checks = append(value.checks, projected)
			projector.quality[check.ResourceID] = value
		}
	}

	return projector, nil
}

func (p *datasetProjector) project(names []string, direction datasetDirection) ([]Dataset, error) {
	if direction != datasetInput && direction != datasetOutput {
		return nil, fmt.Errorf("unsupported dataset direction %d", direction)
	}
	if len(names) == 0 {
		return nil, nil
	}
	result := make([]Dataset, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}

		dataset := Dataset{Namespace: p.namespace, Name: name}
		facets := make(Facets)
		directionalFacets := make(Facets)
		if publication, exists := p.publications[name]; exists {
			facets["schema"] = mustFacet(schemaFacet(publication))
			facets["version"] = mustFacet(versionFacet(publication.version))
		}
		if stats, exists := p.statistics[name]; exists {
			if direction == datasetInput {
				directionalFacets["inputStatistics"] = mustFacet(inputStatisticsFacet(stats))
			} else {
				directionalFacets["outputStatistics"] = mustFacet(outputStatisticsFacet(stats))
			}
		}
		if quality, exists := p.quality[name]; exists {
			assertions := qualityAssertionsFacet(quality)
			if assertions != nil {
				directionalFacets["dataQualityAssertions"] = mustFacet(assertions)
			}
			metrics, err := qualityMetricsFacet(quality)
			if err != nil {
				return nil, fmt.Errorf("dataset %q quality metrics: %w", name, err)
			}
			if metrics != nil {
				facets["dataQualityMetrics"] = mustFacet(metrics)
			}
		}
		if direction == datasetOutput {
			if entries := p.lineage[name]; len(entries) > 0 {
				facets["columnLineage"] = mustFacet(columnLineageFacet(p.namespace, entries))
			}
		}
		if len(facets) > 0 {
			dataset.Facets = facets
		}
		if len(directionalFacets) > 0 {
			if direction == datasetInput {
				dataset.InputFacets = directionalFacets
			} else {
				dataset.OutputFacets = directionalFacets
			}
		}
		result = append(result, dataset)
	}
	return result, nil
}

func containsName(names []string, wanted string) bool {
	for _, name := range names {
		if strings.TrimSpace(name) == wanted {
			return true
		}
	}
	return false
}

func datasetNameSet(names []string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			result[name] = struct{}{}
		}
	}
	return result
}

func decodeContractPublication(publication ContractPublication) (publicationSchema, error) {
	if err := publication.Validate(); err != nil {
		return publicationSchema{}, fmt.Errorf("contract publication %q is invalid: %w", publication.AuthoredID, err)
	}
	if strings.TrimSpace(publication.InstanceID) == "" || publication.InstanceID != strings.TrimSpace(publication.InstanceID) || hasControl(publication.InstanceID) {
		return publicationSchema{}, fmt.Errorf("contract publication has invalid instance id")
	}
	if publication.AuthoredID.Validate() != nil {
		return publicationSchema{}, fmt.Errorf("contract publication has invalid authored id %q", publication.AuthoredID)
	}
	if publication.ProjectionProfile != contractprojection.Profile || publication.Version == "" || publication.Version != strings.TrimSpace(publication.Version) || hasControl(publication.Version) {
		return publicationSchema{}, fmt.Errorf("contract publication %q has invalid projection identity", publication.AuthoredID)
	}
	baseline, err := contractversion.SemverBaseline(publication.Version)
	if err != nil || strings.TrimPrefix(baseline, "v") != publication.VersionBaseline {
		return publicationSchema{}, fmt.Errorf("contract publication %q has inconsistent version baseline", publication.AuthoredID)
	}
	if len(publication.CanonicalBytes) == 0 || len(publication.CanonicalBytes) > 16<<20 {
		return publicationSchema{}, fmt.Errorf("contract publication %q has invalid canonical bytes", publication.AuthoredID)
	}

	var version string
	var fields []schemaField
	var checks map[string]publishedCheck
	switch publication.ResourceKind {
	case projectgraph.KindSource:
		projection, err := contractprojection.DecodeSourcePublication(publication.CanonicalBytes)
		if err != nil {
			return publicationSchema{}, fmt.Errorf("contract publication %q: decode Source projection: %w", publication.AuthoredID, err)
		}
		if projection.Kind != "Source" || projection.Metadata.ID != publication.AuthoredID.String() {
			return publicationSchema{}, fmt.Errorf("contract publication %q disagrees with canonical Source identity", publication.AuthoredID)
		}
		version = projection.Metadata.Contract.Version
		fields = sourceSchemaFields(projection)
	case projectgraph.KindModel:
		projection, err := contractprojection.DecodeModelPublication(publication.CanonicalBytes)
		if err != nil {
			return publicationSchema{}, fmt.Errorf("contract publication %q: decode Model projection: %w", publication.AuthoredID, err)
		}
		if projection.Kind != "Model" || projection.Metadata.ID != publication.AuthoredID.String() {
			return publicationSchema{}, fmt.Errorf("contract publication %q disagrees with canonical Model identity", publication.AuthoredID)
		}
		version = projection.Metadata.Contract.Version
		fields = modelSchemaFields(projection)
		checks, err = modelPublishedChecks(projection)
		if err != nil {
			return publicationSchema{}, fmt.Errorf("contract publication %q: authored checks: %w", publication.AuthoredID, err)
		}
	default:
		return publicationSchema{}, fmt.Errorf("contract publication %q has unsupported resource kind %q", publication.AuthoredID, publication.ResourceKind)
	}
	if version != publication.Version {
		return publicationSchema{}, fmt.Errorf("contract publication %q disagrees with canonical projection", publication.AuthoredID)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	return publicationSchema{version: publication.Version, fields: fields, checks: checks}, nil
}

func sourceSchemaFields(projection contractprojection.SourceView) []schemaField {
	if projection.Contract.Schema.Fields == nil {
		return []schemaField{}
	}
	fields := make([]schemaField, 0, len(*projection.Contract.Schema.Fields))
	for name, field := range *projection.Contract.Schema.Fields {
		value := schemaField{name: name}
		if field.Datatype != nil {
			value.typeName = *field.Datatype
		}
		fields = append(fields, value)
	}
	return fields
}

func modelSchemaFields(projection contractprojection.ModelView) []schemaField {
	fields := make([]schemaField, 0, len(projection.Contract.Fields))
	for name, field := range projection.Contract.Fields {
		value := schemaField{name: name}
		if field.Datatype != nil {
			value.typeName = *field.Datatype
		}
		fields = append(fields, value)
	}
	return fields
}

func modelPublishedChecks(projection contractprojection.ModelView) (map[string]publishedCheck, error) {
	checks := make(map[string]publishedCheck)
	if projection.Contract.Checks == nil {
		return checks, nil
	}
	for _, check := range *projection.Contract.Checks {
		if !canonicalFacetText(check.ID, 256) || !canonicalFacetText(check.Type, 256) {
			return nil, fmt.Errorf("check identity is invalid")
		}
		if _, exists := checks[check.ID]; exists {
			return nil, fmt.Errorf("duplicate check id %q", check.ID)
		}
		value := publishedCheck{id: check.ID, typeName: check.Type}
		if check.Field != nil {
			if !canonicalFacetText(*check.Field, 256) {
				return nil, fmt.Errorf("check %q field is invalid", check.ID)
			}
			value.field = *check.Field
		}
		if check.Fields != nil {
			value.fields = append([]string(nil), (*check.Fields)...)
			for _, field := range value.fields {
				if !canonicalFacetText(field, 256) {
					return nil, fmt.Errorf("check %q field is invalid", check.ID)
				}
			}
		}
		if check.Values != nil {
			value.values = append([]string(nil), (*check.Values)...)
			for _, accepted := range value.values {
				if !canonicalFacetText(accepted, 1024) {
					return nil, fmt.Errorf("check %q accepted value is invalid", check.ID)
				}
			}
		}
		if check.To != nil {
			if !canonicalFacetText(*check.To, 512) {
				return nil, fmt.Errorf("check %q relationship is invalid", check.ID)
			}
			value.to = *check.To
		}
		if check.Severity != nil {
			if !canonicalFacetText(*check.Severity, 32) {
				return nil, fmt.Errorf("check %q severity is invalid", check.ID)
			}
			value.severity = *check.Severity
		} else {
			// The release gate evaluator's closed vocabulary defaults omitted
			// authored severity to an error-level check.
			value.severity = "error"
		}
		if check.Minimum != nil {
			minimum := *check.Minimum
			value.minimum = &minimum
		}
		if check.Maximum != nil {
			maximum := *check.Maximum
			value.maximum = &maximum
		}
		checks[check.ID] = value
	}
	return checks, nil
}

func schemaFacet(publication publicationSchema) map[string]any {
	fields := make([]map[string]any, 0, len(publication.fields))
	for _, field := range publication.fields {
		value := map[string]any{"name": field.name}
		if field.typeName != "" {
			value["type"] = field.typeName
		}
		fields = append(fields, value)
	}
	return map[string]any{
		"_producer": Producer, "_schemaURL": SchemaFacetSchemaURL,
		"fields": fields,
	}
}

func versionFacet(version string) map[string]any {
	return map[string]any{
		"_producer": Producer, "_schemaURL": VersionFacetSchemaURL,
		"datasetVersion": version,
	}
}

func inputStatisticsFacet(stats DatasetStatistics) map[string]any {
	return map[string]any{
		"_producer": Producer, "_schemaURL": InputStatisticsSchemaURL,
		"rowCount": stats.RowCount, "size": stats.SizeBytes, "fileCount": stats.FileCount,
	}
}

func outputStatisticsFacet(stats DatasetStatistics) map[string]any {
	return map[string]any{
		"_producer": Producer, "_schemaURL": OutputStatisticsSchemaURL,
		"rowCount": stats.RowCount, "size": stats.SizeBytes, "fileCount": stats.FileCount,
	}
}

func qualityAssertionsFacet(quality datasetQuality) map[string]any {
	assertions := make([]map[string]any, 0, len(quality.checks))
	for _, check := range quality.checks {
		evidence := check.evidence
		value := map[string]any{
			"assertion": evidence.Kind,
			"success":   evidence.Outcome == release.GateSuccess,
		}
		if evidence.Severity != "" {
			severity := evidence.Severity
			if severity == "warning" {
				severity = "warn"
			}
			value["severity"] = severity
		}
		if evidence.ObservedRows >= 0 {
			value["actual"] = strconv.FormatInt(evidence.ObservedRows, 10)
		}
		value["name"] = check.authored.id
		if check.authored.field != "" {
			value["column"] = check.authored.field
		}
		if expected := expectedCheckValue(check.authored); expected != "" {
			value["expected"] = expected
		}
		params := make(map[string]any)
		if len(check.authored.fields) > 0 {
			params["fields"] = append([]string(nil), check.authored.fields...)
		}
		if len(check.authored.values) > 0 {
			params["values"] = append([]string(nil), check.authored.values...)
		}
		if check.authored.to != "" {
			params["to"] = check.authored.to
		}
		if len(params) > 0 {
			value["params"] = params
		}
		assertions = append(assertions, value)
	}
	if len(assertions) == 0 {
		return nil
	}
	sort.Slice(assertions, func(i, j int) bool {
		left, right := assertions[i], assertions[j]
		leftName, _ := left["name"].(string)
		rightName, _ := right["name"].(string)
		if leftName != rightName {
			return leftName < rightName
		}
		leftAssertion, _ := left["assertion"].(string)
		rightAssertion, _ := right["assertion"].(string)
		return leftAssertion < rightAssertion
	})
	return map[string]any{
		"_producer": Producer, "_schemaURL": QualityAssertionsSchemaURL,
		"assertions": assertions,
	}
}

func qualityMetricsFacet(quality datasetQuality) (map[string]any, error) {
	var rowCount *int64
	for _, check := range quality.checks {
		if check.authored.typeName != "row_count" {
			continue
		}
		observed := check.evidence.ObservedRows
		if rowCount == nil {
			rowCount = &observed
			continue
		}
		if *rowCount != observed {
			return nil, fmt.Errorf("row-count checks disagree")
		}
	}
	if rowCount == nil {
		return nil, nil
	}
	return map[string]any{
		"_producer": Producer, "_schemaURL": QualityMetricsSchemaURL,
		"rowCount":      *rowCount,
		"columnMetrics": map[string]any{},
	}, nil
}

func expectedCheckValue(check publishedCheck) string {
	switch {
	case check.minimum != nil && check.maximum != nil:
		return strconv.FormatInt(*check.minimum, 10) + ".." + strconv.FormatInt(*check.maximum, 10)
	case check.minimum != nil:
		return strconv.FormatInt(*check.minimum, 10)
	case check.maximum != nil:
		return strconv.FormatInt(*check.maximum, 10)
	default:
		return ""
	}
}

func columnLineageFacet(namespace string, entries []PhysicalLineage) map[string]any {
	fields := make(map[string][]map[string]any)
	for _, entry := range entries {
		value := map[string]any{
			"namespace": namespace,
			"name":      entry.Dataset,
			"field":     entry.Field,
		}
		if len(entry.Route) > 0 {
			value["transformations"] = []map[string]any{{
				"type": "DIRECT", "subtype": "RELATIONSHIP",
				"description": strings.Join(entry.Route, "."), "masking": false,
			}}
		}
		fields[entry.Logical] = append(fields[entry.Logical], value)
	}
	fieldValues := make(map[string]any, len(fields))
	for logical, values := range fields {
		fieldValues[logical] = map[string]any{"inputFields": values}
	}
	return map[string]any{
		"_producer": Producer, "_schemaURL": ColumnLineageSchemaURL,
		"fields": fieldValues,
	}
}

func canonicalLineage(output string, entries []PhysicalLineage, eventInputs map[string]struct{}, publications map[string]publicationSchema) ([]PhysicalLineage, error) {
	outputSchema, exists := publications[output]
	if !exists {
		return nil, fmt.Errorf("output dataset %q has no published contract", output)
	}
	outputFields := schemaFieldSet(outputSchema.fields)
	result := append([]PhysicalLineage(nil), entries...)
	for index := range result {
		entry := &result[index]
		if !canonicalFacetText(entry.Logical, 256) || !canonicalFacetText(entry.Dataset, 256) || !canonicalFacetText(entry.Field, 256) {
			return nil, fmt.Errorf("lineage identity is invalid")
		}
		if _, exists := eventInputs[entry.Dataset]; !exists {
			return nil, fmt.Errorf("lineage source dataset %q is not an event input", entry.Dataset)
		}
		if _, exists := outputFields[entry.Logical]; !exists {
			return nil, fmt.Errorf("lineage output field %q is absent from published contract %q", entry.Logical, output)
		}
		inputSchema, exists := publications[entry.Dataset]
		if !exists {
			return nil, fmt.Errorf("lineage source dataset %q has no published contract", entry.Dataset)
		}
		if _, exists := schemaFieldSet(inputSchema.fields)[entry.Field]; !exists {
			return nil, fmt.Errorf("lineage source field %q is absent from published contract %q", entry.Field, entry.Dataset)
		}
		for _, route := range entry.Route {
			if !canonicalFacetText(route, 256) {
				return nil, fmt.Errorf("lineage route is invalid")
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.Logical != right.Logical {
			return left.Logical < right.Logical
		}
		if left.Dataset != right.Dataset {
			return left.Dataset < right.Dataset
		}
		if left.Field != right.Field {
			return left.Field < right.Field
		}
		return strings.Join(left.Route, "\x00") < strings.Join(right.Route, "\x00")
	})
	deduped := result[:0]
	for _, entry := range result {
		if len(deduped) > 0 && sameLineage(deduped[len(deduped)-1], entry) {
			continue
		}
		deduped = append(deduped, entry)
	}
	return deduped, nil
}

func schemaFieldSet(fields []schemaField) map[string]struct{} {
	result := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		result[field.name] = struct{}{}
	}
	return result
}

func sameLineage(left, right PhysicalLineage) bool {
	if left.Logical != right.Logical || left.Dataset != right.Dataset || left.Field != right.Field || len(left.Route) != len(right.Route) {
		return false
	}
	for index := range left.Route {
		if left.Route[index] != right.Route[index] {
			return false
		}
	}
	return true
}

func validateDatasetStatistics(stats DatasetStatistics) error {
	if stats.RowCount < 0 || stats.ColumnCount < 0 || stats.FileCount < 0 || stats.SizeBytes < 0 || stats.SnapshotID < 0 {
		return fmt.Errorf("statistics cannot be negative")
	}
	if !stats.SnapshotAt.IsZero() && !stats.SnapshotAt.Equal(stats.SnapshotAt.UTC()) {
		return fmt.Errorf("snapshot time must be UTC")
	}
	seen := make(map[string]struct{}, len(stats.Columns))
	for _, column := range stats.Columns {
		if !canonicalFacetText(column.Name, 256) {
			return fmt.Errorf("catalog column name is invalid")
		}
		if _, exists := seen[column.Name]; exists {
			return fmt.Errorf("duplicate catalog column %q", column.Name)
		}
		seen[column.Name] = struct{}{}
		if column.Ordinal < 0 {
			return fmt.Errorf("catalog column ordinal cannot be negative")
		}
	}
	return nil
}

func canonicalFacetText(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit && !hasControl(value)
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
