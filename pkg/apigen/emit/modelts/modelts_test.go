package modelts

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestEmit_GeneratesContractInterfaces(t *testing.T) {
	doc := ir.Document{
		Contracts: []ir.Contract{{Name: "DashboardEnvelope", Schema: ir.SchemaRef{Ref: "DashboardEnvelope"}}},
		Schemas: map[string]ir.Schema{
			"DashboardEnvelope": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"page":    {Schema: ir.SchemaRef{Ref: "DashboardPageSignal"}},
					"visuals": {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Ref: "DashboardVisual"}}}},
				},
				PropertyOrder: []string{"page", "visuals"},
				Required:      []string{"page", "visuals"},
			},
			"DashboardPageSignal": {Type: "object", Properties: map[string]ir.SchemaProperty{"pageId": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"pageId"}},
			"DashboardVisual": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"id":   {Schema: ir.SchemaRef{Type: "string"}},
					"data": {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{}}}},
					"note": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"id", "data"},
			},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "export interface DashboardEnvelope")
	require.Contains(t, content, "page: DashboardPageSignal")
	require.Contains(t, content, "visuals: Record<string, DashboardVisual>")
	require.Contains(t, content, "data: Record<string, unknown>")
	require.Contains(t, content, "note?: string")
	require.Contains(t, content, "export type DataContractEnvelope = DashboardEnvelope")
}

func TestEmit_RendersNumericLiteralReferences(t *testing.T) {
	constant := 1.0
	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"ExplorationSpec": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"schemaVersion": {Schema: ir.SchemaRef{Type: "integer", Const: &constant}},
				},
				Required: []string{"schemaVersion"},
			},
		},
		Contracts: []ir.Contract{{Name: "ExplorationSpec", Schema: ir.SchemaRef{Ref: "ExplorationSpec"}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Contains(t, string(b), "schemaVersion: 1")
}

func TestEmit_GeneratesDiscriminatedUnion(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "LeapViewSignals"},
		Schemas: map[string]ir.Schema{
			"Visual":      {Type: "union", OneOf: []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}}, Discriminator: &ir.Discriminator{PropertyName: "shape", Mapping: map[string]string{"chart": "ChartVisual", "text": "TextVisual"}}},
			"VisualBase":  {Type: "object", Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"shape"}},
			"ChartVisual": {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}}}, Required: []string{"shape"}},
			"TextVisual":  {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}}}, Required: []string{"shape"}},
		},
		Contracts: []ir.Contract{{Name: "visual", Schema: ir.SchemaRef{Ref: "Visual"}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "export type Visual = ChartVisual | TextVisual")
	require.Contains(t, content, "export type ChartVisual = VisualBase & {")
	require.NotContains(t, content, "export interface ChartVisual extends VisualBase")
	require.Contains(t, content, "shape: 'chart'")
}

func TestEmit_UsesIntersectionForUnionBase(t *testing.T) {
	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"PathSourceLocation":        {Type: "union", OneOf: []ir.SchemaRef{{Ref: "CSVPathSourceLocation"}, {Ref: "JSONPathSourceLocation"}}},
			"CSVPathSourceLocation":     {Type: "object", Properties: map[string]ir.SchemaProperty{"type": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"type"}},
			"JSONPathSourceLocation":    {Type: "object", Properties: map[string]ir.SchemaProperty{"type": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"type"}},
			"SourceLocationPathVariant": {Type: "object", Base: &ir.SchemaRef{Ref: "PathSourceLocation"}, Properties: map[string]ir.SchemaProperty{"path": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"path"}},
		},
		Contracts: []ir.Contract{{Name: "SourceLocationPathVariant", Schema: ir.SchemaRef{Ref: "SourceLocationPathVariant"}}},
	}
	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "export type SourceLocationPathVariant = PathSourceLocation & {")
	require.NotContains(t, content, "export interface SourceLocationPathVariant extends PathSourceLocation")
}

func TestEmit_ReferencesImportedContractNamespaceWithoutRegeneratingIt(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "LeapViewSignals"},
		Schemas: map[string]ir.Schema{
			"DashboardEnvelope": {
				Type: "object", Namespace: "LeapViewSignals",
				Properties: map[string]ir.SchemaProperty{"visual": {Schema: ir.SchemaRef{Ref: "VisualizationEnvelope"}}},
				Required:   []string{"visual"},
			},
			"VisualizationEnvelope": {Type: "object", Namespace: "LeapViewVisualization"},
		},
		Contracts: []ir.Contract{{Name: "DashboardEnvelope", Schema: ir.SchemaRef{Ref: "DashboardEnvelope"}}},
	}

	b, err := Emit(doc, Options{ContractImports: map[string]ContractImport{
		"LeapViewVisualization": {TypeScriptModule: "../visualization"},
	}})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, `import type * as LeapViewVisualization from '../visualization'`)
	require.Contains(t, content, "visual: LeapViewVisualization.VisualizationEnvelope")
	require.NotContains(t, content, "export interface VisualizationEnvelope")
}

func TestEmit_RendersScalarUnion(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Contracts"},
		Schemas: map[string]ir.Schema{
			"JsonScalar": {Type: "union", Namespace: "Contracts", OneOf: []ir.SchemaRef{{Type: "string"}, {Type: "number"}, {Type: "boolean"}, {Type: "null"}}},
			"Mapping":    {Type: "object", Namespace: "Contracts", Properties: map[string]ir.SchemaProperty{"value": {Schema: ir.SchemaRef{Ref: "JsonScalar"}}}, Required: []string{"value"}},
		},
		Contracts: []ir.Contract{{Name: "Mapping", Schema: ir.SchemaRef{Ref: "Mapping"}}},
	}
	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Contains(t, string(b), "export type JsonScalar = string | number | boolean | null")
}
