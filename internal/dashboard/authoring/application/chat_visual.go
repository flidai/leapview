package application

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ChatVisualImport is the exact authored visual source recovered from one
// persisted query_visual tool call.
type ChatVisualImport struct {
	ArtifactID      string
	ToolCallID      string
	SemanticModelID projectgraph.ResourceID
	Visual          document.DashboardVisual
	Filters         []document.DashboardFilter
}

// AddChatVisualRequest identifies a governed editable draft and its immutable
// source revision. Provenance and command identity are supplied by the server.
type AddChatVisualRequest struct {
	ProjectID   projectgraph.ResourceID
	ActorID     string
	DashboardID authoring.DashboardID
	PageID      string
	Source      ChatVisualImport
	CommandID   authoring.CommandID
	Provenance  authoring.Provenance
}

// AddChatVisualToDraft appends one complete visual definition and its visible
// page component with a single audited ReplaceDocument command.
func (a *Application) AddChatVisualToDraft(ctx context.Context, request AddChatVisualRequest) (authoringservice.Result, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, err
	}
	if err := request.CommandID.Validate(); err != nil {
		return authoringservice.Result{}, err
	}
	read, err := a.Draft(ctx, DraftRequest{ProjectID: request.ProjectID, ActorID: request.ActorID, DashboardID: request.DashboardID})
	if err != nil {
		return authoringservice.Result{}, err
	}
	targetModel, err := graphID(read.Revision.Document.Spec.SemanticModel)
	if err != nil || targetModel != request.Source.SemanticModelID {
		return authoringservice.Result{}, fmt.Errorf("%w: visual semantic model does not match dashboard", authoring.ErrInvalidPayload)
	}
	documentValue, err := read.Revision.Document.Clone()
	if err != nil {
		return authoringservice.Result{}, err
	}
	if replayRepository, ok := a.repository.(authoring.CommandReplayRepository); ok {
		replayed, found, lookupErr := replayRepository.LookupCommandReplay(ctx, request.ProjectID, request.DashboardID, request.CommandID)
		if lookupErr != nil {
			return authoringservice.Result{}, lookupErr
		}
		if found {
			if replayed.Revision.IsZero() {
				return authoringservice.Result{}, fmt.Errorf("%w: chat visual command has no retained result revision", authoring.ErrInvalidAuthoring)
			}
			resultRevision, revisionErr := a.repository.GetRevision(ctx, request.ProjectID, request.DashboardID, replayed.Revision.RevisionID)
			if revisionErr != nil {
				return authoringservice.Result{}, revisionErr
			}
			if err := resultRevision.Validate(); err != nil {
				return authoringservice.Result{}, err
			}
			if resultRevision.DashboardID != request.DashboardID || !sameRevision(resultRevision.Token(), replayed.Revision) ||
				!sameChatVisualProvenance(resultRevision.Provenance, request.Provenance) ||
				!chatVisualReplayMatches(resultRevision.Document, request.PageID, request.Source, request.CommandID) {
				return authoringservice.Result{}, authoring.ErrCommandReuse
			}
			return authoringservice.Result{Revision: replayed.Revision, Lifecycle: read.Lifecycle}, nil
		}
	}
	alreadyAdded, err := AddChatVisualToDocument(&documentValue, request.PageID, request.Source, request.CommandID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	if alreadyAdded {
		return authoringservice.Result{Revision: read.Revision.Token(), Lifecycle: read.Lifecycle}, nil
	}
	if err := authoring.ValidateCanonicalDocument(documentValue); err != nil {
		return authoringservice.Result{}, fmt.Errorf("%w: imported visual is not a canonical dashboard document: %v", authoring.ErrInvalidPayload, err)
	}
	command := authoring.Command{
		ID: request.CommandID, DashboardID: request.DashboardID, DraftID: read.Lifecycle.Draft.ID,
		ExpectedRevision: read.Revision.Token(), Provenance: request.Provenance,
		ReplaceDocument: &authoring.ReplaceDocumentPayload{Document: documentValue},
	}
	project, err := projectID(request.ProjectID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	return a.authoring.Execute(ctx, project, command)
}

func sameChatVisualProvenance(stored, requested authoring.Provenance) bool {
	return stored.Origin == authoring.OriginAgent && stored.ActorID == requested.ActorID &&
		stored.ConversationID == requested.ConversationID && stored.ToolCallID == requested.ToolCallID
}

func chatVisualReplayMatches(value document.DashboardDocument, pageID string, source ChatVisualImport, commandID authoring.CommandID) bool {
	if value.Spec.SemanticModel != string(source.SemanticModelID) {
		return false
	}
	visualID := importedChatVisualID(source.ArtifactID, commandID)
	visual, exists := value.Spec.Visuals[visualID]
	if !exists || !reflect.DeepEqual(visual, source.Visual) {
		return false
	}
	componentID := visualID + "_component"
	foundComponent := false
	for _, page := range value.Spec.Pages {
		for _, component := range page.Components {
			base, err := component.Base()
			if err != nil || base == nil || base.ID != componentID {
				continue
			}
			visualComponent, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok || visualComponent.Visual != visualID || page.ID != pageID || foundComponent {
				return false
			}
			foundComponent = true
		}
	}
	return foundComponent && chatVisualFiltersPresent(value, source.Filters, source.ArtifactID, visualID)
}

// AddChatVisualToDocument adds the source visual and all authored filter
// semantics to a detached dashboard document. commandID keeps generated IDs
// stable across retries while allowing the same artifact to be added twice
// deliberately with distinct command identities.
func AddChatVisualToDocument(value *document.DashboardDocument, pageID string, source ChatVisualImport, commandID authoring.CommandID) (bool, error) {
	if value == nil {
		return false, fmt.Errorf("%w: dashboard document is required", authoring.ErrInvalidPayload)
	}
	if err := commandID.Validate(); err != nil {
		return false, err
	}
	if strings.TrimSpace(source.ArtifactID) == "" || strings.TrimSpace(string(source.SemanticModelID)) == "" {
		return false, fmt.Errorf("%w: visual artifact and semantic model are required", authoring.ErrInvalidPayload)
	}
	if err := source.SemanticModelID.Validate(); err != nil {
		return false, fmt.Errorf("%w: visual semantic model is invalid", authoring.ErrInvalidPayload)
	}
	pageIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == pageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return false, fmt.Errorf("%w: dashboard page %q was not found", authoring.ErrNotFound, pageID)
	}
	visualID := importedChatVisualID(source.ArtifactID, commandID)
	componentID := visualID + "_component"
	if existing, exists := value.Spec.Visuals[visualID]; exists && reflect.DeepEqual(existing, source.Visual) {
		for pageNumber := range value.Spec.Pages {
			for _, component := range value.Spec.Pages[pageNumber].Components {
				base, err := component.Base()
				if err != nil || base == nil || base.ID != componentID {
					continue
				}
				visual, ok := component.Value.(*document.VisualDashboardPageComponent)
				if !ok || visual.Visual != visualID || pageNumber != pageIndex {
					return false, fmt.Errorf("%w: retry selects a different dashboard page", authoring.ErrCommandReuse)
				}
				if !chatVisualFiltersPresent(*value, source.Filters, source.ArtifactID, visualID) {
					return false, fmt.Errorf("%w: imported visual filters changed during retry", authoring.ErrCommandReuse)
				}
				return true, nil
			}
		}
	}
	if value.Spec.Visuals == nil {
		value.Spec.Visuals = map[string]document.DashboardVisual{}
	}
	if _, exists := value.Spec.Visuals[visualID]; exists {
		return false, fmt.Errorf("%w: imported visual identity conflicts with existing content", authoring.ErrCommandReuse)
	}
	value.Spec.Visuals[visualID] = source.Visual
	if err := appendChatVisualFilters(value, source.Filters, source.ArtifactID, visualID); err != nil {
		return false, err
	}
	page := &value.Spec.Pages[pageIndex]
	component := document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{
			ID: componentID, Type: "visual", Placement: nextChatVisualPlacement(*value, *page, source.Visual.Type),
		},
		Type: "visual", Visual: visualID,
	}}
	page.Components = append(page.Components, component)
	return false, nil
}

func importedChatVisualID(artifactID string, commandID authoring.CommandID) string {
	artifact := safeObjectIDSuffix(artifactID, 48)
	command := safeObjectIDSuffix(commandID.String(), 36)
	if artifact == "" {
		artifact = "artifact"
	}
	return "chat_visual_" + artifact + "_" + command
}

func safeObjectIDSuffix(value string, limit int) string {
	var result strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9', character == '_', character == '-':
			result.WriteRune(character)
		}
		if result.Len() >= limit {
			break
		}
	}
	return strings.Trim(result.String(), "-_")
}

func nextChatVisualPlacement(value document.DashboardDocument, page document.DashboardPage, visualType document.DashboardVisualType) document.DashboardPlacement {
	columns := int32(12)
	if value.Spec.Layout != nil && value.Spec.Layout.Columns > 0 {
		columns = value.Spec.Layout.Columns
	}
	if page.Layout != nil && page.Layout.Columns != nil && *page.Layout.Columns > 0 {
		columns = *page.Layout.Columns
	}
	span, rows := chatVisualPlacementSize(visualType)
	if columns < span {
		span = columns
	}
	bottom := int64(1)
	for _, component := range page.Components {
		base, err := component.Base()
		if err != nil || base == nil {
			continue
		}
		candidate := int64(base.Placement.Row) + int64(base.Placement.RowSpan)
		if candidate > bottom {
			bottom = candidate
		}
	}
	if bottom > int64(1<<31-1) {
		bottom = int64(1<<31 - 1)
	}
	return document.DashboardPlacement{Column: 1, Row: int32(bottom), ColumnSpan: span, RowSpan: rows}
}

func chatVisualPlacementSize(visualType document.DashboardVisualType) (int32, int32) {
	switch visualType {
	case document.DashboardVisualTypeKpi, document.DashboardVisualTypeGauge:
		return 4, 3
	case document.DashboardVisualTypeTree:
		return 6, 6
	case document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return 6, 5
	default:
		return 6, 4
	}
}

func appendChatVisualFilters(value *document.DashboardDocument, filters []document.DashboardFilter, sourceVisualID, importedVisualID string) error {
	for _, filter := range filters {
		copy, err := cloneChatFilter(filter)
		if err != nil {
			return fmt.Errorf("clone imported dashboard filter: %w", err)
		}
		if copy.Targets == nil {
			// query_visual renders a standalone dashboard containing only this
			// visual, so an unscoped filter applies only to the imported visual.
			// Keeping it unscoped on the destination would change other visuals.
			targets := []string{importedVisualID}
			copy.Targets = &targets
		} else {
			targets := append([]string(nil), (*copy.Targets)...)
			appliesToSource := false
			for index := range targets {
				if targets[index] == sourceVisualID {
					targets[index] = importedVisualID
					appliesToSource = true
				}
			}
			if !appliesToSource {
				// Preserve an explicit target list that excludes this source
				// visual by not importing the inapplicable filter.
				continue
			}
			copy.Targets = &targets
		}
		copy.ID = uniqueChatFilterID(*value, copy)
		if chatFilterPresent(value.Spec.Filters, copy) {
			continue
		}
		value.Spec.Filters = append(value.Spec.Filters, copy)
	}
	return nil
}

func uniqueChatFilterID(value document.DashboardDocument, filter document.DashboardFilter) string {
	base := safeObjectIDSuffix(filter.ID, 96)
	if base == "" {
		base = "chat_filter"
	}
	for suffix := 0; suffix < 10000; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate = fmt.Sprintf("%.110s_%d", base, suffix)
		}
		filter.ID = candidate
		if chatFilterPresent(value.Spec.Filters, filter) {
			return candidate
		}
		if !chatFilterIDExists(value.Spec.Filters, candidate) {
			return candidate
		}
	}
	return base + "_imported"
}

func chatFilterIDExists(filters []document.DashboardFilter, id string) bool {
	for _, filter := range filters {
		if filter.ID == id {
			return true
		}
	}
	return false
}

func chatFilterPresent(filters []document.DashboardFilter, candidate document.DashboardFilter) bool {
	for _, filter := range filters {
		if filter.ID == candidate.ID && reflect.DeepEqual(filter, candidate) {
			return true
		}
	}
	return false
}

func chatVisualFiltersPresent(value document.DashboardDocument, filters []document.DashboardFilter, sourceVisualID, importedVisualID string) bool {
	copy := value
	before := len(copy.Spec.Filters)
	if err := appendChatVisualFilters(&copy, filters, sourceVisualID, importedVisualID); err != nil {
		return false
	}
	return len(copy.Spec.Filters) == before
}

func cloneChatFilter(value document.DashboardFilter) (document.DashboardFilter, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return document.DashboardFilter{}, err
	}
	var cloned document.DashboardFilter
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return document.DashboardFilter{}, err
	}
	return cloned, nil
}

func graphID(value string) (projectgraph.ResourceID, error) {
	id, err := projectgraph.NewResourceID(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return id, nil
}
