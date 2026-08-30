package deploymentpostgres

import (
	"errors"
	"reflect"
	"testing"
	"time"

	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

func qualificationInputArtifacts() release.CandidateArtifactSet {
	nullable := true
	return release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{SourceDigest: qualificationDigest("source")},
		Compiler: release.CandidateCompilerEvidence{Manifest: projectmanifest.Project{
			Sources: map[string]semanticmodel.Source{
				"source:customers": {
					SchemaMode: "strict",
					Fields:     map[string]semanticmodel.SourceField{"id": {Name: "id", Nullable: &nullable}},
				},
				"source:orders": {
					SchemaMode: "inferred",
					Freshness:  &semanticmodel.SourceFreshnessSpec{Basis: "revision", Revision: "rev-1"},
				},
			},
			Models: nil,
			NameIndex: projectmanifest.NameIndex{Sources: map[string]string{
				"customers":  "source:customers",
				"orders.raw": "source:orders",
			}},
		}},
		Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataRefreshSources},
	}
}

func qualificationInputModelArtifacts() release.CandidateArtifactSet {
	artifacts := qualificationInputArtifacts()
	minimum, maximum := int64(1), int64(10)
	artifacts.Compiler.Manifest.Models = map[string]semanticmodel.Table{
		"model:customers": {
			ModelName:          "customers",
			Entities:           map[string]semanticmodel.EntityDefinition{"customer": {Type: "primary", Fields: []string{"id"}}},
			GrainEntity:        "customer",
			Checks:             []semanticmodel.ModelCheck{{Type: "non_null", Field: "id", Severity: "error", Minimum: &minimum, Maximum: &maximum}},
			SourceDependencies: []string{"source:customers"},
		},
		"model:orders": {
			ModelName:   "orders",
			Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "unique", Fields: []string{"id"}}},
			GrainEntity: "order",
		},
	}
	return artifacts
}

func qualificationInputObservations() []analyticsmaterialize.SourceObservation {
	return []analyticsmaterialize.SourceObservation{
		{ID: "orders_raw", Schema: []semanticmodel.ColumnSchema{{Name: "id", Ordinal: 1, PhysicalType: "BIGINT"}}, Revision: "rev-1", RevisionObserved: time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC), FreshnessObserved: time.Date(2026, 8, 30, 11, 1, 0, 0, time.UTC), ObservationQueries: 2, ObservationRows: 3, ObservationMillis: 4},
		{ID: "source:customers", Schema: []semanticmodel.ColumnSchema{{Name: "id", Ordinal: 1, PhysicalType: "BIGINT"}}, SchemaFailure: analyticsmaterialize.ObservationUnavailable, FreshnessFailure: analyticsmaterialize.ObservationTimeout, ObservationQueries: 5, ObservationRows: 6, ObservationMillis: 7},
	}
}

func TestNativeQualificationInputsMapsAliasesAndSortsDetachedValues(t *testing.T) {
	artifacts := qualificationInputModelArtifacts()
	observations := qualificationInputObservations()
	sources, models, err := nativeQualificationInputs(artifacts, observations)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{sources[0].ID, sources[1].ID}; !reflect.DeepEqual(got, []string{"source:customers", "source:orders"}) {
		t.Fatalf("source ids = %v", got)
	}
	if sources[1].Revision != "rev-1" || sources[1].RevisionObserved.IsZero() || sources[1].FreshnessObserved.IsZero() || sources[1].ObservationQueries != 2 || sources[1].ObservationRows != 3 || sources[1].ObservationMillis != 4 {
		t.Fatalf("orders source evidence = %#v", sources[1])
	}
	if sources[0].SchemaFailure != analyticsmaterialize.ObservationUnavailable || sources[0].FreshnessFailure != analyticsmaterialize.ObservationTimeout {
		t.Fatalf("customers failures = %#v", sources[0])
	}
	if got := []string{models[0].ID, models[1].ID}; !reflect.DeepEqual(got, []string{"model:customers", "model:orders"}) {
		t.Fatalf("model ids = %v", got)
	}
	if models[0].Model.GrainEntity != "customer" || len(models[0].Model.Entities["customer"].Fields) != 1 || len(models[0].Model.Checks) != 1 {
		t.Fatalf("model contract was not carried: %#v", models[0].Model)
	}

	// Returned nested values must not alias the immutable artifact or caller
	// observations.
	sources[0].Observed[0].Name = "changed"
	*sources[0].Source.Fields["id"].Nullable = false
	models[0].Model.Entities["customer"].Fields[0] = "changed"
	models[0].Model.Checks[0].Fields = []string{"changed"}
	if artifacts.Compiler.Manifest.Sources["source:customers"].Fields["id"].Nullable == nil || !*artifacts.Compiler.Manifest.Sources["source:customers"].Fields["id"].Nullable {
		t.Fatal("source field nullable pointer aliases artifact")
	}
	if got := observations[0].Schema[0].Name; got != "id" {
		t.Fatalf("observation schema aliases returned input: %q", got)
	}
	if got := artifacts.Compiler.Manifest.Models["model:customers"].Entities["customer"].Fields[0]; got != "id" || artifacts.Compiler.Manifest.Models["model:customers"].Checks[0].Fields != nil {
		t.Fatalf("model values alias artifact: entity=%q checks=%#v", got, artifacts.Compiler.Manifest.Models["model:customers"].Checks)
	}
}

func TestNativeQualificationInputsRejectsMissingDuplicateAndUnknownObservations(t *testing.T) {
	artifacts := qualificationInputArtifacts()
	cases := map[string][]analyticsmaterialize.SourceObservation{
		"missing": {{ID: "orders_raw", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}}},
		"duplicate canonical": {
			{ID: "orders_raw", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
			{ID: "source:orders", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
			{ID: "source:customers", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
		},
		"unknown": {
			{ID: "orders_raw", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
			{ID: "source:customers", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
			{ID: "source:unknown", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
		},
	}
	for name, observations := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := nativeQualificationInputs(artifacts, observations)
			if err == nil || !errors.Is(err, ErrNativeQualificationInvalid) {
				t.Fatalf("err = %v, want ErrNativeQualificationInvalid", err)
			}
		})
	}
}

func TestNativeQualificationInputsRejectsUnknownDataMode(t *testing.T) {
	artifacts := qualificationInputArtifacts()
	artifacts.Generation.DataMode = ""
	if _, _, err := nativeQualificationInputs(artifacts, qualificationInputObservations()); err == nil || !errors.Is(err, ErrNativeQualificationInvalid) {
		t.Fatalf("live observations with unknown data mode err = %v, want ErrNativeQualificationInvalid", err)
	}
	artifacts.Compiler.Manifest.Sources = nil
	artifacts.Compiler.Manifest.NameIndex.Sources = nil
	if _, _, err := nativeQualificationInputs(artifacts, nil); err == nil || !errors.Is(err, ErrNativeQualificationInvalid) {
		t.Fatalf("zero-source unknown data mode err = %v, want ErrNativeQualificationInvalid", err)
	}
}

func TestNativeQualificationInputsRejectsAliasCollision(t *testing.T) {
	artifacts := qualificationInputArtifacts()
	artifacts.Compiler.Manifest.NameIndex.Sources["orders_raw"] = "source:customers"
	if _, _, err := nativeQualificationInputs(artifacts, qualificationInputObservations()); err == nil || !errors.Is(err, ErrNativeQualificationInvalid) {
		t.Fatalf("alias collision err = %v, want ErrNativeQualificationInvalid", err)
	}
}

func TestNativeQualificationInputsUsesCompleteValidBaseEvidence(t *testing.T) {
	artifacts := qualificationInputArtifacts()
	artifacts.Generation.DataMode = release.GenerationDataReuseBase
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	base := release.GateEvidence{
		Version: 1, CandidateID: "base", SourceDigest: qualificationDigest("base-source"), BindingGeneration: qualificationDigest("binding"), RuntimeVersion: "runtime", DuckDBVersion: "duckdb", Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 5, MaxMillis: 1000}, Outcome: release.GateSuccess, EvaluatedAt: now,
		Sources: []release.GateSourceEvidence{
			{ID: "source:customers", Mode: "strict", SourceDigest: qualificationDigest("customers"), SchemaOutcome: release.GateSuccess, ObservedSchema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}, ObservedAt: now},
			{ID: "source:orders", Mode: "inferred", SourceDigest: qualificationDigest("orders"), SchemaOutcome: release.GateSuccess, ObservedSchema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}, ObservedAt: now},
		},
	}
	artifacts.Generation.BaseGateEvidence = &base
	sources, _, err := nativeQualificationInputs(artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[1].ObservationQueries != 0 || sources[1].ObservationRows != 0 || sources[1].ObservationMillis != 0 || sources[1].FreshnessObserved != now {
		t.Fatalf("base source inputs = %#v", sources)
	}
	if sources[1].Revision != "rev-1" || sources[1].RevisionObserved != now {
		t.Fatalf("base revision evidence = %#v", sources[1])
	}
	base.Sources[0].ObservedSchema[0].Name = "mutated"
	if sources[0].Observed[0].Name != "id" {
		t.Fatal("base evidence schema aliases returned input")
	}
}

func TestNativeQualificationInputsRequiresReuseModeForBaseFallback(t *testing.T) {
	artifacts := qualificationInputArtifacts()
	artifacts.Generation.DataMode = release.GenerationDataRefreshSources
	artifacts.Generation.BaseGateEvidence = &release.GateEvidence{
		Version: 1, CandidateID: "base", SourceDigest: qualificationDigest("base-source"), BindingGeneration: qualificationDigest("binding"), RuntimeVersion: "runtime", DuckDBVersion: "duckdb",
		Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 5, MaxMillis: 1000}, Outcome: release.GateSuccess, EvaluatedAt: time.Now().UTC(),
		Sources: []release.GateSourceEvidence{
			{ID: "source:customers", Mode: "strict", SourceDigest: qualificationDigest("customers"), SchemaOutcome: release.GateSuccess, ObservedSchema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
			{ID: "source:orders", Mode: "inferred", SourceDigest: qualificationDigest("orders"), SchemaOutcome: release.GateSuccess, ObservedSchema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
		},
	}
	if _, _, err := nativeQualificationInputs(artifacts, nil); err == nil || !errors.Is(err, ErrNativeQualificationInvalid) {
		t.Fatalf("refresh base fallback err = %v, want ErrNativeQualificationInvalid", err)
	}
}

func TestNativeQualificationInputsAllowsZeroSourceRefreshWithoutBase(t *testing.T) {
	artifacts := qualificationInputArtifacts()
	artifacts.Generation.DataMode = release.GenerationDataRefreshSources
	artifacts.Compiler.Manifest.Sources = nil
	artifacts.Compiler.Manifest.NameIndex.Sources = nil
	artifacts.Generation.BaseGateEvidence = nil
	sources, models, err := nativeQualificationInputs(artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 || len(models) != 0 {
		t.Fatalf("zero-source inputs = sources:%#v models:%#v", sources, models)
	}
}

func TestNativeQualificationInputsRejectsInvalidOrIncompleteBaseEvidence(t *testing.T) {
	artifacts := qualificationInputArtifacts()
	artifacts.Generation.DataMode = release.GenerationDataReuseBase
	base := release.GateEvidence{Version: 1, CandidateID: "base", SourceDigest: qualificationDigest("source"), BindingGeneration: qualificationDigest("binding"), RuntimeVersion: "runtime", DuckDBVersion: "duckdb", Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 5, MaxMillis: 1000}, Outcome: release.GateSuccess, EvaluatedAt: time.Now().UTC(), Sources: []release.GateSourceEvidence{{ID: "source:orders", Mode: "inferred", SourceDigest: qualificationDigest("orders"), SchemaOutcome: release.GateSuccess}}}
	artifacts.Generation.BaseGateEvidence = &base
	if _, _, err := nativeQualificationInputs(artifacts, nil); err == nil || !errors.Is(err, ErrNativeQualificationInvalid) {
		t.Fatalf("incomplete base err = %v", err)
	}
	base.Sources = []release.GateSourceEvidence{{ID: "unknown", Mode: "inferred", SourceDigest: qualificationDigest("unknown"), SchemaOutcome: release.GateSuccess}, {ID: "source:customers", Mode: "strict", SourceDigest: qualificationDigest("customers"), SchemaOutcome: release.GateSuccess}}
	if _, _, err := nativeQualificationInputs(artifacts, nil); err == nil || !errors.Is(err, ErrNativeQualificationInvalid) {
		t.Fatalf("unknown base err = %v", err)
	}
}
