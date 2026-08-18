package model

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestManagedConnectionRejectsAuthoredPhysicalLocation(t *testing.T) {
	for _, connection := range []Connection{
		{Kind: "managed", Root: "/server/revision"},
		{Kind: "managed", Scope: "s3://private-bucket/revision"},
	} {
		if _, err := connection.Validate("olist"); err == nil || !strings.Contains(err.Error(), "physical location") {
			t.Fatalf("Validate() error = %v, want managed physical location rejection", err)
		}
	}
}

func TestConnectionRejectsRemovedLocalKind(t *testing.T) {
	_, err := (Connection{Kind: "local"}).Validate("files")
	if err == nil || !strings.Contains(err.Error(), `unsupported kind "local"`) {
		t.Fatalf("Validate() error = %v, want unsupported local kind", err)
	}
}

func TestLogicalExternalConnectionDefersTargetOwnedAuthOnlyDuringAuthoring(t *testing.T) {
	connection := Connection{Kind: "postgres"}
	if _, err := connection.ValidateAuthored("warehouse"); err != nil {
		t.Fatalf("ValidateAuthored() error = %v", err)
	}
	if _, err := connection.Validate("warehouse"); err == nil || !strings.Contains(err.Error(), "requires auth") {
		t.Fatalf("Validate() error = %v, want unresolved runtime auth rejection", err)
	}
}

func TestPublicAccessIsExplicitAndConnectorBounded(t *testing.T) {
	public := Connection{Kind: "s3", Access: ConnectionAccessPublic}
	if _, err := public.Validate("objects"); err != nil {
		t.Fatalf("public s3 connection rejected: %v", err)
	}
	omitted := Connection{Kind: "s3"}
	if _, err := omitted.ValidateAuthored("objects"); err != nil {
		t.Fatalf("omitted s3 authored connection rejected: %v", err)
	}
	if public.Access == omitted.Access || ConnectionCredentialsConfigured(public) {
		t.Fatalf("public and omitted access collapsed: public=%q credentials=%v", public.Access, ConnectionCredentialsConfigured(public))
	}
	if _, err := (Connection{Kind: "postgres", Access: ConnectionAccessPublic}).ValidateAuthored("warehouse"); err == nil {
		t.Fatal("unsupported public postgres access accepted")
	}
}

func TestPublicAccessRejectsCredentialMaterial(t *testing.T) {
	connection := Connection{Kind: "s3", Access: ConnectionAccessPublic, Auth: ConnectionAuth{"secret_access_key": "secret"}}
	if _, err := connection.Validate("objects"); err == nil || !strings.Contains(err.Error(), "public access cannot include") {
		t.Fatalf("public credential-bearing connection error = %v", err)
	}
}

func TestLogicalQuackConnectionRequiresTargetOwnedEndpointAndTokenAtRuntime(t *testing.T) {
	logical := Connection{Kind: "quack"}
	if _, err := logical.ValidateAuthored("lakehouse"); err != nil {
		t.Fatalf("ValidateAuthored() error = %v", err)
	}
	if _, err := logical.Validate("lakehouse"); err == nil || !strings.Contains(err.Error(), "requires endpoint") {
		t.Fatalf("Validate() error = %v, want unresolved endpoint rejection", err)
	}
	resolved := Connection{
		Kind: "quack", Host: "quack.example.com", Port: 443, SSLMode: "require",
		Auth: ConnectionAuth{"token": "source-secret"},
	}
	if _, err := resolved.Validate("lakehouse"); err != nil {
		t.Fatalf("Validate() resolved Quack error = %v", err)
	}
	resolved.Auth["password"] = "forbidden"
	if _, err := resolved.Validate("lakehouse"); err == nil || !strings.Contains(err.Error(), "unsupported auth key") {
		t.Fatalf("Validate() extra auth error = %v", err)
	}
}

func TestObjectStorageCredentialModes(t *testing.T) {
	for _, connection := range []Connection{
		{Kind: "s3", Scope: "s3://public/", Credentials: ConnectionCredentials{Provider: "none"}},
		{Kind: "s3", Scope: "s3://private/", Credentials: ConnectionCredentials{Provider: "ambient", Region: "eu-west-1"}},
		{Kind: "azure_blob", Scope: "az://container/", Credentials: ConnectionCredentials{Provider: "ambient", AccountName: "analytics"}},
	} {
		if _, err := connection.Validate("lake"); err != nil {
			t.Fatalf("Validate(%#v): %v", connection.Credentials, err)
		}
	}
	for _, connection := range []Connection{
		{Kind: "r2", Credentials: ConnectionCredentials{Provider: "ambient"}},
		{Kind: "azure_blob", Credentials: ConnectionCredentials{Provider: "ambient"}},
		{Kind: "s3", Credentials: ConnectionCredentials{Provider: "ambient"}},
		{Kind: "azure_blob", Credentials: ConnectionCredentials{Provider: "ambient", AccountName: "analytics"}},
		{Kind: "azure_blob", Scope: "az://container/", Credentials: ConnectionCredentials{Provider: "ambient", AccountName: "analytics", Endpoint: "blob.example.com"}},
	} {
		if _, err := connection.Validate("lake"); err == nil {
			t.Fatalf("Validate(%#v) succeeded", connection)
		}
	}
}

func TestManagedSourceRejectsAbsoluteAndTraversalPaths(t *testing.T) {
	connections := map[string]Connection{"olist": {Kind: "managed"}}
	for _, value := range []string{filepath.Join(string(filepath.Separator), "orders.csv"), "../orders.csv"} {
		source := Source{Connection: "olist", Path: value, Format: "csv"}
		if err := source.Validate("orders", connections); err == nil {
			t.Fatalf("Validate(%q) error = nil, want unsafe managed path rejection", value)
		}
	}
}

func TestValidateRejectsAuthoredSourceReads(t *testing.T) {
	model := &Model{
		Name:        "test",
		Connections: map[string]Connection{"files": {Kind: "managed"}},
		Sources: map[string]Source{
			"orders": {Connection: "files", Path: "orders.csv", Format: "csv"},
		},
		Tables: map[string]Table{
			"orders": {
				Sources:     []string{"orders"},
				SourceReads: map[string][]string{"orders": {"order_id"}},
				Entities:    map[string]ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}},
				GrainEntity: "order_id",
				Dimensions:  map[string]MetricDimension{"order_id": {Label: "Order ID"}},
				Transform:   Transform{SQL: "SELECT order_id FROM source.orders"},
			},
		},
	}

	err := model.Validate()
	if err == nil || !strings.Contains(err.Error(), "source_reads is no longer supported") {
		t.Fatalf("Validate() error = %v, want source_reads rejection", err)
	}
}

func TestSemanticDefinitionsValidateTypedMetricsDimensionsAndMetrics(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Model)
		wantErr string
	}{
		{
			name: "missing non-count input",
			mutate: func(model *Model) {
				model.Metrics["rating_count"] = Metric{Type: "aggregate", Dataset: "ratings", Aggregation: "sum", Empty: "zero"}
			},
			wantErr: "aggregate input is required",
		},
		{
			name: "input from another fact",
			mutate: func(model *Model) {
				model.Metrics["rating_total"] = Metric{Type: "aggregate", Dataset: "ratings", Aggregation: "sum", Input: &MetricInput{Field: "tags.weight"}, Empty: "null"}
			},
			wantErr: "is not owned by dataset",
		},
		{
			name: "metric-only input function",
			mutate: func(model *Model) {
				model.Metrics["rating_total"] = Metric{Type: "aggregate", Dataset: "ratings", Aggregation: "sum", Input: &MetricInput{}, Empty: "null"}
			},
			wantErr: "aggregate input is required",
		},
		{
			name: "invalid time grain",
			mutate: func(model *Model) {
				model.Dimensions["activity_date"] = SemanticDimension{Type: "timestamp", Datatype: DataTypeDateTime, Grains: []string{"fortnight"}, Bindings: map[string]DimensionBinding{"ratings": {Field: "ratings.rated_at"}}}
			},
			wantErr: "unsupported time grain",
		},
		{
			name: "incompatible binding type",
			mutate: func(model *Model) {
				model.Dimensions["score_label"] = SemanticDimension{Type: "string", Datatype: DataTypeString, Bindings: map[string]DimensionBinding{"ratings": {Field: "ratings.score"}}}
			},
			wantErr: "is incompatible",
		},
		{
			name: "metric cycle",
			mutate: func(model *Model) {
				model.Metrics = map[string]Metric{"a": {Type: "derived", Expression: "${b}"}, "b": {Type: "derived", Expression: "${a}"}}
			},
			wantErr: "dependency cycle",
		},
		{
			name: "ratio cycle",
			mutate: func(model *Model) {
				model.Metrics = map[string]Metric{
					"a":            {Type: "ratio", Numerator: "b", Denominator: "rating_count"},
					"b":            {Type: "ratio", Numerator: "a", Denominator: "rating_count"},
					"rating_count": {Type: "aggregate", Dataset: "ratings", Aggregation: "count", Input: &MetricInput{Field: "ratings.score"}},
				}
			},
			wantErr: "dependency cycle",
		},
		{
			name: "unknown metric filter",
			mutate: func(model *Model) {
				model.Metrics = map[string]Metric{
					"filtered": {Type: "aggregate", Dataset: "ratings", Aggregation: "sum", Input: &MetricInput{Field: "ratings.score"}, Where: []string{"missing_filter"}},
				}
			},
			wantErr: "unknown semantic filter",
		},
		{
			name: "ambiguous implicit binding",
			mutate: func(model *Model) {
				model.Relationships = append(model.Relationships,
					Relationship{ID: "ratings_movies_alt", FromDataset: "ratings", FromFields: []string{"alt_movie_id"}, ToDataset: "movies", ToFields: []string{"movie_id"}, Cardinality: "many_to_one"},
				)
				model.Dimensions["movie_title"] = SemanticDimension{Type: "string", Datatype: DataTypeString, Bindings: map[string]DimensionBinding{"ratings": {Field: "movies.title"}}}
			},
			wantErr: "ambiguous relationship path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := semanticDefinitionTestModel()
			test.mutate(model)
			err := model.validateSemanticDefinitions()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateSemanticDefinitions() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestSemanticCountAcceptsTypedInput(t *testing.T) {
	model := semanticDefinitionTestModel()
	model.Metrics["rating_count"] = Metric{Type: "aggregate", Dataset: "ratings", Aggregation: "count", Input: &MetricInput{Field: "ratings.score"}, Empty: "zero"}
	if err := model.validateSemanticDefinitions(); err != nil {
		t.Fatalf("count input rejected: %v", err)
	}
}

func TestSemanticDimensionExplicitPathResolvesAmbiguousGraph(t *testing.T) {
	model := semanticDefinitionTestModel()
	model.Relationships = append(model.Relationships,
		Relationship{ID: "ratings_movies_alt", FromDataset: "ratings", FromFields: []string{"alt_movie_id"}, ToDataset: "movies", ToFields: []string{"movie_id"}, Cardinality: "many_to_one"},
	)
	model.Dimensions["movie_title"] = SemanticDimension{Type: "string", Datatype: DataTypeString, Bindings: map[string]DimensionBinding{
		"ratings": {Field: "movies.title", Path: []string{"ratings_movies"}},
	}}
	if err := model.validateSemanticDefinitions(); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticFilterLiteralValidationIsStrictAndTyped(t *testing.T) {
	tests := []struct {
		name      string
		dimension MetricDimension
		value     any
		wantErr   string
	}{
		{name: "integer rejects string", dimension: MetricDimension{Datatype: DataTypeInteger}, value: "1", wantErr: "not an integer"},
		{name: "boolean rejects string", dimension: MetricDimension{Datatype: DataTypeBoolean}, value: "true", wantErr: "not boolean"},
		{name: "date rejects datetime", dimension: MetricDimension{Datatype: DataTypeDate}, value: "2026-01-01T00:00:00", wantErr: "not a date"},
		{name: "datetime accepts fractional seconds", dimension: MetricDimension{Datatype: DataTypeDateTime}, value: "2026-01-01T00:00:00.123", wantErr: ""},
		{name: "datetime rejects timezone", dimension: MetricDimension{Datatype: DataTypeDateTime}, value: "2026-01-01T00:00:00Z", wantErr: "timezone-free"},
		{name: "datetimetz requires timezone", dimension: MetricDimension{Datatype: DataTypeDateTimeTZ}, value: "2026-01-01T00:00:00", wantErr: "RFC3339"},
		{name: "opaque rejects comparison", dimension: MetricDimension{Datatype: DataTypeOpaque}, value: "anything", wantErr: "opaque"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CoerceSemanticLiteral(test.value, test.dimension)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("CoerceSemanticLiteral() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("CoerceSemanticLiteral() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestSemanticDefinitionsRejectInvalidFilterLiteral(t *testing.T) {
	model := semanticDefinitionTestModel()
	ratings := model.Tables["ratings"]
	score := ratings.Dimensions["score"]
	score.Datatype = DataTypeInteger
	ratings.Dimensions["score"] = score
	model.Tables["ratings"] = ratings
	model.Filters = map[string]SemanticFilterSpec{
		"invalid": {Field: "ratings.score", Operator: "equals", Value: "1"},
	}
	if err := model.validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "not an integer") {
		t.Fatalf("validateSemanticDefinitions() error = %v, want strict integer literal validation", err)
	}
}

func TestSemanticGraphRejectsClosedMetricAndFilterShapes(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Model)
		wantErr string
	}{
		{
			name: "unsupported aggregation",
			mutate: func(model *Model) {
				model.Metrics["bad"] = Metric{Type: "aggregate", Dataset: "orders", Aggregation: "median", Input: &MetricInput{Field: "orders.amount"}}
			},
			wantErr: "unsupported aggregation",
		},
		{
			name: "unsupported empty value",
			mutate: func(model *Model) {
				model.Metrics["bad"] = Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &MetricInput{Field: "orders.amount"}, Empty: "error"}
			},
			wantErr: "unsupported empty value",
		},
		{
			name: "numeric input type",
			mutate: func(model *Model) {
				model.Metrics["bad"] = Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &MetricInput{Field: "orders.status"}}
			},
			wantErr: "input has unsupported datatype",
		},
		{
			name: "ratio rejects where",
			mutate: func(model *Model) {
				model.Metrics["bad"] = Metric{Type: "ratio", Numerator: "amount", Denominator: "amount", Where: []string{"eligible"}}
			},
			wantErr: "ratio does not accept aggregate or derived fields",
		},
		{
			name: "derived rejects aggregate fields",
			mutate: func(model *Model) {
				model.Metrics["bad"] = Metric{Type: "derived", Expression: "${amount}", Dataset: "orders"}
			},
			wantErr: "derived does not accept aggregate or ratio fields",
		},
		{
			name: "invalid logical datatype",
			mutate: func(model *Model) {
				model.Dimensions["bad"] = SemanticDimension{Datatype: LogicalDataType("Money"), Bindings: map[string]DimensionBinding{}}
			},
			wantErr: "unsupported datatype",
		},
		{
			name: "empty all node",
			mutate: func(model *Model) {
				model.Filters["bad"] = SemanticFilterSpec{All: []SemanticFilterSpec{}}
			},
			wantErr: "all node requires a non-empty child list",
		},
		{
			name: "boolean node cannot carry leaf fields",
			mutate: func(model *Model) {
				model.Filters["bad"] = SemanticFilterSpec{
					All:      []SemanticFilterSpec{{Field: "orders.status", Operator: "equals", Value: "open"}},
					Field:    "orders.status",
					Operator: "equals",
					Value:    "open",
				}
			},
			wantErr: "boolean node cannot contain leaf fields",
		},
		{
			name: "comparison rejects null literal",
			mutate: func(model *Model) {
				model.Filters["bad"] = SemanticFilterSpec{Field: "orders.status", Operator: "equals", Value: nil}
			},
			wantErr: "requires a value",
		},
		{
			name: "set rejects null literal",
			mutate: func(model *Model) {
				model.Filters["bad"] = SemanticFilterSpec{Field: "orders.status", Operator: "in", Value: []any{"open", nil}}
			},
			wantErr: "prohibits null values",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := directSemanticValidationModel()
			test.mutate(model)
			err := model.ValidateSemanticGraph()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateSemanticGraph() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestSemanticFilterInterchangeOmitsAbsentUnionBranches(t *testing.T) {
	value := SemanticFilterSpec{Field: "orders.status", Operator: "equals", Value: "open"}
	encoded, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "all:") || strings.Contains(text, "any:") || strings.Contains(text, "not:") {
		t.Fatalf("leaf filter encoded absent union branches: %s", text)
	}
	var decoded SemanticFilterSpec
	if err := yaml.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := directSemanticValidationModelWithFilter(decoded).ValidateSemanticGraph(); err != nil {
		t.Fatalf("round-tripped leaf filter rejected: %v", err)
	}
}

func directSemanticValidationModelWithFilter(filter SemanticFilterSpec) *Model {
	model := directSemanticValidationModel()
	model.Filters = map[string]SemanticFilterSpec{"eligible": filter}
	metric := model.Metrics["amount"]
	metric.Where = []string{"eligible"}
	model.Metrics["amount"] = metric
	return model
}

func directSemanticValidationModel() *Model {
	return &Model{
		Name: "sales",
		Tables: map[string]Table{
			"orders": {Entities: map[string]ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id", Dimensions: map[string]MetricDimension{
				"order_id": {Type: "string", Datatype: DataTypeString},
				"amount":   {Type: "number", Datatype: DataTypeDecimal},
				"status":   {Type: "string", Datatype: DataTypeString},
			}},
		},
		Datasets: map[string]SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics: map[string]Metric{
			"amount": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &MetricInput{Field: "orders.amount"}},
		},
		Dimensions: map[string]SemanticDimension{},
		Filters:    map[string]SemanticFilterSpec{},
	}
}

func TestMetricUnitInferenceAndContradictions(t *testing.T) {
	base := func() *Model {
		return &Model{
			Name: "sales",
			Tables: map[string]Table{"orders": {Dimensions: map[string]MetricDimension{
				"amount": {Type: "number", Datatype: DataTypeDecimal},
			}}},
			Metrics: map[string]Metric{
				"revenue":       {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &MetricInput{Field: "orders.amount"}, Unit: "BRL"},
				"order_count":   {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &MetricInput{Field: "orders.amount"}},
				"same_currency": {Type: "ratio", Numerator: "revenue", Denominator: "revenue"},
				"per_order":     {Type: "ratio", Numerator: "revenue", Denominator: "order_count"},
				"total":         {Type: "derived", Expression: "${revenue} + ${revenue}"},
			},
		}
	}
	model := base()
	if err := model.validateSemanticDefinitions(); err != nil {
		t.Fatalf("known compatible units rejected: %v", err)
	}
	if got := model.Metrics["per_order"].Unit; got != "BRL" {
		t.Fatalf("dimensionless denominator inference = %q, want BRL", got)
	}
	if got := model.Metrics["same_currency"].Unit; got != "dimensionless" {
		t.Fatalf("same-currency ratio inference = %q, want dimensionless", got)
	}
	if got := model.Metrics["total"].Unit; got != "BRL" {
		t.Fatalf("additive unit inference = %q, want BRL", got)
	}
	coalesced := base()
	coalesced.Metrics["coalesced"] = Metric{Type: "derived", Expression: "coalesce(${revenue}, ${revenue})"}
	if err := coalesced.validateSemanticDefinitions(); err != nil || coalesced.Metrics["coalesced"].Unit != "BRL" {
		t.Fatalf("coalesce unit inference = %q, error %v; want BRL", coalesced.Metrics["coalesced"].Unit, err)
	}
	nullif := base()
	nullif.Metrics["nullif"] = Metric{Type: "derived", Expression: "nullif(${revenue}, ${revenue})"}
	if err := nullif.validateSemanticDefinitions(); err != nil {
		t.Fatalf("compatible nullif units rejected: %v", err)
	}
	nullif.Metrics["usd"] = Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &MetricInput{Field: "orders.amount"}, Unit: "USD"}
	nullif.Metrics["bad_nullif"] = Metric{Type: "derived", Expression: "nullif(${revenue}, ${usd})"}
	if err := nullif.validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "incompatible additive units") {
		t.Fatalf("incompatible nullif units error = %v", err)
	}

	incompatible := base()
	incompatible.Metrics["usd"] = Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &MetricInput{Field: "orders.amount"}, Unit: "USD"}
	incompatible.Metrics["bad"] = Metric{Type: "derived", Expression: "${revenue} + ${usd}"}
	if err := incompatible.validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "incompatible additive units") {
		t.Fatalf("incompatible additive units error = %v", err)
	}

	contradiction := base()
	contradiction.Metrics["bad_ratio"] = Metric{Type: "ratio", Numerator: "revenue", Denominator: "order_count", Unit: "USD"}
	if err := contradiction.validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "contradicts inferred unit") {
		t.Fatalf("authored unit contradiction error = %v", err)
	}

	countUnit := base()
	count := countUnit.Metrics["order_count"]
	count.Unit = "widgets"
	countUnit.Metrics["order_count"] = count
	if err := countUnit.validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "must be dimensionless") {
		t.Fatalf("count unit contradiction error = %v", err)
	}
}

func semanticDefinitionTestModel() *Model {
	return &Model{
		Name: "activity",
		Tables: map[string]Table{
			"ratings": {Dimensions: map[string]MetricDimension{
				"movie_id": {Type: "string", Datatype: DataTypeString}, "alt_movie_id": {Type: "string", Datatype: DataTypeString}, "score": {Type: "number", Datatype: DataTypeDecimal}, "rated_at": {Type: "timestamp", Datatype: DataTypeDateTime},
			}},
			"tags":   {Dimensions: map[string]MetricDimension{"weight": {Type: "number", Datatype: DataTypeDecimal}}},
			"movies": {Dimensions: map[string]MetricDimension{"movie_id": {Type: "string", Datatype: DataTypeString}, "title": {Type: "string", Datatype: DataTypeString}}},
		},
		Datasets: map[string]SemanticDatasetSpec{
			"movies": {Model: "movies"}, "ratings": {Model: "ratings"}, "tags": {Model: "tags"},
		},
		Relationships: []Relationship{{ID: "ratings_movies", FromDataset: "ratings", FromFields: []string{"movie_id"}, ToDataset: "movies", ToFields: []string{"movie_id"}, Cardinality: "many_to_one"}},
		Metrics: map[string]Metric{
			"rating_count": {Type: "aggregate", Dataset: "ratings", Aggregation: "count", Input: &MetricInput{Field: "ratings.score"}, Empty: "zero"},
			"tag_count":    {Type: "aggregate", Dataset: "tags", Aggregation: "count", Input: &MetricInput{Field: "tags.weight"}, Empty: "zero"},
		},
		Dimensions: map[string]SemanticDimension{},
	}
}

func TestCanonicalGrainPreservesOrderedCompositeIdentity(t *testing.T) {
	table := Table{
		Entities: map[string]ModelEntitySpec{
			"order_line": {Type: "primary", Fields: []string{"order_id", "line_number"}},
		},
		GrainEntity: "order_line",
	}
	if got := table.GrainFields(); !reflect.DeepEqual(got, []string{"order_id", "line_number"}) {
		t.Fatalf("grain fields = %#v, want ordered composite tuple", got)
	}
	if _, err := table.SingularGrainField(); err == nil {
		t.Fatal("SingularGrainField accepted composite grain")
	}
}

func TestSemanticTimeGrainRetainsNativeOrderAndDatasetMetricBindings(t *testing.T) {
	model := semanticDefinitionTestModel()
	model.Dimensions["activity_date"] = SemanticDimension{
		Type: "timestamp", Datatype: DataTypeDateTime, NativeGrain: "month", Grains: []string{"month", "quarter", "year"},
		Bindings: map[string]DimensionBinding{"ratings": {Field: "ratings.rated_at"}},
	}
	model.Datasets = map[string]SemanticDatasetSpec{"ratings": {Model: "ratings", DefaultTimeDimension: "activity_date"}}
	model.Metrics["revenue"] = Metric{
		Type: "aggregate", Dataset: "ratings", Aggregation: "sum", Input: &MetricInput{Field: "ratings.score"}, TimeDimension: "activity_date",
	}
	if err := model.validateSemanticDefinitions(); err != nil {
		t.Fatalf("valid native time metadata rejected: %v", err)
	}

	model.Dimensions["activity_date"] = SemanticDimension{
		Type: "timestamp", Datatype: DataTypeDateTime, NativeGrain: "month", Grains: []string{"day", "month"},
		Bindings: map[string]DimensionBinding{"ratings": {Field: "ratings.rated_at"}},
	}
	if err := model.validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "finer than native grain") {
		t.Fatalf("finer time grain error = %v", err)
	}

	model.Dimensions["activity_date"] = SemanticDimension{Type: "timestamp", Datatype: DataTypeDateTime, Grains: []string{"month"}, Bindings: map[string]DimensionBinding{"ratings": {Field: "ratings.rated_at"}}}
	model.Datasets["ratings"] = SemanticDatasetSpec{Model: "ratings", DefaultTimeDimension: "missing"}
	if err := model.validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "default time dimension") {
		t.Fatalf("unknown default time dimension error = %v", err)
	}

	model.Dimensions["activity_date"] = SemanticDimension{
		Type: "timestamp", Datatype: DataTypeDateTime, NativeGrain: "month", Grains: []string{"month", "quarter", "year"},
		Bindings: map[string]DimensionBinding{"ratings": {Field: "ratings.rated_at"}},
	}
	model.Dimensions["activity_date_override"] = SemanticDimension{
		Type: "timestamp", Datatype: DataTypeDateTime, NativeGrain: "month", Grains: []string{"month", "quarter", "year"},
		Bindings: map[string]DimensionBinding{"ratings": {Field: "ratings.rated_at"}},
	}
	model.Datasets["ratings"] = SemanticDatasetSpec{Model: "ratings", DefaultTimeDimension: "activity_date"}
	model.Metrics["revenue"] = Metric{
		Type: "aggregate", Dataset: "ratings", Aggregation: "sum", Input: &MetricInput{Field: "ratings.score"}, TimeDimension: "activity_date_override",
	}
	if err := model.validateSemanticDefinitions(); err != nil {
		t.Fatalf("valid metric time dimension override rejected: %v", err)
	}
	model.Dimensions["activity_date_override"] = SemanticDimension{Type: "string", Datatype: DataTypeString, Bindings: map[string]DimensionBinding{"ratings": {Field: "ratings.movie_id"}}}
	if err := model.validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "is not temporal") {
		t.Fatalf("non-temporal metric override error = %v", err)
	}
}
