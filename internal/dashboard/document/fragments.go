package document

// This file implements the Dashboard-local composition boundary. It operates
// on generated canonical Dashboard DTOs only; fragments are never converted
// into authoring/runtime resource types and never enter the project graph.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	configschema "github.com/flidai/leapview/internal/project/schema"
	"gopkg.in/yaml.v3"
)

// FragmentExpansion is the result of expanding one canonical Dashboard. The
// returned document has Includes cleared and is suitable for canonical
// revision/runtime storage. Paths and Layout are source evidence only.
type FragmentExpansion struct {
	Document DashboardDocument
	Paths    []string
	Layout   FragmentLayout
}

// FragmentLayout preserves the reviewable source arrangement without making
// it a second Dashboard representation. Entries are ordered by collection
// expansion order (glob matches are lexical within each include pattern).
type FragmentLayout struct {
	Visuals    []FragmentSource `json:"visuals,omitempty"`
	Filters    []FragmentSource `json:"filters,omitempty"`
	Pages      []FragmentSource `json:"pages,omitempty"`
	Components []FragmentSource `json:"components,omitempty"`
}

type FragmentSource struct {
	Path string `json:"path"`
}

// FragmentError retains the source path and YAML line for diagnostics emitted
// before the project schema/compiler has a resource envelope to annotate.
type FragmentError struct {
	Path    string
	Line    int
	Message string
}

func (e *FragmentError) Error() string {
	if e == nil {
		return "dashboard fragment error"
	}
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

type fragmentState struct {
	visuals       map[string]DashboardVisual
	filters       []DashboardFilter
	pages         []DashboardPage
	components    map[string][]DashboardPageComponent
	unscoped      []DashboardPageComponent
	active        map[string]struct{}
	paths         []string
	layout        FragmentLayout
	projectRoot   string
	dashboardDir  string
	dashboardPath string
}

// ExpandDashboardFragments expands all typed local include collections in a
// generated canonical DashboardDocument. Includes are resolved relative to
// the Dashboard resource, while all concrete paths remain inside projectRoot.
// Mapping collections are unioned by key and ordered collections concatenate;
// no object is patched or deep-merged.
func ExpandDashboardFragments(input DashboardDocument, dashboardPath, projectRoot string) (FragmentExpansion, error) {
	document, err := cloneDashboardDocument(input)
	if err != nil {
		return FragmentExpansion{}, fmt.Errorf("clone canonical dashboard: %w", err)
	}
	if document.Spec.Includes == nil {
		return FragmentExpansion{Document: document}, validateExpandedIDs(document)
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return FragmentExpansion{}, fmt.Errorf("resolve project boundary: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return FragmentExpansion{}, fmt.Errorf("resolve project boundary: %w", err)
	}
	dashboardPath, err = filepath.Abs(dashboardPath)
	if err != nil {
		return FragmentExpansion{}, fmt.Errorf("resolve dashboard path: %w", err)
	}
	state := &fragmentState{
		visuals: make(map[string]DashboardVisual), components: make(map[string][]DashboardPageComponent),
		active: make(map[string]struct{}), projectRoot: root, dashboardDir: filepath.Dir(dashboardPath), dashboardPath: dashboardPath,
	}
	includes := document.Spec.Includes
	if err := state.expandIncludes(includes); err != nil {
		return FragmentExpansion{}, err
	}
	// Include mappings precede local mappings. A duplicate key is a hard error;
	// a local value can never redefine a fragment value.
	for id, value := range document.Spec.Visuals {
		if _, exists := state.visuals[id]; exists {
			return FragmentExpansion{}, state.errorf(dashboardPath, 0, "visual %q is redefined after fragment expansion", id)
		}
		state.visuals[id] = value
	}
	state.filters = append(state.filters, document.Spec.Filters...)
	state.pages = append(state.pages, document.Spec.Pages...)
	document.Spec.Visuals, document.Spec.Filters, document.Spec.Pages = state.visuals, state.filters, state.pages
	if err := attachComponents(&document, state); err != nil {
		return FragmentExpansion{}, err
	}
	document.Spec.Includes = nil
	if err := validateExpandedIDs(document); err != nil {
		return FragmentExpansion{}, err
	}
	return FragmentExpansion{Document: document, Paths: append([]string(nil), state.paths...), Layout: state.layout}, nil
}

func (state *fragmentState) expandIncludes(includes *DashboardIncludes) error {
	for _, pattern := range includeStrings(includes.Visuals) {
		if err := state.expandPattern(pattern, "visuals"); err != nil {
			return err
		}
	}
	for _, pattern := range includeStrings(includes.Filters) {
		if err := state.expandPattern(pattern, "filters"); err != nil {
			return err
		}
	}
	for _, pattern := range includeStrings(includes.Pages) {
		if err := state.expandPattern(pattern, "pages"); err != nil {
			return err
		}
	}
	for _, pattern := range includeStrings(includes.Components) {
		if err := state.expandPattern(pattern, "components"); err != nil {
			return err
		}
	}
	return nil
}

func includeStrings(values *[]string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), (*values)...)
}

func (state *fragmentState) expandPattern(pattern, expected string) error {
	paths, err := resolveFragmentPaths(state.projectRoot, state.dashboardDir, pattern)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := state.expandFile(path, expected); err != nil {
			return err
		}
	}
	return nil
}

func (state *fragmentState) expandFile(path, expected string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return state.errorf(path, 0, "cannot resolve fragment: %v", err)
	}
	if _, exists := state.active[canonical]; exists {
		return state.errorf(path, 0, "fragment include cycle detected")
	}
	state.active[canonical] = struct{}{}
	defer delete(state.active, canonical)
	state.paths = appendUnique(state.paths, canonical)
	state.addLayout(expected, canonical)
	content, err := os.ReadFile(canonical)
	if err != nil {
		return state.errorf(canonical, 0, "read fragment: %v", err)
	}
	var documentNode yaml.Node
	if err := yaml.Unmarshal(content, &documentNode); err != nil {
		return state.errorf(canonical, 0, "decode YAML: %v", err)
	}
	root := documentNode
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = *root.Content[0]
	}
	if root.Kind != yaml.MappingNode && root.Kind != yaml.SequenceNode {
		return state.errorf(canonical, root.Line, "fragment must be a mapping or sequence")
	}
	if root.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(root.Content); i += 2 {
			key := root.Content[i]
			if key.Value == "apiVersion" || key.Value == "kind" || key.Value == "metadata" || key.Value == "spec" {
				return state.errorf(canonical, key.Line, "fragment must not be a project resource envelope")
			}
		}
	}
	if nested, ok := fragmentMapNode(&root, "includes"); ok {
		var includes DashboardIncludes
		if err := decodeNodeJSON(nested, &includes); err != nil {
			return state.errorf(canonical, nested.Line, "decode includes: %v", err)
		}
		if err := state.expandIncludes(&includes); err != nil {
			return err
		}
	}
	value := &root
	if wrapped, ok := fragmentMapNode(&root, expected); ok {
		for i := 0; i+1 < len(root.Content); i += 2 {
			key := root.Content[i].Value
			if key != expected && key != "includes" {
				return state.errorf(canonical, root.Content[i].Line, "fragment collection %q cannot contain %q", expected, key)
			}
		}
		value = wrapped
	} else if root.Kind == yaml.MappingNode {
		value = fragmentMapWithout(&root, "includes")
	}
	switch expected {
	case "visuals":
		var values map[string]DashboardVisual
		if err := decodeNodeJSON(value, &values); err != nil {
			return state.errorf(canonical, value.Line, "decode visuals: %v", err)
		}
		for id, visual := range values {
			if _, exists := state.visuals[id]; exists {
				return state.errorf(canonical, value.Line, "visual %q is defined more than once", id)
			}
			state.visuals[id] = visual
		}
	case "filters":
		if value.Kind == yaml.MappingNode && len(value.Content) == 0 {
			return nil
		}
		var values []DashboardFilter
		if err := decodeNodeJSON(value, &values); err != nil {
			return state.errorf(canonical, value.Line, "decode filters: %v", err)
		}
		state.filters = append(state.filters, values...)
	case "pages":
		if value.Kind == yaml.MappingNode && len(value.Content) == 0 {
			return nil
		}
		var values []DashboardPage
		if err := decodeNodeJSON(value, &values); err != nil {
			return state.errorf(canonical, value.Line, "decode pages: %v", err)
		}
		state.pages = append(state.pages, values...)
	case "components":
		if value.Kind == yaml.SequenceNode {
			var values []DashboardPageComponent
			if err := decodeNodeJSON(value, &values); err != nil {
				return state.errorf(canonical, value.Line, "decode components: %v", err)
			}
			state.unscoped = append(state.unscoped, values...)
		} else {
			var values map[string][]DashboardPageComponent
			if err := decodeNodeJSON(value, &values); err != nil {
				return state.errorf(canonical, value.Line, "decode components: %v", err)
			}
			for pageID, components := range values {
				state.components[pageID] = append(state.components[pageID], components...)
			}
		}
	}
	return nil
}

func (state *fragmentState) addLayout(collection, path string) {
	entry := FragmentSource{Path: path}
	switch collection {
	case "visuals":
		state.layout.Visuals = append(state.layout.Visuals, entry)
	case "filters":
		state.layout.Filters = append(state.layout.Filters, entry)
	case "pages":
		state.layout.Pages = append(state.layout.Pages, entry)
	case "components":
		state.layout.Components = append(state.layout.Components, entry)
	}
}

func attachComponents(document *DashboardDocument, state *fragmentState) error {
	pageIndexes := make(map[string]int, len(document.Spec.Pages))
	for index, page := range document.Spec.Pages {
		pageIndexes[page.ID] = index
	}
	for pageID, values := range state.components {
		index, ok := pageIndexes[pageID]
		if !ok {
			return state.errorf(state.dashboardPath, 0, "component fragment references unknown page %q", pageID)
		}
		document.Spec.Pages[index].Components = append(append([]DashboardPageComponent(nil), values...), document.Spec.Pages[index].Components...)
	}
	if len(state.unscoped) > 0 {
		if len(document.Spec.Pages) != 1 {
			return state.errorf(state.dashboardPath, 0, "unscoped component fragment requires exactly one page")
		}
		document.Spec.Pages[0].Components = append(append([]DashboardPageComponent(nil), state.unscoped...), document.Spec.Pages[0].Components...)
	}
	return nil
}

func validateExpandedIDs(document DashboardDocument) error {
	visuals := make(map[string]struct{}, len(document.Spec.Visuals))
	for id := range document.Spec.Visuals {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("dashboard visual id is required after fragment expansion")
		}
		if _, exists := visuals[id]; exists {
			return fmt.Errorf("dashboard visual %q is duplicated after fragment expansion", id)
		}
		visuals[id] = struct{}{}
	}
	filters := map[string]struct{}{}
	for _, filter := range document.Spec.Filters {
		if strings.TrimSpace(filter.ID) == "" {
			return fmt.Errorf("dashboard filter id is required after fragment expansion")
		}
		if _, exists := filters[filter.ID]; exists {
			return fmt.Errorf("dashboard filter %q is duplicated after fragment expansion", filter.ID)
		}
		filters[filter.ID] = struct{}{}
	}
	pages := map[string]struct{}{}
	components := map[string]struct{}{}
	for _, page := range document.Spec.Pages {
		if strings.TrimSpace(page.ID) == "" {
			return fmt.Errorf("dashboard page id is required after fragment expansion")
		}
		if _, exists := pages[page.ID]; exists {
			return fmt.Errorf("dashboard page %q is duplicated after fragment expansion", page.ID)
		}
		pages[page.ID] = struct{}{}
		for _, component := range page.Components {
			id := componentID(component)
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("dashboard component id is required after fragment expansion")
			}
			if _, exists := components[id]; exists {
				return fmt.Errorf("dashboard component %q is duplicated after fragment expansion", id)
			}
			components[id] = struct{}{}
		}
	}
	return nil
}

func componentID(value DashboardPageComponent) string {
	switch component := value.Value.(type) {
	case *FilterDashboardPageComponent:
		return component.ID
	case *HeaderDashboardPageComponent:
		return component.ID
	case *VisualDashboardPageComponent:
		return component.ID
	default:
		return ""
	}
}

func cloneDashboardDocument(value DashboardDocument) (DashboardDocument, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return DashboardDocument{}, err
	}
	var clone DashboardDocument
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return DashboardDocument{}, err
	}
	return clone, nil
}

func decodeNodeJSON(node *yaml.Node, target any) error {
	if node == nil {
		return fmt.Errorf("empty fragment collection")
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return err
	}
	encoded, err := json.Marshal(configschema.NormalizeYAMLValue(raw))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func resolveFragmentPaths(projectRoot, dashboardDir, pattern string) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("dashboard fragment include pattern is required")
	}
	if filepath.IsAbs(pattern) || isWindowsAbsolute(pattern) {
		return nil, fmt.Errorf("dashboard fragment include pattern %q must be relative to the dashboard", pattern)
	}
	if strings.Contains(filepath.ToSlash(pattern), "**") {
		return nil, fmt.Errorf("dashboard fragment include pattern %q uses unsupported ** glob", pattern)
	}
	clean := filepath.Clean(filepath.FromSlash(pattern))
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if part == ".." {
			return nil, fmt.Errorf("dashboard fragment include pattern %q escapes the project boundary", pattern)
		}
	}
	matches, err := filepath.Glob(filepath.Join(dashboardDir, clean))
	if err != nil {
		return nil, fmt.Errorf("dashboard fragment include pattern %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("dashboard fragment include pattern %q matched no files", pattern)
	}
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project boundary: %w", err)
	}
	sort.Strings(matches)
	result, seen := make([]string, 0, len(matches)), map[string]struct{}{}
	for _, match := range matches {
		canonical, err := filepath.EvalSymlinks(match)
		if err != nil {
			return nil, fmt.Errorf("dashboard fragment include %q cannot be resolved: %w", pattern, err)
		}
		relative, err := filepath.Rel(root, canonical)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("dashboard fragment include %q resolves outside the project boundary", pattern)
		}
		info, err := os.Stat(match)
		if err != nil {
			return nil, fmt.Errorf("dashboard fragment include %q: %w", pattern, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("dashboard fragment include %q matched directory %s", pattern, match)
		}
		ext := strings.ToLower(filepath.Ext(match))
		if ext != ".yaml" && ext != ".yml" {
			return nil, fmt.Errorf("dashboard fragment include %q matched non-YAML file %s", pattern, match)
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result, nil
}

func isWindowsAbsolute(path string) bool {
	return len(path) >= 3 && ((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func fragmentMapNode(root *yaml.Node, key string) (*yaml.Node, bool) {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, false
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == key {
			return root.Content[index+1], true
		}
	}
	return nil, false
}

func fragmentMapWithout(root *yaml.Node, omit string) *yaml.Node {
	if root == nil || root.Kind != yaml.MappingNode {
		return root
	}
	copyNode := *root
	copyNode.Content = make([]*yaml.Node, 0, len(root.Content))
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == omit {
			continue
		}
		copyNode.Content = append(copyNode.Content, root.Content[index], root.Content[index+1])
	}
	return &copyNode
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (state *fragmentState) errorf(path string, line int, format string, args ...any) error {
	return &FragmentError{Path: path, Line: line, Message: fmt.Sprintf(format, args...)}
}
