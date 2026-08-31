package compiler

// This is the canonical generated-dashboard compilation boundary.  A
// DashboardDocument is lowered directly into query bindings, Visual IR, and
// immutable dashboard definition state; no dashboard/authoring value is used
// as an intermediate representation.

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

// DocumentResult is the immutable result of compiling one generated
// DashboardDocument.  Normalized retains the source DTO for export and
// revision evidence; Definition is the serving-ready IR.
type DocumentResult struct {
	Normalized document.DashboardDocument
	Definition definition.Definition
}

// BuilderPreviewResult is a fail-soft draft projection. Structural, layout,
// filter, and semantic-model errors remain fatal, while individual visual
// lowering failures are keyed by visual definition ID so the builder can keep
// rendering unrelated visuals and let the author switch back or repair the
// affected field wells. Publish continues to use CompileDocument.
type BuilderPreviewResult struct {
	DocumentResult
	VisualErrors map[string]string
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

// CompileDocumentBuilderPreview compiles visual definitions independently for
// the draft builder. Authored interactions are projected separately by the
// builder and are omitted from each isolated lowering pass so a target visual
// that is temporarily invalid cannot make an otherwise valid source fail.
func CompileDocumentBuilderPreview(doc document.DashboardDocument, models map[string]*semanticmodel.Model) (BuilderPreviewResult, error) {
	if doc.Kind != document.DashboardResourceKindDashboard {
		return BuilderPreviewResult{}, fmt.Errorf("dashboard document kind %q is invalid", doc.Kind)
	}
	id := strings.TrimSpace(doc.Metadata.ID)
	if id == "" {
		return BuilderPreviewResult{}, fmt.Errorf("dashboard metadata.id is required")
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
		return BuilderPreviewResult{}, fmt.Errorf("dashboard semantic model %q is unavailable", modelID)
	}
	filters, err := CompileCanonicalDashboardFilterContract(doc, model)
	if err != nil {
		return BuilderPreviewResult{}, fmt.Errorf("compile dashboard filters: %w", err)
	}
	layout, err := CompileDashboardLayout(doc.Spec)
	if err != nil {
		return BuilderPreviewResult{}, fmt.Errorf("compile dashboard layout: %w", err)
	}
	visuals := make(map[string]visualizationdefinition.Definition, len(doc.Spec.Visuals))
	visualErrors := make(map[string]string)
	context := dashboardCompileContext{model: model, modelID: modelID}
	visualIDs := make([]string, 0, len(doc.Spec.Visuals))
	for visualID := range doc.Spec.Visuals {
		visualIDs = append(visualIDs, visualID)
	}
	sort.Strings(visualIDs)
	for _, visualID := range visualIDs {
		visual := doc.Spec.Visuals[visualID]
		visual.Interactions = nil
		compiled, compileErr := context.compileVisuals(map[string]document.DashboardVisual{visualID: visual})
		if compileErr != nil {
			visualErrors[visualID] = compileErr.Error()
			continue
		}
		visuals[visualID] = compiled[visualID]
	}
	pages := builderPreviewPages(layout.Pages, visuals)
	compiled, err := definition.New(id, valueOrString(doc.Metadata.DisplayName, doc.Metadata.Name), valueOrString(doc.Metadata.Description, ""), modelID, pages, visuals)
	if err != nil {
		return BuilderPreviewResult{}, err
	}
	compiled.Layout = &definition.Layout{Defaults: layout.Defaults, NarrowView: layout.NarrowView}
	if err := filters.ApplyToDefinition(&compiled); err != nil {
		return BuilderPreviewResult{}, err
	}
	return BuilderPreviewResult{DocumentResult: DocumentResult{Normalized: doc, Definition: compiled}, VisualErrors: visualErrors}, nil
}

func builderPreviewPages(pages []dashboard.Page, visuals map[string]visualizationdefinition.Definition) []dashboard.Page {
	result := make([]dashboard.Page, len(pages))
	for pageIndex, page := range pages {
		page.Visuals = append([]dashboard.PageVisual(nil), page.Visuals...)
		retained := page.Visuals[:0]
		for _, component := range page.Visuals {
			if component.Kind == "visual" {
				if _, ok := visuals[component.Visual]; !ok {
					continue
				}
			}
			retained = append(retained, component)
		}
		page.Visuals = retained
		result[pageIndex] = page
	}
	return result
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
