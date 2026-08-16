package dashboard

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/flidai/leapview/internal/dashboard/catalog"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type Signals struct {
	Runtime                   Runtime                       `json:"runtime"`
	VisualWindowCommand       VisualizationWindowRequest    `json:"visualWindowCommand"`
	InteractionCommand        InteractionCommand            `json:"interactionCommand"`
	SpatialInteractionCommand SpatialSelectionCommand       `json:"spatialInteractionCommand"`
	FilterCommand             dashboardfilter.Command       `json:"filterCommand"`
	FilterOptionRequest       dashboardfilter.OptionRequest `json:"filterOptionRequest"`
	NavigationCommand         NavigationCommand             `json:"navigationCommand"`
	InteractionSelections     []InteractionSelection        `json:"interactionSelections"`
	SpatialSelections         []SpatialInteractionSelection `json:"spatialSelections"`
}

type NavigationCommand struct {
	PageID             string `json:"pageID"`
	BaseFilterRevision uint64 `json:"baseFilterRevision"`
	ClientMutationID   string `json:"clientMutationID"`
}

// Browser command contracts are owned by the visualization IR. These aliases
// keep dashboard orchestration readable without creating a second wire model.
type VisualizationWindowRequest = visualizationir.VisualizationWindowRequest
type SpatialBounds = visualizationir.VisualizationSpatialBounds

type Catalog = catalog.Catalog
type CatalogProject = catalog.Project
type CatalogModel = catalog.Model
type CatalogDashboard = catalog.Dashboard

type Page struct {
	ID             string                             `json:"id" yaml:"id"`
	Title          string                             `json:"title" yaml:"title"`
	Description    string                             `json:"description,omitempty" yaml:"description"`
	Canvas         PageCanvas                         `json:"canvas" yaml:"canvas"`
	Grid           PageGrid                           `json:"grid" yaml:"grid"`
	FilterBindings map[string]dashboardfilter.Binding `json:"filterBindings,omitempty" yaml:"filter_bindings,omitempty"`
	Visuals        []PageVisual                       `json:"visuals" yaml:"visuals"`
	Width          int                                `json:"width,omitempty" yaml:"-"`
	Height         int                                `json:"height,omitempty" yaml:"-"`
}

type PageCanvas struct {
	Width  int `json:"width" yaml:"width"`
	Height int `json:"height" yaml:"height"`
}

type PageGrid struct {
	Columns   int `json:"columns" yaml:"columns"`
	RowHeight int `json:"rowHeight" yaml:"row_height"`
	Gap       int `json:"gap" yaml:"gap"`
	Padding   int `json:"padding" yaml:"padding"`
}

type PagePlacement struct {
	Col     int `json:"col" yaml:"col"`
	Row     int `json:"row" yaml:"row"`
	ColSpan int `json:"colSpan" yaml:"col_span"`
	RowSpan int `json:"rowSpan" yaml:"row_span"`
}

func (p Page) WithDefaults() Page {
	if p.Canvas.Width <= 0 {
		if p.Width > 0 {
			p.Canvas.Width = p.Width
		} else {
			p.Canvas.Width = 1366
		}
	}
	if p.Canvas.Height <= 0 {
		if p.Height > 0 {
			p.Canvas.Height = p.Height
		} else {
			p.Canvas.Height = 940
		}
	}
	if p.Grid.Columns <= 0 {
		p.Grid.Columns = 12
	}
	if p.Grid.RowHeight <= 0 {
		p.Grid.RowHeight = 48
	}
	if p.Grid.Gap < 0 {
		p.Grid.Gap = 0
	}
	if p.Grid.Gap == 0 {
		p.Grid.Gap = 16
	}
	if p.Grid.Padding < 0 {
		p.Grid.Padding = 0
	}
	p.Width = p.Canvas.Width
	p.Height = p.Canvas.Height
	return p
}

func (p Page) PlacedVisuals() []PageVisual {
	p = p.WithDefaults()
	visuals := make([]PageVisual, 0, len(p.Visuals))
	for _, visual := range p.Visuals {
		if visual.Placement.IsZero() {
			visuals = append(visuals, visual)
			continue
		}
		visual.X, visual.Y, visual.Width, visual.Height = p.Grid.Rect(p.Canvas, visual.Placement)
		visuals = append(visuals, visual)
	}
	return visuals
}

func (g PageGrid) Rect(canvas PageCanvas, placement PagePlacement) (float64, float64, float64, float64) {
	g = Page{Canvas: canvas, Grid: g}.WithDefaults().Grid
	availableWidth := float64(canvas.Width - (g.Padding * 2) - (g.Gap * (g.Columns - 1)))
	colWidth := availableWidth / float64(g.Columns)
	x := float64(g.Padding) + float64(placement.Col-1)*(colWidth+float64(g.Gap))
	y := float64(g.Padding) + float64(placement.Row-1)*float64(g.RowHeight+g.Gap)
	width := float64(placement.ColSpan)*colWidth + float64(placement.ColSpan-1)*float64(g.Gap)
	height := float64(placement.RowSpan*g.RowHeight) + float64((placement.RowSpan-1)*g.Gap)
	return x, y, width, height
}

func (p PagePlacement) IsZero() bool {
	return p.Col == 0 && p.Row == 0 && p.ColSpan == 0 && p.RowSpan == 0
}

type PageVisual struct {
	ID           string                       `json:"id" yaml:"id"`
	Kind         string                       `json:"kind" yaml:"kind"`
	Visual       string                       `json:"visual,omitempty" yaml:"visual"`
	Binding      dashboardfilter.BindingRef   `json:"binding,omitempty" yaml:"binding,omitempty"`
	Presentation dashboardfilter.Presentation `json:"presentation,omitempty" yaml:"presentation,omitempty"`
	Description  string                       `json:"description,omitempty" yaml:"description"`
	Placement    PagePlacement                `json:"placement" yaml:"placement"`
	X            float64                      `json:"x" yaml:"-"`
	Y            float64                      `json:"y" yaml:"-"`
	Width        float64                      `json:"width" yaml:"-"`
	Height       float64                      `json:"height" yaml:"-"`
	Eyebrow      string                       `json:"eyebrow,omitempty" yaml:"eyebrow"`
	Title        string                       `json:"title,omitempty" yaml:"title"`
	Subtitle     string                       `json:"subtitle,omitempty" yaml:"subtitle"`
	Badges       []string                     `json:"badges,omitempty" yaml:"badges"`
}

type Filters struct {
	Selections          []InteractionSelection        `json:"selections"`
	SpatialSelections   []SpatialInteractionSelection `json:"spatialSelections"`
	InteractionRevision uint64                        `json:"interactionRevision"`
	CompiledState       *dashboardfilter.State        `json:"-"`
	ServingStateID      string                        `json:"-"`
	ActivePageID        string                        `json:"-"`
	DataRevisions       map[string]int64              `json:"-"`
}

type Runtime struct {
	ClientID         string `json:"clientId"`
	StreamInstanceID string `json:"streamInstanceId"`
	DashboardID      string `json:"dashboardId"`
	PageID           string `json:"pageId"`
	ModelID          string `json:"modelId"`
}

func (f Filters) WithDefaults() Filters {
	if f.Selections == nil {
		f.Selections = []InteractionSelection{}
	}
	if f.SpatialSelections == nil {
		f.SpatialSelections = []SpatialInteractionSelection{}
	}
	return f
}

type SpatialSelectionCommand struct {
	VisualID            string                                                `json:"visualID"`
	SpecRevision        string                                                `json:"specRevision"`
	DataRevision        int64                                                 `json:"dataRevision"`
	ServingStateID      string                                                `json:"servingStateID"`
	FilterRevision      int64                                                 `json:"filterRevision"`
	InteractionRevision int64                                                 `json:"interactionRevision"`
	InteractionID       string                                                `json:"interactionID"`
	Action              string                                                `json:"action"`
	Gesture             visualizationir.VisualizationSpatialSelectionGesture  `json:"gesture"`
	Geometry            visualizationir.VisualizationSpatialSelectionGeometry `json:"geometry"`
}

type SpatialInteractionSelection struct {
	VisualID      string                                                `json:"visualID"`
	InteractionID string                                                `json:"interactionID"`
	Gesture       visualizationir.VisualizationSpatialSelectionGesture  `json:"gesture"`
	Geometry      visualizationir.VisualizationSpatialSelectionGeometry `json:"geometry"`
	Order         int                                                   `json:"order"`
}

type InteractionSelection struct {
	ID              string                      `json:"id"`
	SourceKind      string                      `json:"sourceKind"`
	SourceID        string                      `json:"sourceId"`
	InteractionKind string                      `json:"interactionKind"`
	Entries         []InteractionSelectionEntry `json:"entries"`
	Label           string                      `json:"label"`
	Order           int                         `json:"order"`
}

type InteractionSelectionEntry struct {
	Mappings []InteractionSelectionMapping `json:"mappings"`
	Label    string                        `json:"label,omitempty"`
}

// InteractionSelectionValue is a JSON scalar carried from a rendered datum
// into a stored interaction selection. Arrays and objects are never valid.
type InteractionSelectionValue any

type InteractionSelectionMapping struct {
	Field string                    `json:"field"`
	Fact  string                    `json:"fact,omitempty"`
	Grain string                    `json:"grain,omitempty"`
	Value InteractionSelectionValue `json:"value"`
	Label string                    `json:"label,omitempty"`

	decodedFromJSON bool
	valuePresent    bool
}

func (m *InteractionSelectionMapping) UnmarshalJSON(data []byte) error {
	type wireMapping InteractionSelectionMapping
	var wire wireMapping
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = InteractionSelectionMapping(wire)
	m.decodedFromJSON = true
	_, m.valuePresent = fields["value"]
	return nil
}

// HasValue distinguishes an explicit null selection from a missing JSON value.
func (m InteractionSelectionMapping) HasValue() bool {
	return !m.decodedFromJSON || m.valuePresent
}

type InteractionCommand struct {
	SourceKind          string                      `json:"sourceKind"`
	SourceID            string                      `json:"sourceId"`
	InteractionKind     string                      `json:"interactionKind"`
	Action              string                      `json:"action"`
	Toggle              bool                        `json:"toggle"`
	Mappings            []InteractionCommandMapping `json:"mappings"`
	SpecRevision        string                      `json:"specRevision"`
	DataRevision        int64                       `json:"dataRevision"`
	ServingStateID      string                      `json:"servingStateID"`
	FilterRevision      int64                       `json:"filterRevision"`
	InteractionRevision int64                       `json:"interactionRevision"`
}

const UIRowSelectionField = "__leapview.rowKey"

type InteractionCommandMapping struct {
	Field string                    `json:"field"`
	Fact  string                    `json:"fact,omitempty"`
	Grain string                    `json:"grain,omitempty"`
	Value InteractionSelectionValue `json:"value"`
	Label string                    `json:"label,omitempty"`

	decodedFromJSON bool
	valuePresent    bool
}

func (m *InteractionCommandMapping) UnmarshalJSON(data []byte) error {
	type wireMapping InteractionCommandMapping
	var wire wireMapping
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = InteractionCommandMapping(wire)
	m.decodedFromJSON = true
	_, m.valuePresent = fields["value"]
	return nil
}

// HasValue distinguishes a deliberately selected null from a missing JSON
// member. Programmatically constructed commands are treated as explicit.
func (m InteractionCommandMapping) HasValue() bool {
	return !m.decodedFromJSON || m.valuePresent
}

func IsInteractionSelectionScalar(value InteractionSelectionValue) bool {
	switch value := value.(type) {
	case nil, string, bool:
		return true
	case float64:
		return !math.IsNaN(value) && !math.IsInf(value, 0)
	case float32:
		return !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func InteractionSelectionValueMatchesType(value InteractionSelectionValue, semanticType string, grain ...string) bool {
	if value == nil {
		return true
	}
	switch semanticType {
	case "":
		return IsInteractionSelectionScalar(value)
	case "string":
		_, ok := value.(string)
		return ok
	case "date":
		text, ok := value.(string)
		if !ok {
			return false
		}
		if len(grain) > 0 && grain[0] != "" {
			_, err := ParseInteractionSelectionTime(text, grain[0])
			return err == nil
		}
		_, err := time.Parse(time.DateOnly, text)
		return err == nil
	case "timestamp":
		text, ok := value.(string)
		if !ok {
			return false
		}
		if len(grain) > 0 && grain[0] != "" {
			_, err := ParseInteractionSelectionTime(text, grain[0])
			return err == nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", time.DateOnly} {
			if _, err := time.Parse(layout, text); err == nil {
				return true
			}
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		switch value.(type) {
		case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// ParseInteractionSelectionTime accepts the canonical labels emitted by time-
// grained queries as well as full date and timestamp values.
func ParseInteractionSelectionTime(value string, grain string) (time.Time, error) {
	if grain == "quarter" && len(value) == 7 && value[4:6] == "-Q" && value[6] >= '1' && value[6] <= '4' {
		year, err := time.Parse("2006", value[:4])
		if err == nil {
			return year.AddDate(0, int(value[6]-'1')*3, 0), nil
		}
	}
	grainLayouts := map[string]string{"month": "2006-01", "year": "2006"}
	if layout := grainLayouts[grain]; layout != "" {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", time.DateOnly} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid %s time value %q", grain, value)
}

func (c InteractionCommand) IsEmpty() bool {
	if c.SourceKind == "" || c.SourceID == "" || c.InteractionKind == "" {
		return true
	}
	if c.Action == "clear" {
		return false
	}
	if len(c.Mappings) == 0 {
		return true
	}
	for _, mapping := range c.Mappings {
		if mapping.Field == "" {
			return true
		}
	}
	return false
}

func (f Filters) ApplyInteraction(command InteractionCommand) Filters {
	f = f.WithDefaults()
	if command.IsEmpty() {
		return f
	}

	selectionID := command.SourceKind + ":" + command.SourceID + ":" + command.InteractionKind
	next := make([]InteractionSelection, 0, len(f.Selections)+1)
	maxOrder := 0
	changed := false

	for _, selection := range f.Selections {
		if selection.Order > maxOrder {
			maxOrder = selection.Order
		}
		if selection.ID == selectionID || (selection.SourceKind == command.SourceKind && selection.SourceID == command.SourceID && selection.InteractionKind == command.InteractionKind) {
			changed = true
			if command.Action == "clear" {
				continue
			}
			selection.ID = selectionID
			if command.Action == "replace" {
				selection.Entries = updateSelectionEntries(nil, command.Mappings, false)
			} else {
				selection.Entries = updateSelectionEntries(selection.Entries, command.Mappings, command.Toggle)
			}
			selection.Label = interactionSelectionLabel(selection.Entries)
			if len(selection.Entries) > 0 {
				next = append(next, selection)
			}
			continue
		}
		next = append(next, selection)
	}

	if !changed && command.Action != "clear" {
		entries := updateSelectionEntries(nil, command.Mappings, false)
		if len(entries) > 0 {
			next = append(next, InteractionSelection{
				ID:              selectionID,
				SourceKind:      command.SourceKind,
				SourceID:        command.SourceID,
				InteractionKind: command.InteractionKind,
				Entries:         entries,
				Label:           interactionSelectionLabel(entries),
				Order:           maxOrder + 1,
			})
		}
	}

	if !reflect.DeepEqual(f.Selections, next) {
		f.InteractionRevision++
	}
	f.Selections = next
	return f
}

// ApplySpatialInteraction stores one canonical geometry per source visual and
// interaction. Replacing a spatial selection moves it to the end of the stable
// selection order; clearing it cannot affect selections owned by other maps.
func (f Filters) ApplySpatialInteraction(command SpatialSelectionCommand) Filters {
	f = f.WithDefaults()
	if command.VisualID == "" || command.InteractionID == "" || (command.Action != "set" && command.Action != "clear") {
		return f
	}

	next := make([]SpatialInteractionSelection, 0, len(f.SpatialSelections)+1)
	maxOrder := 0
	for _, selection := range f.SpatialSelections {
		if selection.Order > maxOrder {
			maxOrder = selection.Order
		}
		if selection.VisualID == command.VisualID && selection.InteractionID == command.InteractionID {
			continue
		}
		next = append(next, selection)
	}
	if command.Action == "set" && command.Geometry.Value != nil {
		next = append(next, SpatialInteractionSelection{
			VisualID:      command.VisualID,
			InteractionID: command.InteractionID,
			Gesture:       command.Gesture,
			Geometry:      command.Geometry,
			Order:         maxOrder + 1,
		})
	}
	if !reflect.DeepEqual(f.SpatialSelections, next) {
		f.InteractionRevision++
	}
	f.SpatialSelections = next
	return f
}

func updateSelectionEntries(existing []InteractionSelectionEntry, incoming []InteractionCommandMapping, toggle bool) []InteractionSelectionEntry {
	entry := interactionSelectionEntry(incoming)
	if len(entry.Mappings) == 0 {
		return nil
	}

	out := make([]InteractionSelectionEntry, 0, len(existing)+1)
	found := false
	for _, existingEntry := range existing {
		if selectionEntriesEqual(existingEntry, entry) {
			found = true
			if toggle {
				continue
			}
		}
		out = append(out, copySelectionEntry(existingEntry))
	}
	if !found {
		out = append(out, entry)
	}
	return out
}

func interactionSelectionEntry(incoming []InteractionCommandMapping) InteractionSelectionEntry {
	mappings := make([]InteractionSelectionMapping, 0, len(incoming))
	for _, mapping := range incoming {
		mappings = append(mappings, InteractionSelectionMapping{
			Field: mapping.Field,
			Fact:  mapping.Fact,
			Grain: mapping.Grain,
			Value: mapping.Value,
			Label: defaultString(mapping.Label, interactionSelectionValueLabel(mapping.Value)),
		})
	}
	entry := InteractionSelectionEntry{Mappings: mappings}
	entry.Label = interactionEntryLabel(entry)
	return entry
}

func selectionEntriesContain(existing []InteractionSelectionEntry, incoming InteractionSelectionEntry) bool {
	for _, entry := range existing {
		if selectionEntriesEqual(entry, incoming) {
			return true
		}
	}
	return false
}

func selectionEntriesEqual(left, right InteractionSelectionEntry) bool {
	if len(left.Mappings) != len(right.Mappings) {
		return false
	}
	values := make(map[string]int, len(left.Mappings))
	for _, mapping := range left.Mappings {
		values[selectionMappingKey(mapping)]++
	}
	for _, mapping := range right.Mappings {
		key := selectionMappingKey(mapping)
		if values[key] == 0 {
			return false
		}
		values[key]--
	}
	return true
}

func selectionMappingKey(mapping InteractionSelectionMapping) string {
	value, err := json.Marshal(mapping.Value)
	if err != nil {
		value = []byte(fmt.Sprintf("%T:%v", mapping.Value, mapping.Value))
	}
	return mapping.Field + "\x00" + mapping.Fact + "\x00" + mapping.Grain + "\x00" + string(value)
}

func copySelectionEntry(entry InteractionSelectionEntry) InteractionSelectionEntry {
	out := InteractionSelectionEntry{
		Mappings: make([]InteractionSelectionMapping, len(entry.Mappings)),
		Label:    entry.Label,
	}
	copy(out.Mappings, entry.Mappings)
	return out
}

func interactionSelectionLabel(entries []InteractionSelectionEntry) string {
	if len(entries) == 0 {
		return ""
	}
	labels := make([]string, 0, len(entries))
	for _, entry := range entries {
		label := entry.Label
		if label == "" {
			label = interactionEntryLabel(entry)
		}
		labels = append(labels, label)
	}
	return joinValues(labels)
}

func interactionEntryLabel(entry InteractionSelectionEntry) string {
	if len(entry.Mappings) == 0 {
		return ""
	}
	labels := make([]string, 0, len(entry.Mappings))
	for _, mapping := range entry.Mappings {
		label := mapping.Label
		if label == "" {
			label = selectionLabel(mapping.Field, []string{interactionSelectionValueLabel(mapping.Value)})
		}
		labels = append(labels, label)
	}
	return joinValues(labels)
}

func interactionSelectionValueLabel(value InteractionSelectionValue) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprint(value)
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func selectionLabel(field string, values []string) string {
	if len(values) == 1 {
		return field + " is " + values[0]
	}
	return field + " in " + joinValues(values)
}

func joinValues(values []string) string {
	if len(values) == 0 {
		return ""
	}
	out := values[0]
	for _, value := range values[1:] {
		out += ", " + value
	}
	return out
}

type Patch struct {
	Filters Filters                                          `json:"filters"`
	Status  Status                                           `json:"status"`
	Visuals map[string]visualizationir.VisualizationEnvelope `json:"visuals"`
}

type Status struct {
	Loading         bool     `json:"loading"`
	Error           string   `json:"error"`
	RefreshID       string   `json:"refreshId"`
	Generation      int64    `json:"generation"`
	LastUpdated     string   `json:"lastUpdated"`
	SetupRequired   bool     `json:"setupRequired"`
	ProgressPercent *float64 `json:"progressPercent"`
}

func NormalizeProgressPercent(percent *float64, loading bool) *float64 {
	if percent == nil || math.IsNaN(*percent) || math.IsInf(*percent, 0) {
		value := float64(100)
		if loading {
			value = 0
		}
		return &value
	}
	value := math.Max(0, math.Min(100, *percent))
	return &value
}

type Datum map[string]any

type InteractionConfig struct {
	Kind     string                     `json:"kind"`
	Toggle   bool                       `json:"toggle"`
	Mappings []InteractionConfigMapping `json:"mappings"`
	Targets  []string                   `json:"targets,omitempty"`
}

type InteractionConfigMapping struct {
	Field string `json:"field"`
	Fact  string `json:"fact,omitempty"`
	Grain string `json:"grain,omitempty"`
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

type TableRequest struct {
	Table        string    `json:"visual"`
	Block        string    `json:"block"`
	Start        int       `json:"start"`
	Count        int       `json:"count"`
	RequestSeq   int       `json:"requestSeq"`
	Sort         TableSort `json:"sort"`
	ResetVersion int       `json:"resetVersion"`
}

const (
	TableChunkSize         = 50
	TableInteractiveRowCap = 10000
	TableRowHeight         = 34
	TableMaxRequestCount   = 1000
)

type TableStyle struct {
	Density string `json:"density" yaml:"density"`
	Zebra   *bool  `json:"zebra" yaml:"zebra"`
	Grid    string `json:"grid" yaml:"grid"`
}

func (s TableStyle) WithDefaults() TableStyle {
	if s.Density != "compact" && s.Density != "spacious" {
		s.Density = "comfortable"
	}
	if s.Zebra == nil {
		zebra := true
		s.Zebra = &zebra
	}
	switch s.Grid {
	case "none", "columns", "full":
	default:
		s.Grid = "rows"
	}
	return s
}

func (s TableStyle) RowHeight() int {
	switch s.WithDefaults().Density {
	case "compact":
		return 28
	case "spacious":
		return 42
	default:
		return TableRowHeight
	}
}

func DefaultTableRequest() TableRequest {
	return TableRequest{
		Table: "orders",
		Block: "all",
		Start: 0,
		Count: TableChunkSize,
		Sort:  TableSort{Key: "purchase_date", Direction: "desc"},
	}
}

func (r TableRequest) WithDefaults() TableRequest {
	defaults := DefaultTableRequest()
	if r.Table == "" {
		r.Table = defaults.Table
	}
	if r.Block == "" {
		r.Block = defaults.Block
	}
	if r.Block != "all" && r.Block != "a" && r.Block != "b" && r.Block != "c" {
		r.Block = defaults.Block
	}
	if r.Count <= 0 {
		r.Count = defaults.Count
	}
	if r.Count > TableMaxRequestCount {
		r.Count = TableMaxRequestCount
	}
	if r.Start < 0 {
		r.Start = 0
	}
	if r.RequestSeq < 0 {
		r.RequestSeq = 0
	}
	if r.Sort.Key == "" {
		r.Sort = defaults.Sort
	}
	if r.Sort.Direction != "asc" && r.Sort.Direction != "desc" {
		r.Sort.Direction = defaults.Sort.Direction
	}
	return r
}

func (r TableRequest) Reset() TableRequest {
	r = r.WithDefaults()
	r.Block = "all"
	r.Start = 0
	r.Count = TableChunkSize
	r.ResetVersion++
	return r
}

type TableSort struct {
	Key       string `json:"key"`
	Direction string `json:"direction"`
}

type Table struct {
	Version       int                         `json:"version"`
	Kind          string                      `json:"-"`
	Title         string                      `json:"title"`
	Style         TableStyle                  `json:"style"`
	Interaction   InteractionConfig           `json:"interaction"`
	Selection     []InteractionSelectionEntry `json:"selection"`
	Columns       []TableColumn               `json:"columns"`
	Cardinality   TableCardinality            `json:"cardinality"`
	AvailableRows int                         `json:"availableRows"`
	IsCapped      bool                        `json:"isCapped"`
	RowCap        int                         `json:"rowCap"`
	ChunkSize     int                         `json:"chunkSize"`
	RowHeight     int                         `json:"rowHeight"`
	ResetVersion  int                         `json:"resetVersion"`
	Sort          TableSort                   `json:"sort"`
	Blocks        map[string]TableBlock       `json:"blocks"`
	LoadingBlock  string                      `json:"loadingBlock"`
	Error         string                      `json:"error"`
}

// TabularVisual is the wire representation of a table, matrix, or pivot in
// the unified visual namespace. Table remains the specialized runtime state.
type TabularVisual struct {
	Table
	ID   string `json:"id"`
	Type string `json:"type"`
}

func NewTabularVisual(id string, table Table) TabularVisual {
	visualType := "table"
	switch table.Kind {
	case "matrix_table":
		visualType = "matrix"
	case "pivot_table":
		visualType = "pivot"
	}
	return TabularVisual{Table: table, ID: id, Type: visualType}
}

type TableCardinality struct {
	Kind  string `json:"kind"`
	Value int    `json:"value"`
}

const (
	CardinalityUnknown    = "unknown"
	CardinalityLowerBound = "lower_bound"
	CardinalityEstimated  = "estimated"
	CardinalityExact      = "exact"
)

func ExactCardinality(value int) TableCardinality {
	return TableCardinality{Kind: CardinalityExact, Value: max(0, value)}
}

func LowerBoundCardinality(value int) TableCardinality {
	return TableCardinality{Kind: CardinalityLowerBound, Value: max(0, value)}
}

func (c TableCardinality) ExactValue() (int, bool) {
	return c.Value, c.Kind == CardinalityExact
}

type TableBlock struct {
	Start        int              `json:"start"`
	RequestSeq   int              `json:"requestSeq"`
	ResetVersion int              `json:"resetVersion"`
	Sort         TableSort        `json:"sort"`
	Rows         []map[string]any `json:"rows"`
}

type TableColumn struct {
	Key         string                `json:"key" yaml:"key"`
	Label       string                `json:"label" yaml:"label"`
	Align       string                `json:"align,omitempty" yaml:"align,omitempty"`
	Role        string                `json:"role,omitempty" yaml:"role,omitempty"`
	Group       string                `json:"group,omitempty" yaml:"group,omitempty"`
	Measure     string                `json:"measure,omitempty" yaml:"measure,omitempty"`
	ColumnValue string                `json:"columnValue,omitempty" yaml:"column_value,omitempty"`
	Width       int                   `json:"width,omitempty" yaml:"width,omitempty"`
	Format      string                `json:"format,omitempty" yaml:"format,omitempty"`
	Formatting  []TableFormattingRule `json:"formatting,omitempty" yaml:"formatting,omitempty"`
}

type TableFormattingRule struct {
	Kind       string            `json:"kind" yaml:"kind"`
	Values     map[string]string `json:"values,omitempty" yaml:"values,omitempty"`
	Min        *float64          `json:"min,omitempty" yaml:"min,omitempty"`
	Max        *float64          `json:"max,omitempty" yaml:"max,omitempty"`
	Color      string            `json:"color,omitempty" yaml:"color,omitempty"`
	Background string            `json:"background,omitempty" yaml:"background,omitempty"`
	LowColor   string            `json:"lowColor,omitempty" yaml:"low_color,omitempty"`
	HighColor  string            `json:"highColor,omitempty" yaml:"high_color,omitempty"`
}

func OrdersTableColumns() []TableColumn {
	return []TableColumn{
		{Key: "order_id", Label: "Order"},
		{Key: "purchase_date", Label: "Purchased"},
		{Key: "status", Label: "Status"},
		{Key: "state", Label: "State"},
		{Key: "category", Label: "Category"},
		{Key: "revenue", Label: "Revenue", Align: "right"},
		{Key: "review_score", Label: "Review", Align: "right"},
		{Key: "delivery_days", Label: "Delivery", Align: "right"},
	}
}

func EmptyTable(request TableRequest, err error) Table {
	request = request.WithDefaults()
	message := ""
	if err != nil {
		message = err.Error()
	}
	return Table{
		Version:       2,
		Kind:          "data_table",
		Title:         "Orders",
		Style:         TableStyle{}.WithDefaults(),
		Selection:     []InteractionSelectionEntry{},
		Columns:       OrdersTableColumns(),
		Cardinality:   TableCardinality{Kind: CardinalityUnknown},
		AvailableRows: 0,
		IsCapped:      false,
		RowCap:        TableInteractiveRowCap,
		ChunkSize:     TableChunkSize,
		RowHeight:     TableStyle{}.RowHeight(),
		ResetVersion:  request.ResetVersion,
		Sort:          request.Sort,
		Blocks:        emptyTableBlocks(),
		LoadingBlock:  "",
		Error:         message,
	}
}

func emptyTableBlocks() map[string]TableBlock {
	return map[string]TableBlock{
		"a": {Start: 0, Rows: []map[string]any{}},
		"b": {Start: TableChunkSize, Rows: []map[string]any{}},
		"c": {Start: TableChunkSize * 2, Rows: []map[string]any{}},
	}
}

func EmptyPatch(filters Filters, err error) Patch {
	message := ""
	if err != nil {
		message = err.Error()
	}

	return Patch{
		Filters: filters.WithDefaults(),
		Status: Status{
			Loading:         false,
			Error:           message,
			SetupRequired:   err != nil,
			ProgressPercent: NormalizeProgressPercent(nil, false),
		},
		Visuals: map[string]visualizationir.VisualizationEnvelope{},
	}
}
