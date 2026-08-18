package model

import (
	"fmt"
	"regexp"
)

var (
	semanticIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// Resource names may carry project-level dot/hyphen qualifiers, while
	// semantic dataset/table/member aliases use semanticIdentifierPattern.
	modelBindingNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
)

func validateModelBindingName(value string) error {
	if !modelBindingNamePattern.MatchString(value) {
		return fmt.Errorf("must match %s", modelBindingNamePattern.String())
	}
	return nil
}

// AIContext is descriptive authoring metadata. It is intentionally kept out
// of planning and execution inputs.
type AIContext struct {
	Instructions string   `yaml:"instructions"`
	Synonyms     []string `yaml:"synonyms"`
	Examples     []string `yaml:"examples"`
}

// LogicalDataType is the portable v1 field vocabulary shared by Model and
// SemanticModel resources.
type LogicalDataType string

const (
	DataTypeString     LogicalDataType = "String"
	DataTypeInteger    LogicalDataType = "Integer"
	DataTypeDecimal    LogicalDataType = "Decimal"
	DataTypeFloat      LogicalDataType = "Float"
	DataTypeBoolean    LogicalDataType = "Boolean"
	DataTypeDate       LogicalDataType = "Date"
	DataTypeTime       LogicalDataType = "Time"
	DataTypeDateTime   LogicalDataType = "DateTime"
	DataTypeDateTimeTZ LogicalDataType = "DateTimeTz"
	DataTypeOpaque     LogicalDataType = "Opaque"
)

type ModelFieldSpec struct {
	Datatype    LogicalDataType `yaml:"datatype"`
	Label       string          `yaml:"label"`
	Description string          `yaml:"description"`
	AIContext   *AIContext      `yaml:"aiContext"`
}

type ModelEntitySpec struct {
	Type        string     `yaml:"type"`
	Fields      []string   `yaml:"fields"`
	Description string     `yaml:"description"`
	AIContext   *AIContext `yaml:"aiContext"`
}

type ModelGrainSpec struct {
	Entity string `yaml:"entity"`
}

type ModelSpec struct {
	Source      string                     `yaml:"source"`
	Sources     []string                   `yaml:"sources"`
	Transform   Transform                  `yaml:"transform"`
	Entities    map[string]ModelEntitySpec `yaml:"entities"`
	Grain       ModelGrainSpec             `yaml:"grain"`
	Fields      map[string]ModelFieldSpec  `yaml:"fields"`
	Description string                     `yaml:"description"`
}

type SemanticDatasetSpec struct {
	Model                string     `yaml:"model"`
	DefaultTimeDimension string     `yaml:"defaultTimeDimension"`
	DisplayName          string     `yaml:"displayName"`
	Description          string     `yaml:"description"`
	AIContext            *AIContext `yaml:"aiContext"`
}

type RelationshipEndpointSpec struct {
	Dataset string   `yaml:"dataset"`
	Entity  string   `yaml:"entity"`
	Fields  []string `yaml:"fields"`
}

type RelationshipSpec struct {
	From        RelationshipEndpointSpec `yaml:"from"`
	To          RelationshipEndpointSpec `yaml:"to"`
	Description string                   `yaml:"description"`
	AIContext   *AIContext               `yaml:"aiContext"`
}

type TimeSemanticsSpec struct {
	NativeGrain string   `yaml:"nativeGrain"`
	Grains      []string `yaml:"grains"`
	Calendar    string   `yaml:"calendar"`
	Timezone    string   `yaml:"timezone"`
}

type SemanticDimensionSpec struct {
	Label       string                      `yaml:"label"`
	Description string                      `yaml:"description"`
	AIContext   *AIContext                  `yaml:"aiContext"`
	Datatype    LogicalDataType             `yaml:"datatype"`
	Time        *TimeSemanticsSpec          `yaml:"time"`
	Bindings    map[string]DimensionBinding `yaml:"bindings"`
}

type SemanticFilterSpec struct {
	Field     string               `yaml:"field,omitempty"`
	Operator  string               `yaml:"operator,omitempty"`
	Value     any                  `yaml:"value,omitempty"`
	Path      []string             `yaml:"path,omitempty"`
	All       []SemanticFilterSpec `yaml:"all,omitempty"`
	Any       []SemanticFilterSpec `yaml:"any,omitempty"`
	Not       *SemanticFilterSpec  `yaml:"not,omitempty"`
	AIContext *AIContext           `yaml:"aiContext,omitempty"`
}

type AggregateMetricSpec struct {
	Type          string      `yaml:"type"`
	Dataset       string      `yaml:"dataset"`
	Aggregation   string      `yaml:"aggregation"`
	Input         MetricInput `yaml:"input"`
	Where         []string    `yaml:"where"`
	Empty         string      `yaml:"empty"`
	TimeDimension string      `yaml:"timeDimension"`
}

type DerivedMetricSpec struct {
	Type       string `yaml:"type"`
	Expression string `yaml:"expression"`
}

type RatioMetricSpec struct {
	Type        string `yaml:"type"`
	Numerator   string `yaml:"numerator"`
	Denominator string `yaml:"denominator"`
}

type MetricCommonSpec struct {
	Label       string     `yaml:"label"`
	Description string     `yaml:"description"`
	AIContext   *AIContext `yaml:"aiContext"`
	Unit        string     `yaml:"unit"`
	Format      string     `yaml:"format"`
	Hidden      bool       `yaml:"hidden"`
}

type SemanticMetricSpec struct {
	MetricCommonSpec `yaml:",inline"`
	Type             string       `yaml:"type"`
	Dataset          string       `yaml:"dataset"`
	Aggregation      string       `yaml:"aggregation"`
	Input            *MetricInput `yaml:"input"`
	Where            []string     `yaml:"where"`
	Empty            string       `yaml:"empty"`
	TimeDimension    string       `yaml:"timeDimension"`
	Expression       string       `yaml:"expression"`
	Numerator        string       `yaml:"numerator"`
	Denominator      string       `yaml:"denominator"`
}

type Model struct {
	Name                    string                         `yaml:"-"`
	Title                   string                         `yaml:"-"`
	Description             string                         `yaml:"-"`
	AIContext               *AIContext                     `yaml:"aiContext,omitempty" json:"-"`
	DefaultConnection       string                         `yaml:"-"`
	Connections             map[string]Connection          `yaml:"-"`
	Sources                 map[string]Source              `yaml:"-"`
	Tables                  map[string]Table               `yaml:"-"`
	Datasets                map[string]SemanticDatasetSpec `yaml:"-"`
	StructuredRelationships map[string]RelationshipSpec    `yaml:"-"`
	Relationships           []Relationship                 `yaml:"-"`
	Dimensions              map[string]SemanticDimension   `yaml:"-"`
	Filters                 map[string]SemanticFilterSpec  `yaml:"-"`
	Metrics                 map[string]Metric              `yaml:"-"`
}

type Connection struct {
	Kind string `yaml:"kind" json:"Kind"`
	// Access is a portable, non-secret connection policy. Empty means the
	// normal target-owned authentication path; Public is explicit no-auth.
	Access      ConnectionAccess `yaml:"access,omitempty" json:"access,omitempty"`
	Description string           `yaml:"description" json:"Description"`
	Path        string           `yaml:"path" json:"Path"`
	Root        string           `yaml:"root" json:"Root"`
	Scope       string           `yaml:"scope" json:"Scope"`
	Host        string           `yaml:"host" json:"-"`
	Port        int              `yaml:"port" json:"-"`
	Database    string           `yaml:"database" json:"-"`
	Username    string           `yaml:"username" json:"-"`
	SSLMode     string           `yaml:"sslMode" json:"-"`
	// Auth is populated only on a short-lived refresh copy by the injected
	// credential resolver. It is deliberately absent from authored contracts.
	Auth           ConnectionAuth            `yaml:"-" json:"-"`
	Credentials    ConnectionCredentials     `yaml:"credentials" json:"-"`
	Options        map[string]any            `yaml:"options" json:"Options"`
	Defaults       ConnectionDefaults        `yaml:"defaults" json:"Defaults"`
	ReaderDefaults map[string]map[string]any `yaml:"-" json:"readerDefaults,omitempty"`
}

type ConnectionAccess string

const (
	ConnectionAccessPublic ConnectionAccess = "public"
)

type ConnectionCredentials struct {
	Provider    string `yaml:"provider" json:"provider"`
	Secret      string `yaml:"secret" json:"secret,omitempty"`
	Region      string `yaml:"region" json:"region,omitempty"`
	Endpoint    string `yaml:"endpoint" json:"endpoint,omitempty"`
	AccountName string `yaml:"accountName" json:"accountName,omitempty"`
}

type ConnectionDefaults struct {
	Options map[string]any `yaml:"options"`
}

type ConnectionAuth map[string]any

type Source struct {
	Format      string `yaml:"format"`
	Description string `yaml:"description"`
	Path        string `yaml:"path"`
	Connection  string `yaml:"connection"`
	Object      string `yaml:"object"`
	// Structured location evidence is retained after lowering. Object remains
	// the canonical runtime relation string for existing adapters.
	LocationType string         `yaml:"-" json:"locationType,omitempty"`
	Catalog      string         `yaml:"-" json:"catalog,omitempty"`
	SchemaName   string         `yaml:"-" json:"schemaName,omitempty"`
	RelationName string         `yaml:"-" json:"relationName,omitempty"`
	Options      map[string]any `yaml:"options"`
	// EffectiveOptions is compiler-owned and excludes secrets. Runtime readers
	// consume this resolved map instead of independently merging defaults.
	EffectiveOptions map[string]any         `yaml:"-" json:"effectiveOptions"`
	SchemaMode       string                 `yaml:"-" json:"schemaMode,omitempty"`
	Fields           map[string]SourceField `yaml:"fields"`
	Schema           TableSchema            `yaml:"-"`
}

type Table struct {
	// ModelName is populated only on lowered semantic execution tables. It
	// preserves the project Model binding after the runtime table is keyed by
	// its semantic dataset alias; authored Model resources do not expose it.
	ModelName           string                     `yaml:"-" json:"modelName,omitempty"`
	Source              string                     `yaml:"source"`
	AIContext           *AIContext                 `yaml:"aiContext,omitempty" json:"-"`
	Sources             []string                   `yaml:"sources"`
	SourceReads         map[string][]string        `yaml:"source_reads"`
	Transform           Transform                  `yaml:"transform"`
	Columns             map[string]ModelColumn     `yaml:"columns"`
	Entities            map[string]ModelEntitySpec `yaml:"entities"`
	GrainEntity         string                     `yaml:"grain_entity"`
	Dimensions          map[string]MetricDimension `yaml:"fields"`
	Description         string                     `yaml:"description"`
	Schema              TableSchema                `yaml:"-"`
	SourceDependencies  []string                   `yaml:"-"`
	ModelDependencies   []string                   `yaml:"-"`
	SQLAnalysisEvidence *SQLAnalysisEvidence       `yaml:"-" json:"sqlAnalysisEvidence,omitempty"`
}

// SQLAnalysisEvidence is normalized compiler-owned evidence from the pinned
// DuckDB AST. It intentionally excludes the ephemeral/raw serialized AST.
type SQLAnalysisEvidence struct {
	Validated  bool     `json:"validated"`
	SourceRefs []string `json:"sourceRefs,omitempty"`
	ModelRefs  []string `json:"modelRefs,omitempty"`
}

// GrainFields returns the ordered business-identity tuple selected by the
// canonical grain.entity declaration. It never collapses a composite key to a
// scalar value.
func (t Table) GrainFields() []string {
	if t.GrainEntity != "" {
		if entity, ok := t.Entities[t.GrainEntity]; ok {
			return append([]string(nil), entity.Fields...)
		}
	}
	return nil
}

// SingularGrainField is for consumers whose physical API is inherently
// scalar. Composite model grains are rejected explicitly instead of silently
// selecting their first field.
func (t Table) SingularGrainField() (string, error) {
	fields := t.GrainFields()
	if len(fields) != 1 {
		return "", fmt.Errorf("model grain requires one field, got %d", len(fields))
	}
	return fields[0], nil
}

type Transform struct {
	SQL string `yaml:"sql"`
}

type SourceField struct {
	Field       string          `yaml:"-"`
	Table       string          `yaml:"-"`
	Name        string          `yaml:"-"`
	Type        string          `yaml:"type"`
	Datatype    LogicalDataType `yaml:"-" json:"datatype,omitempty"`
	Nullable    *bool           `yaml:"-" json:"nullable,omitempty"`
	Description string          `yaml:"description"`
}

type ModelColumn struct {
	Field       string          `yaml:"-"`
	Name        string          `yaml:"-"`
	SourceField string          `yaml:"source_field"`
	Description string          `yaml:"description"`
	Type        string          `yaml:"type"`
	Datatype    LogicalDataType `yaml:"datatype,omitempty"`
	AIContext   *AIContext      `yaml:"aiContext,omitempty"`
}

type MetricDimension struct {
	Field       string          `yaml:"-"`
	Table       string          `yaml:"-"`
	Name        string          `yaml:"-"`
	Label       string          `yaml:"label"`
	Description string          `yaml:"description"`
	Type        string          `yaml:"-" json:"-"`
	Datatype    LogicalDataType `yaml:"datatype,omitempty" json:"datatype,omitempty"`
	AIContext   *AIContext      `yaml:"aiContext,omitempty" json:"-"`
}

type TableSchema struct {
	Columns []ColumnSchema `json:"columns,omitempty"`
}

type ColumnSchema struct {
	Name         string `json:"name"`
	Ordinal      int    `json:"ordinal"`
	PhysicalType string `json:"physicalType"`
	Nullable     *bool  `json:"nullable,omitempty"`
	Default      string `json:"default,omitempty"`
	Comment      string `json:"comment,omitempty"`
	PrimaryKey   bool   `json:"primaryKey,omitempty"`
}

type MetricInput struct {
	Field string `yaml:"field"`
}

type SemanticDimension struct {
	Name        string                      `yaml:"-"`
	Label       string                      `yaml:"label"`
	Description string                      `yaml:"description"`
	AIContext   *AIContext                  `yaml:"aiContext,omitempty" json:"-"`
	Type        string                      `yaml:"type"`
	Datatype    LogicalDataType             `yaml:"datatype,omitempty"`
	Grains      []string                    `yaml:"grains"`
	NativeGrain string                      `yaml:"native_grain,omitempty"`
	Timezone    string                      `yaml:"timezone,omitempty"`
	Calendar    string                      `yaml:"calendar,omitempty"`
	WeekStart   string                      `yaml:"week_start,omitempty"`
	Bindings    map[string]DimensionBinding `yaml:"bindings"`
}

type DimensionBinding struct {
	Field string   `yaml:"field"`
	Path  []string `yaml:"path"`
}

type Metric struct {
	Name          string       `yaml:"-"`
	Type          string       `yaml:"type"`
	Dataset       string       `yaml:"dataset,omitempty"`
	Aggregation   string       `yaml:"aggregation,omitempty"`
	Input         *MetricInput `yaml:"input,omitempty"`
	Where         []string     `yaml:"where,omitempty"`
	Empty         string       `yaml:"empty,omitempty"`
	TimeDimension string       `yaml:"timeDimension,omitempty"`
	Expression    string       `yaml:"expression,omitempty"`
	Numerator     string       `yaml:"numerator,omitempty"`
	Denominator   string       `yaml:"denominator,omitempty"`
	Label         string       `yaml:"label"`
	Description   string       `yaml:"description"`
	Unit          string       `yaml:"unit"`
	Format        string       `yaml:"format"`
	Hidden        bool         `yaml:"hidden"`
	AIContext     *AIContext   `yaml:"aiContext,omitempty"`
}

type Relationship struct {
	ID          string     `yaml:"id"`
	Description string     `yaml:"description"`
	Cardinality string     `yaml:"cardinality"`
	FromDataset string     `yaml:"from_dataset,omitempty"`
	FromFields  []string   `yaml:"from_fields,omitempty"`
	ToDataset   string     `yaml:"to_dataset,omitempty"`
	ToFields    []string   `yaml:"to_fields,omitempty"`
	AIContext   *AIContext `yaml:"aiContext,omitempty" json:"-"`
}
