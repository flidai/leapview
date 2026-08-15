package compiler

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"gopkg.in/yaml.v3"
)

// ErrDashboardSourceUnavailable reports that a compiled dashboard does not
// retain enough authored information to be exported as dashboard YAML.
//
// Compiled definitions deliberately contain renderer-owned IR rather than the
// authoring union. They are therefore not accepted by ExportDashboard and are
// never decompiled implicitly.
var ErrDashboardSourceUnavailable = errors.New("dashboard authored source is unavailable")

// DashboardExportMetadata is retained as an alias for the dashboard-owned
// export contract. The compiler remains the production implementation while
// callers can depend on the dashboard capability's narrow port.
type DashboardExportMetadata = dashboardauthoring.DashboardExportMetadata

type canonicalDashboardResource struct {
	APIVersion string                     `yaml:"apiVersion"`
	Kind       string                     `yaml:"kind"`
	Metadata   canonicalDashboardMetadata `yaml:"metadata"`
	Spec       canonicalDashboardSpec     `yaml:"spec"`
}

type canonicalDashboardMetadata struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Owner       string   `yaml:"owner,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
}

// canonicalDashboardSpec is intentionally separate from dashboardSpec. The
// latter is the loader's decoding shape, while this type owns the exact
// canonical resource spelling and omission rules.
type canonicalDashboardSpec struct {
	Appearance        *dashboardappearance.Patch                 `yaml:"appearance,omitempty"`
	SemanticModel     string                                     `yaml:"semanticModel"`
	Filters           map[string]dashboardfilter.Definition      `yaml:"filters,omitempty"`
	FilterBindings    map[string]dashboardfilter.Binding         `yaml:"filter_bindings,omitempty"`
	FilterApplication *dashboardfilter.ApplicationPolicy         `yaml:"filter_application,omitempty"`
	Visuals           map[string]canonicalDashboardVisualization `yaml:"visuals"`
	Pages             []canonicalDashboardPage                   `yaml:"pages"`
}

type canonicalDashboardPage struct {
	ID             string                             `yaml:"id"`
	Title          string                             `yaml:"title"`
	Description    string                             `yaml:"description,omitempty"`
	Canvas         dashboardPageCanvas                `yaml:"canvas,omitempty"`
	Grid           dashboardPageGrid                  `yaml:"grid,omitempty"`
	FilterBindings map[string]dashboardfilter.Binding `yaml:"filter_bindings,omitempty"`
	Components     []dashboardPageVisual              `yaml:"components"`
}

type dashboardPageCanvas struct {
	Width  int `yaml:"width,omitempty"`
	Height int `yaml:"height,omitempty"`
}
type dashboardPageGrid struct {
	Columns   int `yaml:"columns,omitempty"`
	RowHeight int `yaml:"row_height,omitempty"`
	Gap       int `yaml:"gap,omitempty"`
	Padding   int `yaml:"padding,omitempty"`
}

type dashboardPageVisual struct {
	ID           string                       `yaml:"id"`
	Kind         string                       `yaml:"kind"`
	Visual       string                       `yaml:"visual,omitempty"`
	Binding      dashboardfilter.BindingRef   `yaml:"binding,omitempty"`
	Presentation dashboardfilter.Presentation `yaml:"presentation,omitempty"`
	Description  string                       `yaml:"description,omitempty"`
	Placement    dashboardPagePlacement       `yaml:"placement"`
	Eyebrow      string                       `yaml:"eyebrow,omitempty"`
	Title        string                       `yaml:"title,omitempty"`
	Subtitle     string                       `yaml:"subtitle,omitempty"`
	Badges       []string                     `yaml:"badges,omitempty"`
}
type dashboardPagePlacement struct {
	Col     int `yaml:"col"`
	Row     int `yaml:"row"`
	ColSpan int `yaml:"col_span"`
	RowSpan int `yaml:"row_span"`
}

// canonicalDashboardVisualization flattens the closed authoring union into
// the resource's single visual mapping. AuthoringVisualization itself must
// never be marshaled directly: doing so would expose Chart/Tabular internals.
type canonicalDashboardVisualization struct {
	Value dashboardauthoring.AuthoringVisualization
}

func (v canonicalDashboardVisualization) MarshalYAML() (any, error) {
	if (v.Value.Chart == nil) == (v.Value.Tabular == nil) {
		return nil, fmt.Errorf("visualization must contain exactly one authoring variant")
	}
	var node yaml.Node
	if v.Value.Chart != nil {
		if err := node.Encode(v.Value.Chart); err != nil {
			return nil, err
		}
	} else {
		if err := node.Encode(v.Value.Tabular); err != nil {
			return nil, err
		}
		setYAMLMapString(&node, "type", v.Value.Type)
	}
	pruneYAMLDefaults(&node, map[string]struct{}{"type": {}, "query": {}, "title": {}})
	restrictCanonicalVisual(&node)
	return &node, nil
}

func setYAMLMapString(node *yaml.Node, key, value string) {
	if node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// pruneYAMLDefaults removes zero-valued struct fields that are not part of
// the authored contract. Several authoring structs intentionally omit
// yaml omitempty tags because they are decode-first types; emitting their
// zero nested structs would create invalid optional CUE values (for example,
// data_budget.max_rows: 0). Required fields are retained even when empty so
// schema validation can report the precise contract error.
func pruneYAMLDefaults(node *yaml.Node, required map[string]struct{}) bool {
	return pruneYAMLDefaultsMode(node, required, false)
}

func pruneYAMLDefaultsMode(node *yaml.Node, required map[string]struct{}, propagate bool) bool {
	if node == nil {
		return false
	}
	switch node.Kind {
	case yaml.DocumentNode:
		return len(node.Content) > 0 && pruneYAMLDefaultsMode(node.Content[0], required, propagate)
	case yaml.MappingNode:
		kept := node.Content[:0]
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			keepEmpty := false
			if _, ok := required[key.Value]; ok {
				keepEmpty = true
			}
			childRequired := map[string]struct{}{}
			if propagate {
				childRequired = required
			}
			if !pruneYAMLDefaultsMode(value, childRequired, propagate) && !keepEmpty {
				continue
			}
			kept = append(kept, key, value)
		}
		node.Content = kept
		return len(node.Content) > 0
	case yaml.SequenceNode:
		kept := node.Content[:0]
		for _, item := range node.Content {
			if pruneYAMLDefaultsMode(item, required, propagate) {
				kept = append(kept, item)
			}
		}
		node.Content = kept
		return len(node.Content) > 0
	case yaml.ScalarNode:
		if node.Tag == "!!null" || node.Value == "" || node.Value == "0" {
			return false
		}
		return true
	default:
		return true
	}
}

func restrictCanonicalVisual(node *yaml.Node) {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		restrictCanonicalVisual(node.Content[0])
		return
	}
	typeValue := yamlMapValue(node, "type")
	allowed := map[string]struct{}{
		"title": {}, "subtitle": {}, "description": {}, "type": {}, "query": {},
		"datasets": {}, "metadata": {}, "presentation": {}, "accessibility": {},
		"data_budget": {}, "interaction": {}, "calculations": {},
	}
	if typeValue == "scatter" {
		allowed["point"] = struct{}{}
	}
	if typeValue == "map" {
		allowed["geo"] = struct{}{}
	}
	if typeValue == "kpi" {
		allowed["kpi"] = struct{}{}
	}
	if typeValue == "table" || typeValue == "matrix" || typeValue == "pivot" {
		allowed = map[string]struct{}{
			"title": {}, "subtitle": {}, "description": {}, "type": {}, "query": {},
			"default_sort": {}, "presentation": {}, "columns": {}, "interaction": {},
			"measure_formatting": {}, "conditional_formatting": {}, "calculations": {},
		}
	}
	deleteYAMLMapKeys(node, allowed)
	presentation := yamlMapNode(node, "presentation")
	if presentation == nil {
		return
	}
	presentationAllowed := map[string]struct{}{}
	switch typeValue {
	case "kpi":
		for _, key := range []string{"display_units", "note", "tone", "thresholds", "conditional_formatting"} {
			presentationAllowed[key] = struct{}{}
		}
	case "map":
		for _, key := range []string{"legend", "display_units", "show_labels", "labels", "conditional_formatting"} {
			presentationAllowed[key] = struct{}{}
		}
	case "scatter":
		for _, key := range []string{"legend", "display_units", "show_labels", "labels", "conditional_formatting", "axes", "reference_lines", "reference_bands", "event_annotations"} {
			presentationAllowed[key] = struct{}{}
		}
	case "table", "matrix", "pivot":
		for _, key := range []string{"density", "zebra", "grid"} {
			presentationAllowed[key] = struct{}{}
		}
	case "pie", "donut", "funnel":
		for _, key := range []string{"legend", "display_units", "show_labels", "labels", "conditional_formatting", "orientation", "rose", "center_label", "label_position", "inner_radius", "outer_radius", "align", "sort"} {
			presentationAllowed[key] = struct{}{}
		}
	case "treemap", "sunburst", "sankey", "graph", "tree":
		for _, key := range []string{"legend", "display_units", "show_labels", "labels", "conditional_formatting", "orientation", "initial_depth", "roam", "layout", "breadcrumb", "node_gap", "curveness", "focus"} {
			presentationAllowed[key] = struct{}{}
		}
	case "gauge", "radar":
		for _, key := range []string{"display_units", "show_labels", "labels", "conditional_formatting", "minimum", "maximum", "target", "area", "progress_width", "thresholds"} {
			presentationAllowed[key] = struct{}{}
		}
	default:
		for _, key := range []string{"legend", "display_units", "show_labels", "labels", "conditional_formatting", "stacked", "smooth", "show_symbols", "data_zoom", "area", "step", "orientation", "label_position", "symbol_size", "histogram_bins", "series_types", "dual_axis", "axes", "reference_lines", "reference_bands", "event_annotations", "tooltip", "stacking", "series_order", "series_colors"} {
			presentationAllowed[key] = struct{}{}
		}
	}
	deleteYAMLMapKeys(presentation, presentationAllowed)
	if typeValue == "map" {
		restrictGeographicLayers(yamlMapNode(node, "geo"))
	}
}

func restrictGeographicLayers(geo *yaml.Node) {
	layers := yamlMapNode(geo, "layers")
	if layers == nil || layers.Kind != yaml.SequenceNode {
		return
	}
	for _, layer := range layers.Content {
		kind := yamlMapValue(layer, "kind")
		allowed := map[string]struct{}{
			"id": {}, "kind": {}, "value": {}, "category": {}, "label": {}, "tooltip": {},
			"position": {}, "visibility": {}, "color": {}, "stroke": {}, "opacity": {},
		}
		switch kind {
		case "choropleth":
			allowed["geometry_asset"] = struct{}{}
			allowed["join"] = struct{}{}
		case "point":
			allowed["latitude"] = struct{}{}
			allowed["longitude"] = struct{}{}
			allowed["size"] = struct{}{}
			allowed["cluster"] = struct{}{}
		case "heat", "density":
			allowed["latitude"] = struct{}{}
			allowed["longitude"] = struct{}{}
			allowed["heat"] = struct{}{}
		case "reference":
			allowed["geometry_asset"] = struct{}{}
		case "path":
			allowed["latitude"] = struct{}{}
			allowed["longitude"] = struct{}{}
			allowed["path"] = struct{}{}
			allowed["order"] = struct{}{}
			allowed["line"] = struct{}{}
		}
		deleteYAMLMapKeys(layer, allowed)
	}
}

var canonicalResourceRequiredYAMLKeys = map[string]struct{}{
	"apiVersion": {}, "kind": {}, "metadata": {}, "name": {}, "spec": {},
	"semanticModel": {}, "visuals": {}, "pages": {}, "id": {}, "type": {},
	"query": {}, "components": {}, "placement": {}, "col": {}, "row": {},
	"col_span": {}, "row_span": {}, "predicates": {}, "filter": {},
	"binding": {}, "scope": {}, "value": {}, "inclusive": {},
}

func yamlMapValue(node *yaml.Node, key string) string {
	value := yamlMapNode(node, key)
	if value != nil && value.Kind == yaml.ScalarNode {
		return value.Value
	}
	return ""
}

func yamlMapNode(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func deleteYAMLMapKeys(node *yaml.Node, allowed map[string]struct{}) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	kept := node.Content[:0]
	for index := 0; index+1 < len(node.Content); index += 2 {
		if _, ok := allowed[node.Content[index].Value]; ok {
			kept = append(kept, node.Content[index], node.Content[index+1])
		}
	}
	node.Content = kept
}

// ExportDashboard emits one deterministic, schema-validated canonical
// Dashboard resource. The input is authored state; compiled dashboard
// definitions are intentionally not accepted by this function.
func ExportDashboard(document dashboardauthoring.Dashboard, metadata DashboardExportMetadata) ([]byte, error) {
	if err := document.ValidateContract(); err != nil {
		return nil, fmt.Errorf("validate authored dashboard: %w", err)
	}
	name := metadata.Name
	if name == "" {
		name = document.ID
	}
	if name != document.ID {
		return nil, fmt.Errorf("dashboard metadata name %q does not match authored dashboard id %q", name, document.ID)
	}
	title := metadata.Title
	if title == "" {
		title = document.Title
	}
	description := metadata.Description
	if description == "" {
		description = document.Description
	}
	resource := canonicalDashboardResource{
		APIVersion: projectAPIVersion,
		Kind:       "Dashboard",
		Metadata: canonicalDashboardMetadata{
			Name: name, Title: title,
			Description: description, Owner: metadata.Owner,
			Tags: append([]string(nil), metadata.Tags...),
		},
		Spec: canonicalDashboardSpec{
			SemanticModel: document.SemanticModel,
			Visuals:       make(map[string]canonicalDashboardVisualization, len(document.Visuals)),
			Pages:         make([]canonicalDashboardPage, 0, len(document.Pages)),
		},
	}
	if document.Appearance.Icon != nil || document.Appearance.Color != nil {
		resource.Spec.Appearance = &document.Appearance
	}
	if len(document.FilterDefinitions) > 0 {
		resource.Spec.Filters = cloneDashboardFilterDefinitions(document.FilterDefinitions)
	}
	if len(document.FilterBindings) > 0 {
		resource.Spec.FilterBindings = cloneDashboardFilterBindings(document.FilterBindings)
	}
	if document.FilterApplication.Mode != "" {
		resource.Spec.FilterApplication = &document.FilterApplication
	}
	for id, visual := range document.Visuals {
		resource.Spec.Visuals[id] = canonicalDashboardVisualization{Value: visual}
	}
	for _, page := range document.Pages {
		resource.Spec.Pages = append(resource.Spec.Pages, canonicalDashboardPage{
			ID: page.ID, Title: page.Title, Description: page.Description,
			Canvas:         dashboardPageCanvas{Width: page.Canvas.Width, Height: page.Canvas.Height},
			Grid:           dashboardPageGrid{Columns: page.Grid.Columns, RowHeight: page.Grid.RowHeight, Gap: page.Grid.Gap, Padding: page.Grid.Padding},
			FilterBindings: cloneDashboardFilterBindings(page.FilterBindings),
			Components:     canonicalPageVisuals(page.Visuals),
		})
	}
	bytes, err := yaml.Marshal(resource)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical dashboard: %w", err)
	}
	bytes, err = canonicalizeDashboardYAML(bytes)
	if err != nil {
		return nil, fmt.Errorf("canonicalize dashboard YAML: %w", err)
	}
	if err := configschema.ValidateBytes(configschema.KindDashboard, "dashboard.yaml", bytes); err != nil {
		return nil, fmt.Errorf("validate canonical dashboard: %w", err)
	}
	return bytes, nil
}

// ExportDashboardDefinition is a typed source-unavailable seam for callers
// that only have compiler output. No decompiler exists by design.
func ExportDashboardDefinition(_ dashboarddefinition.Definition, _ DashboardExportMetadata) ([]byte, error) {
	return nil, ErrDashboardSourceUnavailable
}

func canonicalPageVisuals(values []dashboard.PageVisual) []dashboardPageVisual {
	result := make([]dashboardPageVisual, 0, len(values))
	for _, value := range values {
		result = append(result, dashboardPageVisual{
			ID: value.ID, Kind: value.Kind, Visual: value.Visual, Binding: value.Binding,
			Presentation: value.Presentation, Description: value.Description,
			Placement: dashboardPagePlacement{
				Col: value.Placement.Col, Row: value.Placement.Row,
				ColSpan: value.Placement.ColSpan, RowSpan: value.Placement.RowSpan,
			},
			Eyebrow: value.Eyebrow, Title: value.Title, Subtitle: value.Subtitle,
			Badges: append([]string(nil), value.Badges...),
		})
	}
	return result
}

func cloneDashboardFilterDefinitions(values map[string]dashboardfilter.Definition) map[string]dashboardfilter.Definition {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]dashboardfilter.Definition, len(values))
	for key, value := range values {
		value.Predicates = append([]dashboardfilter.PredicatePolicy(nil), value.Predicates...)
		value.Options.Values = append([]dashboardfilter.Option(nil), value.Options.Values...)
		result[key] = value
	}
	return result
}

func cloneDashboardFilterBindings(values map[string]dashboardfilter.Binding) map[string]dashboardfilter.Binding {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]dashboardfilter.Binding, len(values))
	for key, value := range values {
		value.TargetPolicy.Include = append([]string(nil), value.TargetPolicy.Include...)
		value.TargetPolicy.Exclude = append([]string(nil), value.TargetPolicy.Exclude...)
		value.OptionInteractions.Include = append([]dashboardfilter.BindingRef(nil), value.OptionInteractions.Include...)
		value.OptionInteractions.Exclude = append([]dashboardfilter.BindingRef(nil), value.OptionInteractions.Exclude...)
		result[key] = value
	}
	return result
}

func canonicalizeDashboardYAML(content []byte) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return nil, err
	}
	pruneYAMLDefaultsMode(&document, canonicalResourceRequiredYAMLKeys, true)
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	defer encoder.Close()
	if err := encoder.Encode(&document); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
