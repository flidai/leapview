package http

import (
	"context"
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type chatDraftVisual struct {
	SemanticModelID string                     `json:"semanticModelId"`
	Visual          document.DashboardVisual   `json:"visual"`
	Filters         []document.DashboardFilter `json:"filters"`
}

// A copied visual gets independent filters and no links to its source canvas.
func (input chatDraftVisual) forVisual(id string) (chatDraftVisual, error) {
	input.Visual.Interactions = nil
	filterIDs := make(map[string]string, len(input.Filters))
	for i, filter := range input.Filters {
		filterIDs[filter.ID] = fmt.Sprintf("%s_filter_%d", id, i)
	}
	filters := make([]document.DashboardFilter, 0, len(input.Filters))
	for _, source := range input.Filters {
		filter, err := application.RemapChatFilterDependencies(source, filterIDs)
		if err != nil {
			return chatDraftVisual{}, err
		}
		filter.ID = filterIDs[source.ID]
		targets := []string{id}
		filter.Targets = &targets
		filter.URLParameter = nil
		filters = append(filters, filter)
	}
	input.Filters = filters
	return input, nil
}

// Size imported visuals in pixels, then snap to the destination grid. This
// keeps chat-created dashboards compact even when a page has custom row sizes.
func chatVisualRowSpan(visualType string, spec document.DashboardSpec, page document.DashboardPage) int32 {
	rowHeight, gap := int32(48), int32(16)
	if spec.Layout != nil {
		rowHeight, gap = spec.Layout.RowHeight, spec.Layout.Gap
	}
	if page.Layout != nil {
		if page.Layout.RowHeight != nil {
			rowHeight = *page.Layout.RowHeight
		}
		if page.Layout.Gap != nil {
			gap = *page.Layout.Gap
		}
	}
	rowHeight, gap = max(1, rowHeight), max(0, gap)
	height := int32(320)
	switch visualType {
	case "kpi":
		height = 128
	case "table", "matrix", "pivot":
		height = 384
	}
	pitch := rowHeight + gap
	return max(1, (height+gap+pitch/2)/pitch)
}

// Fill the first available rectangle, leaving authored components in place.
// KPIs take a quarter row, ordinary charts share a row, and dense visuals
// retain the full width so their axes and columns remain readable.
func chatVisualPlacement(visualType string, spec document.DashboardSpec, page document.DashboardPage, startRow int32, fullWidth ...bool) document.DashboardPlacement {
	columns := int32(12)
	if spec.Layout != nil && spec.Layout.Columns > 0 {
		columns = spec.Layout.Columns
	}
	if page.Layout != nil && page.Layout.Columns != nil && *page.Layout.Columns > 0 {
		columns = *page.Layout.Columns
	}
	width := max(1, columns/2)
	switch visualType {
	case "kpi":
		width = max(1, columns/4)
	case "table", "matrix", "pivot", "map", "heatmap":
		width = columns
	}
	if len(fullWidth) > 0 && fullWidth[0] {
		width = columns
	}
	height := chatVisualRowSpan(visualType, spec, page)
	occupied := make([]document.DashboardPlacement, 0, len(page.Components))
	for _, component := range page.Components {
		if base, err := component.Base(); err == nil && base != nil {
			occupied = append(occupied, base.Placement)
		}
	}
	for row := max(1, startRow); ; row++ {
		for column := int32(1); column+width-1 <= columns; column++ {
			free := true
			for _, p := range occupied {
				if column < p.Column+p.ColumnSpan && column+width > p.Column && row < p.Row+p.RowSpan && row+height > p.Row {
					free = false
					break
				}
			}
			if free {
				return document.DashboardPlacement{Column: column, Row: row, ColumnSpan: width, RowSpan: height}
			}
		}
	}
}

type chatDraftCreator interface {
	CreateFromDocument(context.Context, authoringservice.CreateFromDocumentRequest) (authoringservice.Result, error)
}

// A chat saves authored query definitions, never the rendered result rows.
// The existing authoring service owns authorization, validation, identities,
// idempotency and the atomic private-draft write.
func (h Handler) createChatDashboard(w nethttp.ResponseWriter, r *nethttp.Request, project projectgraph.ResourceID, requestID string) {
	creator, ok := h.Authoring.(chatDraftCreator)
	if !ok {
		writeBuilderError(w, r, fmt.Errorf("dashboard creation is unavailable"))
		return
	}
	var inputs []chatDraftVisual
	if err := json.Unmarshal([]byte(r.FormValue("chatVisuals")), &inputs); err != nil || len(inputs) == 0 || len(inputs) > 100 {
		writeBuilderError(w, r, fmt.Errorf("%w: select between 1 and 100 chat visuals", authoring.ErrInvalidPayload))
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = "Chat dashboard"
	}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard,
		Metadata: document.DashboardMetadata{ID: "dashboard:chat", Name: "chat", DisplayName: &title},
		Spec:     document.DashboardSpec{SemanticModel: inputs[0].SemanticModelID, Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{}},
	}
	page := document.DashboardPage{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}
	chartCount, firstChart := 0, -1
	for i, input := range inputs {
		switch string(input.Visual.Type) {
		case "kpi", "pie", "donut", "table", "matrix", "pivot", "map", "heatmap":
		default:
			chartCount++
			if firstChart < 0 {
				firstChart = i
			}
		}
	}
	for i, input := range inputs {
		if input.SemanticModelID == "" || input.SemanticModelID != doc.Spec.SemanticModel {
			writeBuilderError(w, r, fmt.Errorf("%w: dashboard visuals must use the same semantic model", authoring.ErrInvalidPayload))
			return
		}
		id := fmt.Sprintf("visual_%d", i+1)
		if savedID := r.FormValue("savedVisualId"); savedID != "" {
			id = savedVisualComponentID(savedID, requestID)
		}
		input, err := input.forVisual(id)
		if err != nil {
			writeBuilderError(w, r, err)
			return
		}
		doc.Spec.Visuals[id] = input.Visual
		placement := chatVisualPlacement(string(input.Visual.Type), doc.Spec, page, 1, i == firstChart && (len(inputs) == 1 || chartCount >= 3 && chartCount%2 == 1))
		page.Components = append(page.Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
			DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Type: "visual", Placement: placement}, Type: "visual", Visual: id,
		}})
		doc.Spec.Filters = append(doc.Spec.Filters, input.Filters...)
	}

	doc.Spec.Pages = []document.DashboardPage{page}
	actor := h.currentActor(r)
	target := authoringAuditTarget{}
	var result authoringservice.Result
	err := executeAuthoringUIMutation(r, "createDashboardAuthoringDraft", project.String(), requestID, actor, "", "", authoring.OriginUI, access.CapabilityResourceEdit, &target, func(ctx context.Context) error {
		var err error
		result, err = creator.CreateFromDocument(ctx, authoringservice.CreateFromDocumentRequest{
			ProjectID: project, ActorID: actor, Document: doc, Title: title, Slug: "chat-" + requestID, Origin: authoring.OriginUI, IdempotencyKey: requestID,
		})
		if err == nil {
			target.dashboardID = result.Lifecycle.ID.String()
			target.draftID = draftIDFromLifecycle(result.Lifecycle)
		}
		return err
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if err := result.Lifecycle.Validate(); err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if h.chatDashboardReceipt(w, r, result.Lifecycle.ID.String()) {
		return
	}
	href := dashboardBuilderDraftRoute(result.Lifecycle.ID.String(), draftIDFromLifecycle(result.Lifecycle), "/edit")
	separator := "?"
	if strings.Contains(href, "?") {
		separator = "&"
	}
	nethttp.Redirect(w, r, href+separator+"embed=chat", nethttp.StatusSeeOther)
}
