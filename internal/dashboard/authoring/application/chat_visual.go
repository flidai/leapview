package application

import (
	"context"
	"crypto/sha256"
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
	visualID, err := importedChatVisualID(source, commandID)
	if err != nil {
		return false
	}
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
	visualID, err := importedChatVisualID(source, commandID)
	if err != nil {
		return false, err
	}
	for existingID := range value.Spec.Visuals {
		if strings.HasPrefix(existingID, chatVisualImportPrefix(source.ArtifactID, commandID)+"_") && existingID != visualID {
			return false, fmt.Errorf("%w: imported filter intent changed during retry", authoring.ErrCommandReuse)
		}
	}
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

// The original filter declarations are part of the retained visual identity.
// Destination ID/URL remapping is lossy, so comparing only the imported filters
// cannot distinguish a retry from source arguments that changed to those aliases.
func importedChatVisualID(source ChatVisualImport, commandID authoring.CommandID) (string, error) {
	intent := struct {
		ArtifactID      string
		ToolCallID      string
		SemanticModelID projectgraph.ResourceID
		Filters         []document.DashboardFilter
	}{source.ArtifactID, source.ToolCallID, source.SemanticModelID, source.Filters}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return "", fmt.Errorf("%w: encode chat visual filter intent: %v", authoring.ErrInvalidPayload, err)
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%s_%x", chatVisualImportPrefix(source.ArtifactID, commandID), digest[:16]), nil
}

func chatVisualImportPrefix(artifactID string, commandID authoring.CommandID) string {
	// Leave room for the digest and the page component's _component suffix
	// under the canonical dashboard object's 128-character identity limit.
	artifact := safeObjectIDSuffix(artifactID, 32)
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
	type importedFilter struct {
		sourceID string
		filter   document.DashboardFilter
	}
	imports := make([]importedFilter, 0, len(filters))
	sourceIDs := make(map[string]struct{}, len(filters))
	for _, filter := range filters {
		sourceIDs[filter.ID] = struct{}{}
	}
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
				if targets[index] == sourceVisualID || targets[index] == "page/visual" {
					targets[index] = importedVisualID
					appliesToSource = true
				}
			}
			if !appliesToSource {
				// Preserve an explicit target list that excludes this source
				// visual by not importing the inapplicable filter.
				continue
			}
			copy.Targets = stringSlicePointer(uniqueStrings(targets))
		}
		imports = append(imports, importedFilter{sourceID: filter.ID, filter: copy})
	}
	if len(imports) == 0 {
		return nil
	}

	// A filter whose options depend on a source filter that is out of scope
	// cannot retain its source behavior. Drop it and any cascading dependents.
	eligible := make(map[string]bool, len(imports))
	for _, imported := range imports {
		eligible[imported.sourceID] = true
	}
	for changed := true; changed; {
		changed = false
		for _, imported := range imports {
			if !eligible[imported.sourceID] {
				continue
			}
			for _, dependency := range chatFilterOptionDependencies(imported.filter) {
				if _, known := sourceIDs[dependency]; !known {
					return fmt.Errorf("%w: imported filter %q depends on unknown filter %q", authoring.ErrInvalidPayload, imported.sourceID, dependency)
				}
				if !eligible[dependency] {
					eligible[imported.sourceID] = false
					changed = true
					break
				}
			}
		}
	}
	filtered := imports[:0]
	for _, imported := range imports {
		if eligible[imported.sourceID] {
			filtered = append(filtered, imported)
		}
	}
	imports = filtered
	if len(imports) == 0 {
		return nil
	}

	// First map every source ID, recognizing an earlier import of this same
	// scoped filter before allocating a collision suffix. Repeating the pass
	// resolves dependencies even when their parent filter appears later.
	filterIDs := make(map[string]string, len(imports))
	for _, imported := range imports {
		filterIDs[imported.sourceID] = safeChatFilterID(imported.filter.ID)
	}
	for pass := 0; pass <= len(imports); pass++ {
		changed := false
		claimed := make(map[string]struct{}, len(imports))
		for _, imported := range imports {
			candidate, err := remapChatFilterDependencies(imported.filter, filterIDs)
			if err != nil {
				return fmt.Errorf("clone imported dashboard filter dependencies: %w", err)
			}
			var matched *document.DashboardFilter
			for index := range value.Spec.Filters {
				existing := &value.Spec.Filters[index]
				if _, used := claimed[existing.ID]; used || !chatFilterIDDerivedFromSource(existing.ID, imported.sourceID) {
					continue
				}
				equivalent, err := chatFilterImportShapeEquivalent(*existing, candidate)
				if err != nil {
					return fmt.Errorf("compare imported dashboard filter identity: %w", err)
				}
				if !equivalent {
					continue
				}
				matched = existing
				break
			}
			if matched != nil {
				if filterIDs[imported.sourceID] != matched.ID {
					filterIDs[imported.sourceID] = matched.ID
					changed = true
				}
				claimed[matched.ID] = struct{}{}
				continue
			}

			id := filterIDs[imported.sourceID]
			if _, alreadyClaimed := claimed[id]; chatFilterIDExists(value.Spec.Filters, id) || alreadyClaimed {
				id = availableChatFilterID(value.Spec.Filters, claimed, sourceIDs, imported.filter.ID)
				if filterIDs[imported.sourceID] != id {
					filterIDs[imported.sourceID] = id
					changed = true
				}
			}
			claimed[id] = struct{}{}
		}
		if !changed {
			break
		}
	}

	usedURLParameters := make(map[string]int)
	for _, filter := range value.Spec.Filters {
		if filter.URLParameter != nil {
			usedURLParameters[*filter.URLParameter]++
		}
	}
	for _, page := range value.Spec.Pages {
		if page.FilterBindings == nil {
			continue
		}
		for _, binding := range *page.FilterBindings {
			if binding.URLParameter != nil {
				usedURLParameters[*binding.URLParameter]++
			}
		}
	}
	for _, imported := range imports {
		copy, err := remapChatFilterDependencies(imported.filter, filterIDs)
		if err != nil {
			return fmt.Errorf("clone imported dashboard filter dependencies: %w", err)
		}
		copy.ID = filterIDs[imported.sourceID]
		existing := chatFilterByID(value.Spec.Filters, copy.ID)
		if existing != nil && chatFilterImportEquivalent(*existing, copy) && copy.URLParameter != nil {
			// Recompute the URL parameter with this retained import's existing
			// value removed. That keeps a retry deterministic while still making
			// a changed source URL parameter fail replay matching.
			usedWithoutExisting := copyChatURLParameterCounts(usedURLParameters)
			if existing.URLParameter != nil {
				usedWithoutExisting[*existing.URLParameter]--
			}
			parameter := uniqueChatURLParameter(usedWithoutExisting, *copy.URLParameter, copy.ID)
			copy.URLParameter = &parameter
		} else if copy.URLParameter != nil {
			parameter := uniqueChatURLParameter(usedURLParameters, *copy.URLParameter, copy.ID)
			copy.URLParameter = &parameter
		}
		if chatFilterPresent(value.Spec.Filters, copy) {
			continue
		}
		if copy.URLParameter != nil {
			usedURLParameters[*copy.URLParameter]++
		}
		value.Spec.Filters = append(value.Spec.Filters, copy)
	}
	return nil
}

func safeChatFilterID(value string) string {
	base := safeObjectIDSuffix(value, 96)
	if base == "" {
		base = "chat_filter"
	}
	return base
}

func availableChatFilterID(filters []document.DashboardFilter, claimed, sourceIDs map[string]struct{}, sourceID string) string {
	base := safeChatFilterID(sourceID)
	for suffix := 1; suffix < 10000; suffix++ {
		candidate := fmt.Sprintf("%.110s_%d", base, suffix)
		if _, reserved := sourceIDs[candidate]; reserved {
			continue
		}
		if !chatFilterIDExists(filters, candidate) {
			if _, exists := claimed[candidate]; !exists {
				return candidate
			}
		}
	}
	return fmt.Sprintf("%.96s_imported", base)
}

func chatFilterImportEquivalent(existing, candidate document.DashboardFilter) bool {
	existing.ID, candidate.ID = "", ""
	existing.URLParameter, candidate.URLParameter = nil, nil
	return reflect.DeepEqual(existing, candidate)
}

func chatFilterImportShapeEquivalent(existing, candidate document.DashboardFilter) (bool, error) {
	existing, err := cloneChatFilter(existing)
	if err != nil {
		return false, err
	}
	candidate, err = cloneChatFilter(candidate)
	if err != nil {
		return false, err
	}
	for _, filter := range []*document.DashboardFilter{&existing, &candidate} {
		for _, options := range chatFilterOptions(*filter) {
			options.DependsOn = nil
		}
	}
	return chatFilterImportEquivalent(existing, candidate), nil
}

func chatFilterIDDerivedFromSource(candidateID, sourceID string) bool {
	base := safeChatFilterID(sourceID)
	if candidateID == base {
		return true
	}
	suffix, found := strings.CutPrefix(candidateID, base+"_")
	if !found || suffix == "" {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func chatFilterByID(filters []document.DashboardFilter, id string) *document.DashboardFilter {
	for index := range filters {
		if filters[index].ID == id {
			return &filters[index]
		}
	}
	return nil
}

func remapChatFilterDependencies(filter document.DashboardFilter, ids map[string]string) (document.DashboardFilter, error) {
	copy, err := cloneChatFilter(filter)
	if err != nil {
		return document.DashboardFilter{}, err
	}
	for _, options := range chatFilterOptions(copy) {
		if options == nil || options.DependsOn == nil {
			continue
		}
		dependencies := append([]string(nil), (*options.DependsOn)...)
		for index, dependency := range dependencies {
			if mapped, exists := ids[dependency]; exists {
				dependencies[index] = mapped
			}
		}
		options.DependsOn = &dependencies
	}
	return copy, nil
}

func chatFilterOptions(filter document.DashboardFilter) []*document.DistinctDashboardFilterOptions {
	var result []*document.DistinctDashboardFilterOptions
	switch control := filter.Control.Value.(type) {
	case *document.SingleSelectDashboardFilterControl:
		if control.Options != nil {
			if options, ok := control.Options.Value.(*document.DistinctDashboardFilterOptions); ok {
				result = append(result, options)
			}
		}
	case *document.MultiSelectDashboardFilterControl:
		if control.Options != nil {
			if options, ok := control.Options.Value.(*document.DistinctDashboardFilterOptions); ok {
				result = append(result, options)
			}
		}
	}
	return result
}

func chatFilterOptionDependencies(filter document.DashboardFilter) []string {
	var dependencies []string
	for _, options := range chatFilterOptions(filter) {
		if options != nil && options.DependsOn != nil {
			dependencies = append(dependencies, (*options.DependsOn)...)
		}
	}
	return dependencies
}

func uniqueChatURLParameter(used map[string]int, requested, filterID string) string {
	if used[requested] <= 0 {
		return requested
	}
	suffix := safeObjectIDSuffix(filterID, 28)
	if suffix == "" {
		suffix = "chat_filter"
	}
	for index := 0; index < 10000; index++ {
		discriminator := "_" + suffix
		if index > 0 {
			discriminator += fmt.Sprintf("_%d", index)
		}
		prefix := requested
		if len(prefix)+len(discriminator) > 64 {
			prefix = prefix[:64-len(discriminator)]
		}
		prefix = strings.TrimRight(prefix, "-_")
		if prefix == "" {
			prefix = "filter"
		}
		candidate := prefix + discriminator
		if candidate[0] < 'A' || (candidate[0] > 'Z' && candidate[0] < 'a') || candidate[0] > 'z' {
			candidate = "filter_" + candidate
			if len(candidate) > 64 {
				candidate = candidate[:64]
			}
		}
		if used[candidate] <= 0 {
			return candidate
		}
	}
	return fmt.Sprintf("filter_%s", safeObjectIDSuffix(filterID, 32))
}

func copyChatURLParameterCounts(value map[string]int) map[string]int {
	copy := make(map[string]int, len(value))
	for parameter, count := range value {
		copy[parameter] = count
	}
	return copy
}

func stringSlicePointer(value []string) *[]string {
	return &value
}

func uniqueStrings(value []string) []string {
	seen := make(map[string]struct{}, len(value))
	result := make([]string, 0, len(value))
	for _, item := range value {
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
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
