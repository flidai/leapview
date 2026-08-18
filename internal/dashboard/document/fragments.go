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

// Diagnostic allows callers to project fragment failures into the same
// source-position diagnostic stream used by project schema/compiler errors.
func (e *FragmentError) Diagnostic() configschema.Diagnostic {
	if e == nil {
		return configschema.Diagnostic{Severity: configschema.SeverityError, Code: "compiler.fragment"}
	}
	return configschema.Diagnostic{File: e.Path, Line: e.Line, Severity: configschema.SeverityError, Code: "compiler.fragment", Message: e.Message}
}

type fragmentState struct {
	visuals               map[string]DashboardVisual
	visualOrigins         map[string]idOrigin
	filters               []DashboardFilter
	filterOrigins         []idOrigin
	pages                 []DashboardPage
	pageOrigins           []idOrigin
	components            map[string][]DashboardPageComponent
	componentOrigins      map[string][]idOrigin
	localComponentOrigins map[string][]idOrigin
	finalComponentOrigins map[string][]idOrigin
	unscoped              []DashboardPageComponent
	unscopedOrigins       []idOrigin
	active                map[string]struct{}
	paths                 []string
	layout                FragmentLayout
	projectRoot           string
	dashboardDir          string
	dashboardPath         string
}

type idOrigin struct {
	path string
	line int
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
	canonicalDashboard, err := filepath.EvalSymlinks(dashboardPath)
	if err != nil {
		return FragmentExpansion{}, fmt.Errorf("resolve dashboard path: %w", err)
	}
	if info, statErr := os.Stat(canonicalDashboard); statErr != nil {
		return FragmentExpansion{}, fmt.Errorf("resolve dashboard path: %w", statErr)
	} else if info.IsDir() {
		return FragmentExpansion{}, fmt.Errorf("dashboard path %q is a directory", dashboardPath)
	}
	relativeDashboard, err := filepath.Rel(root, canonicalDashboard)
	if err != nil || filepath.IsAbs(relativeDashboard) || relativeDashboard == ".." || strings.HasPrefix(relativeDashboard, ".."+string(filepath.Separator)) {
		return FragmentExpansion{}, fmt.Errorf("dashboard path %q resolves outside project boundary", dashboardPath)
	}
	state := &fragmentState{
		visuals: make(map[string]DashboardVisual), visualOrigins: make(map[string]idOrigin), components: make(map[string][]DashboardPageComponent), componentOrigins: make(map[string][]idOrigin), localComponentOrigins: make(map[string][]idOrigin), finalComponentOrigins: make(map[string][]idOrigin),
		active: make(map[string]struct{}), projectRoot: root, dashboardDir: filepath.Dir(canonicalDashboard), dashboardPath: filepath.ToSlash(relativeDashboard),
	}
	if document.Spec.Includes == nil {
		return FragmentExpansion{Document: document}, state.validateExpandedIDs(document)
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
		state.visualOrigins[id] = idOrigin{path: state.dashboardPath}
	}
	state.filters = append(state.filters, document.Spec.Filters...)
	for range document.Spec.Filters {
		state.filterOrigins = append(state.filterOrigins, idOrigin{path: state.dashboardPath})
	}
	state.pages = append(state.pages, document.Spec.Pages...)
	for range document.Spec.Pages {
		state.pageOrigins = append(state.pageOrigins, idOrigin{path: state.dashboardPath})
	}
	for _, page := range document.Spec.Pages {
		for range page.Components {
			state.localComponentOrigins[page.ID] = append(state.localComponentOrigins[page.ID], idOrigin{path: state.dashboardPath})
		}
	}
	document.Spec.Visuals, document.Spec.Filters, document.Spec.Pages = state.visuals, state.filters, state.pages
	if err := attachComponents(&document, state); err != nil {
		return FragmentExpansion{}, err
	}
	document.Spec.Includes = nil
	if err := state.validateExpandedIDs(document); err != nil {
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
	relativePath, err := state.relativePath(canonical)
	if err != nil {
		return err
	}
	state.paths = appendUnique(state.paths, relativePath)
	state.addLayout(expected, relativePath)
	content, err := os.ReadFile(canonical)
	if err != nil {
		return state.errorf(canonical, 0, "read fragment: %v", err)
	}
	normalized, err := configschema.NormalizeJSONDocument(canonical, content)
	if err != nil {
		return err
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
	fragmentJSON, nestedJSON, err := selectFragmentJSON(normalized, expected)
	if err != nil {
		return state.errorf(canonical, root.Line, "%v", err)
	}
	if nestedJSON != nil {
		var includes DashboardIncludes
		if err := decodeJSONBytes(nestedJSON, &includes); err != nil {
			return state.errorf(canonical, root.Line, "decode includes: %v", err)
		}
		if err := state.expandIncludes(&includes); err != nil {
			return err
		}
	}
	collectionNode := fragmentCollectionNode(&root, expected)
	switch expected {
	case "visuals":
		var values map[string]DashboardVisual
		if err := decodeJSONBytes(fragmentJSON, &values); err != nil {
			return state.errorf(canonical, root.Line, "decode visuals: %v", err)
		}
		for id, visual := range values {
			if _, exists := state.visuals[id]; exists {
				return state.errorf(relativePath, collectionMapKeyLine(collectionNode, id), "visual %q is defined more than once", id)
			}
			state.visuals[id] = visual
			state.visualOrigins[id] = idOrigin{path: relativePath, line: collectionMapKeyLine(collectionNode, id)}
		}
	case "filters":
		var values []DashboardFilter
		if err := decodeJSONBytes(fragmentJSON, &values); err != nil {
			return state.errorf(canonical, root.Line, "decode filters: %v", err)
		}
		state.filters = append(state.filters, values...)
		for index := range values {
			state.filterOrigins = append(state.filterOrigins, idOrigin{path: relativePath, line: collectionSequenceLine(collectionNode, index)})
		}
	case "pages":
		var values []DashboardPage
		if err := decodeJSONBytes(fragmentJSON, &values); err != nil {
			return state.errorf(canonical, root.Line, "decode pages: %v", err)
		}
		state.pages = append(state.pages, values...)
		for index := range values {
			state.pageOrigins = append(state.pageOrigins, idOrigin{path: relativePath, line: collectionSequenceLine(collectionNode, index)})
			for range values[index].Components {
				state.localComponentOrigins[values[index].ID] = append(state.localComponentOrigins[values[index].ID], idOrigin{path: relativePath, line: collectionSequenceLine(collectionNode, index)})
			}
		}
	case "components":
		var shape any
		if err := json.Unmarshal(fragmentJSON, &shape); err != nil {
			return state.errorf(canonical, root.Line, "decode components: %v", err)
		}
		if _, sequence := shape.([]any); sequence {
			var values []DashboardPageComponent
			if err := decodeJSONBytes(fragmentJSON, &values); err != nil {
				return state.errorf(canonical, root.Line, "decode components: %v", err)
			}
			state.unscoped = append(state.unscoped, values...)
			for index := range values {
				state.unscopedOrigins = append(state.unscopedOrigins, idOrigin{path: relativePath, line: collectionSequenceLine(collectionNode, index)})
			}
		} else {
			var values map[string][]DashboardPageComponent
			if err := decodeJSONBytes(fragmentJSON, &values); err != nil {
				return state.errorf(canonical, root.Line, "decode components: %v", err)
			}
			for pageID, components := range values {
				state.components[pageID] = append(state.components[pageID], components...)
				lines := collectionPageComponentLines(collectionNode, pageID, len(components))
				for _, line := range lines {
					state.componentOrigins[pageID] = append(state.componentOrigins[pageID], idOrigin{path: relativePath, line: line})
				}
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
	for _, page := range document.Spec.Pages {
		origins := make([]idOrigin, 0, len(page.Components))
		if len(state.unscoped) > 0 && len(document.Spec.Pages) == 1 {
			origins = append(origins, state.unscopedOrigins...)
		}
		origins = append(origins, state.componentOrigins[page.ID]...)
		origins = append(origins, state.localComponentOrigins[page.ID]...)
		state.finalComponentOrigins[page.ID] = origins
	}
	return nil
}

func validateExpandedIDs(document DashboardDocument) error {
	return (&fragmentState{}).validateExpandedIDs(document)
}

func (state *fragmentState) validateExpandedIDs(document DashboardDocument) error {
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
	for index, filter := range document.Spec.Filters {
		origin := originAt(state.filterOrigins, index)
		if strings.TrimSpace(filter.ID) == "" {
			return state.errorf(origin.path, origin.line, "dashboard filter id is required after fragment expansion")
		}
		if _, exists := filters[filter.ID]; exists {
			return state.errorf(origin.path, origin.line, "dashboard filter %q is duplicated after fragment expansion", filter.ID)
		}
		filters[filter.ID] = struct{}{}
	}
	pages := map[string]struct{}{}
	components := map[string]struct{}{}
	for pageIndex, page := range document.Spec.Pages {
		pageOrigin := originAt(state.pageOrigins, pageIndex)
		if strings.TrimSpace(page.ID) == "" {
			return state.errorf(pageOrigin.path, pageOrigin.line, "dashboard page id is required after fragment expansion")
		}
		if _, exists := pages[page.ID]; exists {
			return state.errorf(pageOrigin.path, pageOrigin.line, "dashboard page %q is duplicated after fragment expansion", page.ID)
		}
		pages[page.ID] = struct{}{}
		for componentIndex, component := range page.Components {
			origin := componentOriginAt(state, page.ID, componentIndex)
			id := componentID(component)
			if strings.TrimSpace(id) == "" {
				return state.errorf(origin.path, origin.line, "dashboard component id is required after fragment expansion")
			}
			if _, exists := components[id]; exists {
				return state.errorf(origin.path, origin.line, "dashboard component %q is duplicated after fragment expansion", id)
			}
			components[id] = struct{}{}
		}
	}
	return nil
}

func originAt(values []idOrigin, index int) idOrigin {
	if index >= 0 && index < len(values) {
		return values[index]
	}
	return idOrigin{}
}

func componentOriginAt(state *fragmentState, pageID string, index int) idOrigin {
	if values := state.finalComponentOrigins[pageID]; index >= 0 && index < len(values) {
		return values[index]
	}
	return idOrigin{}
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

func decodeJSONBytes(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func selectFragmentJSON(normalized []byte, expected string) ([]byte, []byte, error) {
	var root any
	if err := json.Unmarshal(normalized, &root); err != nil {
		return nil, nil, err
	}
	if object, ok := root.(map[string]any); ok {
		var nested []byte
		if includes, exists := object["includes"]; exists {
			var err error
			nested, err = json.Marshal(includes)
			if err != nil {
				return nil, nil, err
			}
		}
		if collection, exists := object[expected]; exists {
			for key := range object {
				if key != expected && key != "includes" {
					return nil, nil, fmt.Errorf("fragment collection %q cannot contain %q", expected, key)
				}
			}
			value, err := json.Marshal(collection)
			return value, nested, err
		}
		delete(object, "includes")
		value, err := json.Marshal(object)
		return value, nested, err
	}
	return normalized, nil, nil
}

func fragmentCollectionNode(root *yaml.Node, expected string) *yaml.Node {
	if root == nil {
		return nil
	}
	if root.Kind == yaml.MappingNode {
		for index := 0; index+1 < len(root.Content); index += 2 {
			if root.Content[index].Value == expected {
				return root.Content[index+1]
			}
		}
	}
	return root
}

func collectionMapKeyLine(node *yaml.Node, key string) int {
	if node == nil || node.Kind != yaml.MappingNode {
		return 0
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index].Line
		}
	}
	return node.Line
}

func collectionSequenceLine(node *yaml.Node, index int) int {
	if node != nil && node.Kind == yaml.SequenceNode && index >= 0 && index < len(node.Content) {
		return node.Content[index].Line
	}
	if node != nil {
		return node.Line
	}
	return 0
}

func collectionPageComponentLines(node *yaml.Node, pageID string, count int) []int {
	if node == nil || node.Kind != yaml.MappingNode {
		return make([]int, count)
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == pageID {
			lines := make([]int, count)
			for childIndex := range lines {
				lines[childIndex] = collectionSequenceLine(node.Content[index+1], childIndex)
			}
			return lines
		}
	}
	return make([]int, count)
}

func (state *fragmentState) relativePath(path string) (string, error) {
	relative, err := filepath.Rel(state.projectRoot, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", state.errorf(path, 0, "fragment resolves outside project boundary")
	}
	return filepath.ToSlash(relative), nil
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

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (state *fragmentState) errorf(path string, line int, format string, args ...any) error {
	if filepath.IsAbs(path) {
		if relative, err := filepath.Rel(state.projectRoot, path); err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			path = filepath.ToSlash(relative)
		}
	}
	return &FragmentError{Path: path, Line: line, Message: fmt.Sprintf(format, args...)}
}
