package compiler

// This is the canonical generated-dashboard compilation boundary.  A
// DashboardDocument is lowered directly into query bindings, Visual IR, and
// immutable dashboard definition state; no dashboard/authoring value is used
// as an intermediate representation.

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
)

// DocumentResult is the immutable result of compiling one generated
// DashboardDocument.  Normalized retains the source DTO for export and
// revision evidence; Definition is the serving-ready IR.
type DocumentResult struct {
	Normalized document.DashboardDocument
	Definition definition.Definition
}

// CompileDocumentFilterContract compiles only the immutable filter, page and
// layout state needed by draft filter sessions and option loading. Visual
// definitions are intentionally not lowered: a partially edited visual query
// is unrelated to this contract and must not block filter options.
func CompileDocumentFilterContract(doc document.DashboardDocument, models map[string]*semanticmodel.Model) (DocumentResult, error) {
	if doc.Kind != document.DashboardResourceKindDashboard {
		return DocumentResult{}, fmt.Errorf("dashboard document kind %q is invalid", doc.Kind)
	}
	id := strings.TrimSpace(doc.Metadata.ID)
	if id == "" {
		return DocumentResult{}, fmt.Errorf("dashboard metadata.id is required")
	}
	modelID := strings.TrimSpace(doc.Spec.SemanticModel)
	model := models[modelID]
	if model == nil {
		for key, candidate := range models {
			if candidate != nil && (key == modelID || candidate.Name == modelID) {
				modelID, model = key, candidate
				break
			}
		}
	}
	if model == nil {
		return DocumentResult{}, fmt.Errorf("dashboard semantic model %q is unavailable", modelID)
	}
	filters, err := CompileCanonicalDashboardFilterContract(doc, model)
	if err != nil {
		return DocumentResult{}, fmt.Errorf("compile dashboard filters: %w", err)
	}
	layout, err := CompileDashboardLayout(doc.Spec)
	if err != nil {
		return DocumentResult{}, fmt.Errorf("compile dashboard layout: %w", err)
	}
	compiled, err := definition.New(id, valueOrString(doc.Metadata.DisplayName, doc.Metadata.Name), valueOrString(doc.Metadata.Description, ""), modelID, layout.Pages, nil)
	if err != nil {
		return DocumentResult{}, err
	}
	compiled.Layout = &definition.Layout{Defaults: layout.Defaults, NarrowView: layout.NarrowView}
	if err := filters.ApplyToDefinition(&compiled); err != nil {
		return DocumentResult{}, err
	}
	return DocumentResult{Normalized: doc, Definition: compiled}, nil
}

// CompileDocument compiles a generated dashboard document without projecting
// through the legacy dashboard authoring package.
func CompileDocument(doc document.DashboardDocument, models map[string]*semanticmodel.Model) (DocumentResult, error) {
	if doc.Kind != document.DashboardResourceKindDashboard {
		return DocumentResult{}, fmt.Errorf("dashboard document kind %q is invalid", doc.Kind)
	}
	id := strings.TrimSpace(doc.Metadata.ID)
	if id == "" {
		return DocumentResult{}, fmt.Errorf("dashboard metadata.id is required")
	}
	modelID := strings.TrimSpace(doc.Spec.SemanticModel)
	model := models[modelID]
	if model == nil {
		for key, candidate := range models {
			if candidate != nil && (key == modelID || candidate.Name == modelID) {
				modelID, model = key, candidate
				break
			}
		}
	}
	if model == nil {
		return DocumentResult{}, fmt.Errorf("dashboard semantic model %q is unavailable", modelID)
	}
	filters, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		return DocumentResult{}, fmt.Errorf("compile dashboard filters: %w", err)
	}
	layout, err := CompileDashboardLayout(doc.Spec)
	if err != nil {
		return DocumentResult{}, fmt.Errorf("compile dashboard layout: %w", err)
	}
	visuals, err := (dashboardCompileContext{model: model, modelID: modelID}).compileVisuals(doc.Spec.Visuals)
	if err != nil {
		return DocumentResult{}, err
	}
	compiled, err := definition.New(id, valueOrString(doc.Metadata.DisplayName, doc.Metadata.Name), valueOrString(doc.Metadata.Description, ""), modelID, layout.Pages, visuals)
	if err != nil {
		return DocumentResult{}, err
	}
	compiled, err = AttachDashboardLayout(compiled, doc.Spec)
	if err != nil {
		return DocumentResult{}, err
	}
	if err := filters.ApplyToDefinition(&compiled); err != nil {
		return DocumentResult{}, err
	}
	return DocumentResult{Normalized: doc, Definition: compiled}, nil
}

// lowerCanonicalVisualSeries derives a category-series binding from the
// ordered aggregate dimensions. The canonical Dashboard query has no
// renderer-specific `series` property: the first dimension is the category
// axis and the optional second dimension is the authored series field.
