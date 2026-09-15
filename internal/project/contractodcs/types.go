// Package contractodcs exports immutable leapview.contract/v1 publications as
// pinned Open Data Contract Standard 3.1.0 documents.
//
// The adapter owns ODCS-specific DTOs. It never accepts compiler, graph,
// artifact, release, or runtime models.
package contractodcs

import (
	"errors"
	"fmt"
	"sort"
)

const (
	Version            = "3.1.0"
	APIVersion         = "v3.1.0"
	Kind               = "DataContract"
	SchemaSHA256       = "2cb7dd6fe43344d2233e0406438622681dc3ebadcf8f0d606a15b40c8f6752c0"
	ExtensionProperty  = "leapviewContract"
	ExtensionNamespace = "leapview.dev/odcs-extension/v1"
	MappingProfile     = "leapview.dev/odcs-mapping/v1"
	LossReportProfile  = "leapview.dev/odcs-loss/v1"
	OfficialSchemaURL  = "https://github.com/bitol-io/open-data-contract-standard/blob/v3.1.0/schema/odcs-json-schema-v3.1.0.json"
)

var (
	ErrInvalidPublication  = errors.New("invalid contract publication for ODCS export")
	ErrUnsupportedMapping  = errors.New("unsupported ODCS mapping")
	ErrInvalidODCSDocument = errors.New("invalid ODCS 3.1 document")
)

type Document struct {
	Version          string           `json:"version"`
	Kind             string           `json:"kind"`
	APIVersion       string           `json:"apiVersion"`
	ID               string           `json:"id"`
	Name             string           `json:"name,omitempty"`
	Status           string           `json:"status"`
	Schema           []SchemaObject   `json:"schema,omitempty"`
	SLAProperties    []SLAProperty    `json:"slaProperties,omitempty"`
	CustomProperties []CustomProperty `json:"customProperties"`
}

type SchemaObject struct {
	Name          string           `json:"name"`
	LogicalType   string           `json:"logicalType"`
	Properties    []SchemaProperty `json:"properties,omitempty"`
	Quality       []Quality        `json:"quality,omitempty"`
	Relationships []Relationship   `json:"relationships,omitempty"`
}

type SchemaProperty struct {
	Name                     string                    `json:"name"`
	LogicalType              string                    `json:"logicalType,omitempty"`
	LogicalTypeOptions       map[string]any            `json:"logicalTypeOptions,omitempty"`
	Required                 *bool                     `json:"required,omitempty"`
	PrimaryKey               *bool                     `json:"primaryKey,omitempty"`
	PrimaryKeyPosition       *int                      `json:"primaryKeyPosition,omitempty"`
	Unique                   *bool                     `json:"unique,omitempty"`
	Classification           string                    `json:"classification,omitempty"`
	CriticalDataElement      *bool                     `json:"criticalDataElement,omitempty"`
	AuthoritativeDefinitions []AuthoritativeDefinition `json:"authoritativeDefinitions,omitempty"`
	Quality                  []Quality                 `json:"quality,omitempty"`
	Relationships            []Relationship            `json:"relationships,omitempty"`
}

type AuthoritativeDefinition struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

type Quality struct {
	ID                     string         `json:"id"`
	Name                   string         `json:"name,omitempty"`
	Type                   string         `json:"type"`
	Metric                 string         `json:"metric"`
	Arguments              map[string]any `json:"arguments,omitempty"`
	Severity               string         `json:"severity,omitempty"`
	Unit                   string         `json:"unit,omitempty"`
	MustBe                 any            `json:"mustBe,omitempty"`
	MustBeGreaterOrEqualTo *int64         `json:"mustBeGreaterOrEqualTo,omitempty"`
	MustBeLessOrEqualTo    *int64         `json:"mustBeLessOrEqualTo,omitempty"`
	MustBeBetween          []int64        `json:"mustBeBetween,omitempty"`
}

type Relationship struct {
	Type string `json:"type"`
	From any    `json:"from,omitempty"`
	To   any    `json:"to"`
}

type SLAProperty struct {
	ID       string `json:"id,omitempty"`
	Property string `json:"property"`
	Value    int64  `json:"value"`
	Unit     string `json:"unit,omitempty"`
	Element  string `json:"element,omitempty"`
}

type CustomProperty struct {
	Property    string            `json:"property"`
	Value       ExtensionEvidence `json:"value"`
	Description string            `json:"description,omitempty"`
}

// ExtensionEvidence is the complete governed LeapView extension. It carries
// publication provenance only; executable or access semantics are forbidden.
type ExtensionEvidence struct {
	Namespace         string `json:"namespace"`
	ResourceKind      string `json:"resourceKind"`
	ProjectionProfile string `json:"projectionProfile"`
	ProjectionDigest  string `json:"projectionDigest"`
}

type MappingEntry struct {
	ID                  string `json:"id"`
	SourceField         string `json:"sourceField"`
	ODCSTargetField     string `json:"odcsTargetField"`
	Transformation      string `json:"transformation"`
	Limitations         string `json:"limitations"`
	CompatibilityImpact string `json:"compatibilityImpact"`
}

type MappingReport struct {
	Profile string         `json:"profile"`
	Entries []MappingEntry `json:"entries"`
}

type LossKind string

const (
	LossUnsupported LossKind = "unsupported"
	LossDropped     LossKind = "dropped"
	LossDegraded    LossKind = "degraded"
)

type LossEntry struct {
	Kind                LossKind `json:"kind"`
	SourceField         string   `json:"sourceField"`
	ODCSTargetField     string   `json:"odcsTargetField,omitempty"`
	Reason              string   `json:"reason"`
	CompatibilityImpact string   `json:"compatibilityImpact"`
}

type LossReport struct {
	Profile  string      `json:"profile"`
	Lossless bool        `json:"lossless"`
	Entries  []LossEntry `json:"entries"`
}

type Result struct {
	Document      []byte        `json:"document"`
	MappingReport MappingReport `json:"mappingReport"`
	LossReport    LossReport    `json:"lossReport"`
}

type MappingError struct {
	Report LossReport
}

func (e *MappingError) Error() string {
	if len(e.Report.Entries) == 0 {
		return ErrUnsupportedMapping.Error()
	}
	return fmt.Sprintf("%s: %s", ErrUnsupportedMapping, e.Report.Entries[0].SourceField)
}

func (e *MappingError) Unwrap() error { return ErrUnsupportedMapping }

type reportBuilder struct {
	mappings map[string]struct{}
	losses   map[string]LossEntry
}

func newReportBuilder() *reportBuilder {
	return &reportBuilder{mappings: map[string]struct{}{}, losses: map[string]LossEntry{}}
}

func (b *reportBuilder) mapped(id string) { b.mappings[id] = struct{}{} }

func (b *reportBuilder) loss(value LossEntry) {
	key := string(value.Kind) + "\x00" + value.SourceField + "\x00" + value.ODCSTargetField
	b.losses[key] = value
}

func (b *reportBuilder) reports() (MappingReport, LossReport, error) {
	mapping, err := mappingEntries(b.mappings)
	if err != nil {
		return MappingReport{}, LossReport{}, err
	}
	losses := make([]LossEntry, 0, len(b.losses))
	for _, value := range b.losses {
		losses = append(losses, value)
	}
	sort.Slice(losses, func(i, j int) bool {
		if losses[i].SourceField != losses[j].SourceField {
			return losses[i].SourceField < losses[j].SourceField
		}
		return losses[i].Kind < losses[j].Kind
	})
	return MappingReport{Profile: MappingProfile, Entries: mapping}, LossReport{Profile: LossReportProfile, Lossless: len(losses) == 0, Entries: losses}, nil
}
