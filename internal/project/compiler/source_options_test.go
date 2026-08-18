package compiler

import (
	"encoding/json"
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func TestResolveEffectiveSourceOptionsPrecedence(t *testing.T) {
	got, err := ResolveEffectiveSourceOptions(
		semanticmodel.Source{Format: "csv", Options: map[string]any{"delimiter": ";"}},
		semanticmodel.Connection{ReaderDefaults: map[string]map[string]any{"csv": {"header": true, "delimiter": ","}}},
	)
	if err != nil {
		t.Fatalf("ResolveEffectiveSourceOptions() error = %v", err)
	}
	want := map[string]any{"header": true, "delimiter": ";", "quote": `"`, "escape": `"`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective options = %#v, want %#v", got, want)
	}
}

func TestEffectiveSourceOptionsAndSchemaEvidencePersistInManifestJSON(t *testing.T) {
	nonNull := false
	source := semanticmodel.Source{
		LocationType: semanticmodel.KindPath, Path: "orders.csv", Format: "csv", Connection: "files",
		EffectiveOptions: map[string]any{"header": true}, SchemaMode: "compatible",
		Fields: map[string]semanticmodel.SourceField{"id": {Datatype: semanticmodel.DataTypeInteger, Nullable: &nonNull}},
	}
	manifest := projectmanifest.Project{Sources: map[string]semanticmodel.Source{"source:orders": source}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var roundTrip projectmanifest.Project
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	got := roundTrip.Sources["source:orders"]
	if !reflect.DeepEqual(got.EffectiveOptions, source.EffectiveOptions) || got.SchemaMode != source.SchemaMode || got.Fields["id"].Datatype != semanticmodel.DataTypeInteger || got.Fields["id"].Nullable == nil || *got.Fields["id"].Nullable {
		t.Fatalf("manifest round-trip lost compiled source evidence: %#v", got)
	}
	empty := semanticmodel.Source{EffectiveOptions: map[string]any{}}
	manifest.Sources["source:empty"] = empty
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal empty effective options: %v", err)
	}
	if err := json.Unmarshal(raw, &roundTrip); err != nil || roundTrip.Sources["source:empty"].EffectiveOptions == nil {
		t.Fatalf("empty compiled options did not persist: %v %#v", err, roundTrip.Sources["source:empty"].EffectiveOptions)
	}
}
