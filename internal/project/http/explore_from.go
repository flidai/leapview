package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/explorationadapter"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectui "github.com/flidai/leapview/internal/project/ui"
	"github.com/go-chi/chi/v5"
)

// ErrExploreHandoffIncompatible marks an authorized authored visual that the
// canonical exploration contract cannot represent. It is intentionally
// distinct from authorization/not-found errors so the browser can return a
// useful 422 without disclosing inaccessible source metadata.
var ErrExploreHandoffIncompatible = errors.New("dashboard visual is incompatible with data explorer")

// AuthenticatedDashboardSource is the narrow source capability needed by a
// dashboard-to-explorer handoff. Implementations must authorize VIEW before
// reading the authored source and must never resolve a draft for this path.
type AuthenticatedDashboardSource interface {
	LoadDashboardSource(context.Context, projectgraph.ResourceID, authoring.DashboardID, string) (sourceadapter.Source, error)
}

// AuthenticatedPublishedDashboardSource is the explicit authored-repository
// source path. It is distinct from SourceProject, which reads the retained
// published project artifact. Neither method may resolve a draft for an
// exploration handoff.
type AuthenticatedPublishedDashboardSource interface {
	LoadPublishedDashboardSource(context.Context, projectgraph.ResourceID, authoring.DashboardID, string) (sourceadapter.Source, error)
}

// AuthenticatedDashboardSourceKindResolver resolves source ownership through
// the authorized dashboard catalog. URL possession cannot select an instance
// or project source, and drafts are not candidates for this operation.
type AuthenticatedDashboardSourceKindResolver interface {
	ResolveDashboardSourceKind(context.Context, projectgraph.ResourceID, authoring.DashboardID, string) (sourceadapter.SourceKind, error)
}

// AuthorizedExploreModelReader is the handoff read capability. Its
// implementation must acquire one active serving lease, authorize the actor's
// RESOURCE_USE capability against the requested semantic-model graph node,
// then return the model and compiled planner retained by that same lease.
// Handoff code never accepts the unrestricted ProjectDefinitionSnapshot as a
// substitute: doing so would expose model metadata before authorization and
// could pair a source with a different serving generation.
type AuthorizedExploreModelReader interface {
	AuthorizedExploreModel(context.Context, projectgraph.ResourceID, string, string) (*semanticmodel.Model, *semanticquery.CompiledModel, projectgraph.ServingIdentity, error)
}

// ExploreFromDashboardRequest is populated by server-side route wiring. The
// project ID comes from the bound project and the dashboard/visual IDs come
// from route parameters; neither is accepted from an arbitrary query state.
type ExploreFromDashboardRequest struct {
	ProjectID   projectgraph.ResourceID
	DashboardID authoring.DashboardID
	PageID      string
	ComponentID string
	VisualID    string
	ActorID     string
	SourceKind  sourceadapter.SourceKind
	Return      ExploreReturnContext
	// State carries only the current viewer's typed filter/time values. It is
	// merged into the authored visual after the source/model authorization
	// boundary and cannot replace dimensions, metrics, or dataset identity.
	State *DashboardExploreState
}

type DashboardExploreState struct {
	ModelID   string
	DatasetID *string
	Filters   []exploration.ExplorationFilter
	// FilterBindingIDs is parallel to Filters when the browser supplied a
	// scoped dashboard-control overlay. It is intentionally not part of the
	// canonical ExplorationSpec and never grants access by itself.
	FilterBindingIDs []string
	Time             *exploration.ExplorationTimeSelection
}

type ExploreHandoffResult struct {
	// URL is a canonical /explore v2 URL containing only the reconstructed
	// authored ExplorationSpec. The destination reruns it under its viewer's
	// current authorization and serving generation.
	URL        string
	ReturnPath string
	Spec       exploration.ExplorationSpec
}

type ExploreFromDashboardOptions struct {
	Sources    AuthenticatedDashboardSource
	Definition ProjectDefinitionReader
}

// ExploreFromDashboard performs the authenticated source handoff. Source
// authorization and source loading happen before active definition lookup;
// this preserves the source adapter's disclosure boundary. The loaded source
// is then converted using the current viewer's active model/planner pair.
func ExploreFromDashboard(ctx context.Context, options ExploreFromDashboardOptions, request ExploreFromDashboardRequest) (ExploreHandoffResult, error) {
	if options.Sources == nil {
		return ExploreHandoffResult{}, errors.New("dashboard source capability is unavailable")
	}
	if options.Definition == nil {
		return ExploreHandoffResult{}, errors.New("active project definition capability is unavailable")
	}
	if err := request.ProjectID.Validate(); err != nil {
		return ExploreHandoffResult{}, fmt.Errorf("project id: %w", err)
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return ExploreHandoffResult{}, err
	}
	if strings.TrimSpace(request.ComponentID) == "" && strings.TrimSpace(request.VisualID) == "" {
		return ExploreHandoffResult{}, errors.New("visual id is required")
	}
	actorID := strings.TrimSpace(request.ActorID)
	if actorID == "" {
		return ExploreHandoffResult{}, errors.New("actor id is required")
	}
	request.Return = dashboardReturnWithRequestPage(request.Return, request.DashboardID, request.PageID)
	returnPath, err := request.Return.Path()
	if err != nil {
		return ExploreHandoffResult{}, err
	}

	sourceKind := request.SourceKind
	if sourceKind == "" {
		sourceKind = sourceadapter.SourceProject
	}
	var source sourceadapter.Source
	switch sourceKind {
	case sourceadapter.SourceProject:
		// This calls only the retained published project path. There is
		// deliberately no LoadDraft escape hatch in this seam.
		source, err = options.Sources.LoadDashboardSource(ctx, request.ProjectID, request.DashboardID, actorID)
	case sourceadapter.SourceInstance:
		published, ok := options.Sources.(AuthenticatedPublishedDashboardSource)
		if !ok || published == nil {
			return ExploreHandoffResult{}, errors.New("published authored dashboard source capability is unavailable")
		}
		source, err = published.LoadPublishedDashboardSource(ctx, request.ProjectID, request.DashboardID, actorID)
	default:
		return ExploreHandoffResult{}, errors.New("unsupported dashboard source kind")
	}
	if err != nil {
		return ExploreHandoffResult{}, err
	}
	if source.Ref.Kind != sourceKind || source.Ref.ProjectID != request.ProjectID || source.Ref.DashboardID != request.DashboardID {
		return ExploreHandoffResult{}, errors.New("dashboard source identity does not match request")
	}
	visualID := strings.TrimSpace(request.VisualID)
	if componentID := strings.TrimSpace(request.ComponentID); componentID != "" {
		if request.PageID == "" {
			return ExploreHandoffResult{}, errors.New("component handoff requires a page id")
		}
		resolved, resolveErr := dashboardVisualForComponent(source.Document, request.PageID, componentID)
		if resolveErr != nil {
			return ExploreHandoffResult{}, resolveErr
		}
		if visualID != "" && visualID != resolved {
			return ExploreHandoffResult{}, errors.New("dashboard component visual does not match request")
		}
		visualID = resolved
	}
	semanticModelID := strings.TrimSpace(source.Document.Spec.SemanticModel)
	reader, ok := options.Definition.(AuthorizedExploreModelReader)
	if !ok || reader == nil {
		return ExploreHandoffResult{}, errors.New("authorized active semantic model capability is unavailable")
	}
	model, compiled, identity, err := reader.AuthorizedExploreModel(ctx, request.ProjectID, actorID, semanticModelID)
	if err != nil {
		return ExploreHandoffResult{}, err
	}
	if identity.ProjectID != request.ProjectID || identity.Validate() != nil {
		return ExploreHandoffResult{}, errors.New("active semantic model serving identity does not match request")
	}
	if sourceKind == sourceadapter.SourceProject {
		if source.Provenance.Project == nil || source.Provenance.Project.ProjectID != request.ProjectID || source.Provenance.Project.DashboardID != request.DashboardID || source.Provenance.Project.Identity != identity {
			return ExploreHandoffResult{}, errors.New("retained dashboard source serving identity does not match active model")
		}
	} else if source.Provenance.Instance == nil || source.Provenance.Instance.ProjectID != request.ProjectID || source.Provenance.Instance.DashboardID != request.DashboardID || source.Provenance.Instance.DraftRevision != nil || source.Provenance.Instance.PublishedRevision.ValidateComplete() != nil {
		return ExploreHandoffResult{}, errors.New("published authored dashboard source provenance is invalid")
	}
	reverseOptions, err := explorationadapter.ReverseOptionsForActiveModelAtTarget(source.Document, visualID, dashboardFilterTarget(request.PageID, request.ComponentID), model, compiled)
	if err != nil {
		return ExploreHandoffResult{}, fmt.Errorf("%w: %v", ErrExploreHandoffIncompatible, err)
	}
	spec, err := explorationadapter.FromDashboardDocument(source.Document, visualID, reverseOptions)
	if err != nil {
		return ExploreHandoffResult{}, fmt.Errorf("%w: %v", ErrExploreHandoffIncompatible, err)
	}
	if request.PageID != "" && !dashboardVisualOnPage(source.Document, request.PageID, visualID) {
		return ExploreHandoffResult{}, errors.New("dashboard visual is not present on requested page")
	}
	if request.State != nil {
		overlay := dashboardExploreOverlay{}
		if len(request.State.FilterBindingIDs) != 0 {
			var overlayErr error
			overlay, overlayErr = newDashboardExploreFilterOverlay(source.Document, visualID, request.PageID, request.ComponentID, reverseOptions, spec)
			if overlayErr != nil {
				return ExploreHandoffResult{}, fmt.Errorf("%w: %v", ErrExploreHandoffIncompatible, overlayErr)
			}
		}
		if err := applyDashboardExploreState(&spec, *request.State, model, overlay); err != nil {
			return ExploreHandoffResult{}, fmt.Errorf("%w: %v", ErrExploreHandoffIncompatible, err)
		}
	}
	handoff, err := canonicalExploreHandoff(spec, request.Return)
	if err != nil {
		return ExploreHandoffResult{}, err
	}
	// ReturnPath was validated before source lookup to keep route decisions
	// independent of authored content. Keep the explicit local value so this
	// remains true if the generic helper gains another canonical return mode.
	handoff.ReturnPath = returnPath
	return handoff, nil
}

func canonicalExploreHandoff(spec exploration.ExplorationSpec, returnContext ExploreReturnContext) (ExploreHandoffResult, error) {
	returnPath, err := returnContext.Path()
	if err != nil {
		return ExploreHandoffResult{}, err
	}
	href, err := projectui.CanonicalDataExplorerHref(spec)
	if err != nil {
		return ExploreHandoffResult{}, fmt.Errorf("canonical explorer URL: %w", err)
	}
	href, err = appendExploreReturnContext(href, returnContext)
	if err != nil {
		return ExploreHandoffResult{}, err
	}
	return ExploreHandoffResult{URL: href, ReturnPath: returnPath, Spec: spec}, nil
}

func appendExploreReturnContext(href string, context ExploreReturnContext) (string, error) {
	parsed, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("canonical explorer URL: %w", err)
	}
	values := parsed.Query()
	values.Set("returnSurface", string(context.Surface))
	if context.Surface == ExploreReturnDashboard {
		values.Set("returnDashboard", context.DashboardID.String())
		if page := strings.TrimSpace(context.PageID); page != "" {
			values.Set("returnPage", page)
		}
	}
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

// safeExploreRedirectURL validates the canonical handoff URL at the HTTP
// redirect boundary and rebuilds it from a fixed local destination. The
// explorer URL is assembled from authorized authored values, but keeping the
// redirect target explicit here prevents those values from influencing the
// destination authority or path.
func safeExploreRedirectURL(value string) (string, error) {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\\\r\n") {
		return "", errors.New("canonical explorer URL contains unsafe characters")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("canonical explorer URL: %w", err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" || parsed.Path != "/explore" || parsed.RawPath != "" {
		return "", errors.New("canonical explorer URL must target /explore")
	}
	if parsed.RawQuery == "" {
		return "/explore", nil
	}
	return "/explore?" + parsed.RawQuery, nil
}

func dashboardReturnWithRequestPage(value ExploreReturnContext, dashboardID authoring.DashboardID, pageID string) ExploreReturnContext {
	if value.Surface != ExploreReturnDashboard {
		return value
	}
	if value.DashboardID == "" {
		value.DashboardID = dashboardID
	}
	if strings.TrimSpace(value.PageID) == "" {
		value.PageID = strings.TrimSpace(pageID)
	}
	return value
}

func validDashboardRouteID(value string) bool {
	if value == "" || len(value) > 128 || value[0] < 'A' && value[0] > 'Z' && value[0] < 'a' && value[0] > 'z' && value[0] != '_' {
		return false
	}
	for _, char := range value[1:] {
		if (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func dashboardExploreStateFromRequest(r *stdhttp.Request) (*DashboardExploreState, error) {
	if r == nil || r.URL == nil {
		return nil, nil
	}
	values := r.URL.Query()
	if _, present := values["state"]; !present {
		return nil, nil
	}
	command, err := dataExploreCommandFromQuery(values)
	if err != nil {
		return nil, err
	}
	state := &DashboardExploreState{ModelID: command.Spec.ModelID, DatasetID: cloneStringValue(command.Spec.DatasetID), Filters: append([]exploration.ExplorationFilter(nil), command.Spec.Filters...)}
	if bindingIDs, present := values["filterBinding"]; present {
		if len(bindingIDs) != len(state.Filters) {
			return nil, errors.New("dashboard filter binding count does not match filter count")
		}
		state.FilterBindingIDs = make([]string, len(bindingIDs))
		seen := make(map[string]struct{}, len(bindingIDs))
		for index, bindingID := range bindingIDs {
			bindingID = strings.TrimSpace(bindingID)
			if bindingID == "" {
				return nil, errors.New("dashboard filter binding id is empty")
			}
			if _, duplicate := seen[bindingID]; duplicate {
				return nil, errors.New("dashboard filter binding ids must be unique")
			}
			seen[bindingID] = struct{}{}
			state.FilterBindingIDs[index] = bindingID
		}
	}
	if command.Spec.Time != nil {
		copy := *command.Spec.Time
		state.Time = &copy
	}
	return state, nil
}

func cloneStringValue(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.TrimSpace(*left) == strings.TrimSpace(*right)
}

func dashboardVisualOnPage(value document.DashboardDocument, pageID, visualID string) bool {
	pageID, visualID = strings.TrimSpace(pageID), strings.TrimSpace(visualID)
	if !validDashboardRouteID(pageID) || visualID == "" {
		return false
	}
	for _, page := range value.Spec.Pages {
		if page.ID != pageID {
			continue
		}
		for _, component := range page.Components {
			if _, err := component.Base(); err != nil {
				return false
			}
			if visual, ok := component.Value.(*document.VisualDashboardPageComponent); ok && visual.Visual == visualID {
				return true
			}
		}
		return false
	}
	return false
}

// dashboardVisualForComponent resolves a route component against the
// authorized authored document. Component IDs are page-local, and the exact
// component identity is required because multiple placements may reference
// one authored visual with different filter bindings.
func dashboardVisualForComponent(value document.DashboardDocument, pageID, componentID string) (string, error) {
	pageID, componentID = strings.TrimSpace(pageID), strings.TrimSpace(componentID)
	if !validDashboardRouteID(pageID) || !validDashboardRouteID(componentID) {
		return "", errors.New("dashboard component identity is invalid")
	}
	for _, page := range value.Spec.Pages {
		if page.ID != pageID {
			continue
		}
		for _, component := range page.Components {
			base, err := component.Base()
			if err != nil {
				return "", errors.New("dashboard component is invalid")
			}
			if base.ID != componentID {
				continue
			}
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok || visual == nil || strings.TrimSpace(visual.Visual) == "" {
				return "", errors.New("dashboard component is not a visual")
			}
			return strings.TrimSpace(visual.Visual), nil
		}
		return "", errors.New("dashboard component is not present on requested page")
	}
	return "", errors.New("dashboard page is not present in published source")
}

type dashboardExploreOverlay struct {
	editableFilterIndices map[string]int
	editableTimeBindings  map[string]bool
}

// dashboardExploreFilterOverlay preserves the distinction that canonical
// ExplorationFilter intentionally does not carry: a dashboard filter ID is
// either an editable reader control or a fixed authored predicate. The source
// document is authoritative for this distinction and the active reverse
// bindings identify the canonical field. A missing/ambiguous mapping fails
// closed instead of silently changing a query boundary.
func newDashboardExploreFilterOverlay(value document.DashboardDocument, visualID, pageID, componentID string, options explorationadapter.ReverseOptions, spec exploration.ExplorationSpec) (dashboardExploreOverlay, error) {
	result := dashboardExploreOverlay{editableFilterIndices: map[string]int{}, editableTimeBindings: map[string]bool{}}
	used := make([]bool, len(spec.Filters))
	timeFields := map[string]int{}
	outputs := canonicalFilterOutputs(spec)
	for _, filter := range dashboardFiltersForVisual(value, visualID, pageID, componentID) {
		id := strings.TrimSpace(filter.ID)
		if id == "" {
			return dashboardExploreOverlay{}, errors.New("dashboard filter id is required for a scoped handoff")
		}
		canonical, canonicalErr := explorationadapter.ReverseDashboardFilter(filter, options, outputs)
		if canonicalErr != nil {
			return dashboardExploreOverlay{}, fmt.Errorf("dashboard filter %q: %w", id, canonicalErr)
		}
		isTime := dashboardFilterIsTime(filter, spec, options, canonical.Field)
		if isTime {
			field := strings.TrimSpace(canonical.Field)
			timeFields[field]++
			if timeFields[field] > 1 {
				return dashboardExploreOverlay{}, fmt.Errorf("dashboard time filters for %q cannot be represented without losing a fixed bound", field)
			}
		}
		index := -1
		if !isTime {
			for candidate := range spec.Filters {
				if used[candidate] || !sameCanonicalFilter(spec.Filters[candidate], canonical) {
					continue
				}
				index = candidate
				used[candidate] = true
				break
			}
			if index < 0 {
				return dashboardExploreOverlay{}, fmt.Errorf("dashboard filter %q is not represented by the selected visual", id)
			}
		}
		editable := filter.ReaderEditable == nil || *filter.ReaderEditable
		if !editable {
			continue
		}
		if isTime {
			result.editableTimeBindings[id] = true
			continue
		}
		if _, duplicate := result.editableFilterIndices[id]; duplicate {
			return dashboardExploreOverlay{}, fmt.Errorf("dashboard filter %q is ambiguous", id)
		}
		result.editableFilterIndices[id] = index
	}
	return result, nil
}

func sameCanonicalFilter(left, right exploration.ExplorationFilter) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func canonicalFilterOutputs(spec exploration.ExplorationSpec) map[string]string {
	outputs := make(map[string]string, len(spec.Dimensions)+len(spec.Metrics))
	add := func(field string, alias *string) {
		field = strings.TrimSpace(field)
		if field == "" {
			return
		}
		outputs[field] = field
		if alias != nil && strings.TrimSpace(*alias) != "" {
			outputs[strings.TrimSpace(*alias)] = field
		}
	}
	for _, dimension := range spec.Dimensions {
		add(dimension.Field, dimension.Alias)
	}
	for _, metric := range spec.Metrics {
		add(metric.Field, metric.Alias)
	}
	if spec.Pivot != nil {
		for _, dimension := range spec.Pivot.Rows {
			add(dimension.Field, dimension.Alias)
		}
		for _, dimension := range spec.Pivot.Columns {
			add(dimension.Field, dimension.Alias)
		}
		for _, metric := range spec.Pivot.Metrics {
			add(metric.Field, metric.Alias)
		}
	}
	return outputs
}

func dashboardFilterTarget(pageID, componentID string) string {
	pageID, componentID = strings.TrimSpace(pageID), strings.TrimSpace(componentID)
	if pageID == "" || componentID == "" {
		return ""
	}
	return pageID + "/" + componentID
}

func dashboardFilterIsTime(value document.DashboardFilter, spec exploration.ExplorationSpec, options explorationadapter.ReverseOptions, field string) bool {
	if spec.Time == nil || strings.TrimSpace(spec.Time.Field) != strings.TrimSpace(field) || strings.TrimSpace(options.TimeField) != strings.TrimSpace(field) || value.Default == nil {
		return false
	}
	switch value.Default.Value.(type) {
	case *document.RangeDashboardFilterExpression, *document.RelativePeriodDashboardFilterExpression:
		return true
	default:
		return false
	}
}

func dashboardFiltersForVisual(value document.DashboardDocument, visualID, pageID, componentID string) []document.DashboardFilter {
	result := make([]document.DashboardFilter, 0, len(value.Spec.Filters))
	componentTarget := strings.TrimSpace(pageID) + "/" + strings.TrimSpace(componentID)
	for _, filter := range value.Spec.Filters {
		if filter.Targets == nil {
			result = append(result, filter)
			continue
		}
		for _, target := range *filter.Targets {
			target = strings.TrimSpace(target)
			if target == strings.TrimSpace(visualID) || (strings.TrimSpace(componentID) != "" && target == componentTarget) {
				result = append(result, filter)
				break
			}
		}
	}
	return result
}

func applyDashboardExploreState(spec *exploration.ExplorationSpec, state DashboardExploreState, model *semanticmodel.Model, overlay dashboardExploreOverlay) error {
	if spec == nil || model == nil {
		return errors.New("active dashboard exploration state is unavailable")
	}
	if len(state.Filters) > 100 {
		return errors.New("dashboard exploration filter count exceeds 100")
	}
	if modelID := strings.TrimSpace(state.ModelID); modelID != "" && modelID != spec.ModelID {
		return errors.New("current dashboard state model does not match the selected visual")
	}
	if state.DatasetID != nil {
		if spec.DatasetID == nil || strings.TrimSpace(*state.DatasetID) != strings.TrimSpace(*spec.DatasetID) {
			return errors.New("current dashboard state dataset does not match the selected visual")
		}
	}
	allowed := make(map[string]struct{}, len(spec.Dimensions)+len(spec.Filters)+1)
	for _, dimension := range spec.Dimensions {
		allowed[dimension.Field] = struct{}{}
	}
	for _, filter := range spec.Filters {
		allowed[filter.Field] = struct{}{}
	}
	if spec.Time != nil {
		allowed[spec.Time.Field] = struct{}{}
	}
	for _, filter := range state.Filters {
		if _, ok := allowed[strings.TrimSpace(filter.Field)]; !ok {
			return fmt.Errorf("filter field %q is not bound to the selected dashboard visual", filter.Field)
		}
	}
	if state.Time != nil {
		if spec.Time == nil || state.Time.Field != spec.Time.Field || state.Time.Grain != spec.Time.Grain {
			return errors.New("time selection is not bound to the selected dashboard visual")
		}
	}
	if len(state.FilterBindingIDs) != 0 && len(state.FilterBindingIDs) != len(state.Filters) {
		return errors.New("dashboard filter binding count does not match filter count")
	}
	remainingFilters := make([]exploration.ExplorationFilter, 0, len(state.Filters))
	for index, current := range state.Filters {
		bindingID := ""
		if len(state.FilterBindingIDs) != 0 {
			bindingID = state.FilterBindingIDs[index]
		}
		if spec.Time == nil || strings.TrimSpace(current.Field) != strings.TrimSpace(spec.Time.Field) {
			if bindingID != "" {
				filterIndex, ok := overlay.editableFilterIndices[bindingID]
				if !ok || filterIndex < 0 || filterIndex >= len(spec.Filters) {
					return fmt.Errorf("dashboard filter binding %q is not editable for the selected visual", bindingID)
				}
				if strings.TrimSpace(current.Field) != strings.TrimSpace(spec.Filters[filterIndex].Field) {
					return fmt.Errorf("dashboard filter binding %q does not match its canonical field", bindingID)
				}
				if current.DatasetID != nil && !sameOptionalString(current.DatasetID, spec.Filters[filterIndex].DatasetID) {
					return fmt.Errorf("dashboard filter binding %q does not match its canonical dataset", bindingID)
				}
				if current.DatasetID == nil {
					current.DatasetID = cloneStringValue(spec.Filters[filterIndex].DatasetID)
				}
				spec.Filters[filterIndex] = current
				continue
			}
			remainingFilters = append(remainingFilters, current)
			continue
		}
		if bindingID != "" && !overlay.editableTimeBindings[bindingID] {
			return fmt.Errorf("dashboard time filter binding %q is not editable for the selected visual", bindingID)
		}
		rangeValue, clear, err := explorationTimeRangeFromFilter(current.Expression)
		if err != nil {
			return fmt.Errorf("time filter: %w", err)
		}
		if clear {
			spec.Time.Range = nil
		} else {
			spec.Time.Range = rangeValue
		}
	}
	if len(remainingFilters) != 0 {
		// Current dashboard controls are an additional, explicitly scoped
		// predicate. Do not replace an authored predicate merely because it
		// uses the same field: the authored filter may be a fixed security or
		// business boundary (for example region = EMEA), while the current
		// control narrows it further. The source adapter remains authoritative
		// for which dashboard filters are overrideable.
		spec.Filters = append(append([]exploration.ExplorationFilter(nil), spec.Filters...), remainingFilters...)
	}
	if state.Time != nil {
		if len(state.FilterBindingIDs) != 0 {
			return errors.New("dashboard time selection is missing its editable binding identity")
		}
		copy := *state.Time
		spec.Time = &copy
	}
	if err := exploration.ValidateAgainstModel(model, spec); err != nil {
		return err
	}
	return exploration.ValidateShape(spec)
}

// explorationTimeRangeFromFilter converts a typed dashboard date/timestamp
// control into the canonical time range attached to the authored time
// selection. A non-temporal operator is rejected instead of becoming an
// ordinary predicate on the same field, which could silently change the
// meaning of a time control.
func explorationTimeRangeFromFilter(value exploration.ExplorationFilterExpression) (*exploration.ExplorationTimeRange, bool, error) {
	switch expression := value.Value.(type) {
	case *exploration.UnfilteredExplorationFilterExpression:
		if expression == nil {
			return nil, false, errors.New("time filter expression is nil")
		}
		return nil, true, nil
	case *exploration.RangeExplorationFilterExpression:
		if expression == nil {
			return nil, false, errors.New("time range expression is nil")
		}
		lower, err := explorationTimeBoundFromFilter(expression.Lower)
		if err != nil {
			return nil, false, fmt.Errorf("lower bound: %w", err)
		}
		upper, err := explorationTimeBoundFromFilter(expression.Upper)
		if err != nil {
			return nil, false, fmt.Errorf("upper bound: %w", err)
		}
		return &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{
			ExplorationTimeRangeBase: exploration.ExplorationTimeRangeBase{Kind: "absolute"}, Kind: "absolute", Lower: lower, Upper: upper,
		}}, false, nil
	case *exploration.RelativePeriodExplorationFilterExpression:
		if expression == nil {
			return nil, false, errors.New("relative time expression is nil")
		}
		anchorValue, err := explorationTemporalValueFromFilter(expression.AnchorValue)
		if err != nil {
			return nil, false, fmt.Errorf("anchor value: %w", err)
		}
		return &exploration.ExplorationTimeRange{Value: &exploration.RelativeExplorationTimeRange{
			ExplorationTimeRangeBase: exploration.ExplorationTimeRangeBase{Kind: "relative"}, Kind: "relative",
			Direction: expression.Direction, Count: expression.Count, Unit: expression.Unit, IncludeCurrent: expression.IncludeCurrent,
			Anchor: expression.Anchor, AnchorValue: anchorValue,
		}}, false, nil
	default:
		return nil, false, errors.New("unsupported time filter operator")
	}
}

func explorationTimeBoundFromFilter(value *exploration.ExplorationFilterBound) (*exploration.ExplorationTimeBound, error) {
	if value == nil {
		return nil, nil
	}
	temporal, err := explorationTemporalValueFromFilter(&value.Value)
	if err != nil {
		return nil, err
	}
	return &exploration.ExplorationTimeBound{Value: *temporal, Inclusive: value.Inclusive}, nil
}

func explorationTemporalValueFromFilter(value *exploration.ExplorationFilterValue) (*exploration.ExplorationTemporalValue, error) {
	if value == nil {
		return nil, nil
	}
	switch item := value.Value.(type) {
	case *exploration.DateExplorationFilterValue:
		if item == nil {
			return nil, errors.New("date value is nil")
		}
		return &exploration.ExplorationTemporalValue{Value: &exploration.DateExplorationTemporalValue{ExplorationTemporalValueBase: exploration.ExplorationTemporalValueBase{Kind: "date"}, Kind: "date", Value: item.Value}}, nil
	case *exploration.TimestampExplorationFilterValue:
		if item == nil {
			return nil, errors.New("timestamp value is nil")
		}
		return &exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{ExplorationTemporalValueBase: exploration.ExplorationTemporalValueBase{Kind: "timestamp"}, Kind: "timestamp", Value: item.Value}}, nil
	default:
		return nil, errors.New("time values must be date or timestamp")
	}
}

// ExploreFromDashboardRoute is the authenticated browser adapter. Route
// registration remains with the project/app composition owner; keeping this
// method here lets that wiring add one GET without changing the existing
// Add-to-dashboard signal contract.
func (h *BrowserHandler) ExploreFromDashboardRoute(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if h == nil || r == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnauthorized), stdhttp.StatusUnauthorized)
		return
	}
	loader, ok := h.DashboardAuthoring.(AuthenticatedDashboardSource)
	if !ok || loader == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	project, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	dashboardID := authoring.DashboardID(strings.TrimSpace(chi.URLParam(r, "dashboard")))
	pageID := strings.TrimSpace(chi.URLParam(r, "page"))
	componentID := strings.TrimSpace(chi.URLParam(r, "component"))
	sourceKind := sourceadapter.SourceProject
	if resolver, ok := h.DashboardAuthoring.(AuthenticatedDashboardSourceKindResolver); ok && resolver != nil {
		sourceKind, err = resolver.ResolveDashboardSourceKind(r.Context(), project, dashboardID, principal.ID)
		if err != nil {
			stdhttp.NotFound(w, r)
			return
		}
	}
	state, err := dashboardExploreStateFromRequest(r)
	if err != nil {
		stdhttp.Error(w, "invalid dashboard exploration state", stdhttp.StatusBadRequest)
		return
	}
	if state != nil && len(state.FilterBindingIDs) == 0 && (len(state.Filters) != 0 || state.Time != nil) {
		// The mounted handoff has no legacy unscoped-state contract. Requiring
		// authored control identities prevents a forged current state from
		// replacing or clearing a fixed dashboard time/filter boundary.
		stdhttp.Error(w, "dashboard exploration overrides require scoped filter bindings", stdhttp.StatusBadRequest)
		return
	}
	result, err := ExploreFromDashboard(r.Context(), ExploreFromDashboardOptions{Sources: loader, Definition: h.ProjectDefinitionReader}, ExploreFromDashboardRequest{
		ProjectID: project, DashboardID: dashboardID, PageID: pageID, ComponentID: componentID, VisualID: chi.URLParam(r, "visual"), SourceKind: sourceKind, ActorID: principal.ID,
		Return: ExploreReturnContext{Surface: ExploreReturnDashboard, DashboardID: dashboardID, PageID: pageID}, State: state,
	})
	if err != nil {
		if errors.Is(err, ErrExploreHandoffIncompatible) {
			stdhttp.Error(w, "This dashboard visual cannot be represented in Data Explorer.", stdhttp.StatusUnprocessableEntity)
			return
		}
		// Source authorization/unavailability intentionally share a not-found
		// response so the route cannot disclose another dashboard's existence.
		stdhttp.NotFound(w, r)
		return
	}
	location, err := safeExploreRedirectURL(result.URL)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusInternalServerError), stdhttp.StatusInternalServerError)
		return
	}
	stdhttp.Redirect(w, r, location, stdhttp.StatusSeeOther)
}
