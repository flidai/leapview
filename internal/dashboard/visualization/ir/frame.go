package ir

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
)

// ValidateEnvelope validates the complete renderer boundary: immutable
// specification identity, frame shape and scalar types, and window invariants.
func ValidateEnvelope(envelope VisualizationEnvelope) error {
	if err := ValidateEnvelopeRevisions(envelope); err != nil {
		return err
	}
	base, err := specificationBase(envelope.Spec)
	if err != nil {
		return err
	}
	if envelope.VisualID == "" || envelope.RendererID == "" {
		return fmt.Errorf("visualization ID and renderer ID are required")
	}
	schemas, err := validateSpecification(envelope.Spec, base)
	if err != nil {
		return err
	}
	if err := validateSelections(envelope, schemas); err != nil {
		return err
	}
	if err := validateHighlights(envelope.Highlights); err != nil {
		return err
	}
	switch state := envelope.DataState.Value.(type) {
	case *InlineVisualizationDataState:
		if base.DataBudget.MaxRows <= 0 {
			return fmt.Errorf("visualization data budget maxRows must be positive")
		}
		if err := validateInlineState(*state, schemas, base.DataBudget); err != nil {
			return err
		}
		return validateInlineSemantics(envelope.Spec, *state)
	case *WindowedVisualizationDataState:
		if base.DataBudget.MaxRows <= 0 {
			return fmt.Errorf("visualization data budget maxRows must be positive")
		}
		return validateWindowedState(*state, base.DataBudget)
	case *SpatialWindowedVisualizationDataState:
		if base.DataBudget.MaxRows <= 0 {
			return fmt.Errorf("visualization data budget maxRows must be positive")
		}
		return validateSpatialWindowedState(*state, base.DataBudget)
	case *SpatialTiledVisualizationDataState:
		if base.DataBudget.MaxRows != 0 {
			return fmt.Errorf("spatial tiled visualization must not declare a row transport budget")
		}
		return validateSpatialTiledState(*state, schemas)
	default:
		return fmt.Errorf("unsupported visualization data state %T", state)
	}
}

func validateInlineSemantics(spec VisualizationSpec, state InlineVisualizationDataState) error {
	if point, ok := spec.Value.(*PointVisualizationSpec); ok {
		return validatePointRows(*point, state)
	}
	hierarchy, ok := spec.Value.(*HierarchyVisualizationSpec)
	if !ok {
		return nil
	}
	if hierarchy.Mark == VisualizationHierarchyMarkGraph || hierarchy.Mark == VisualizationHierarchyMarkSankey {
		return validateNetworkRows(*hierarchy, state)
	}
	return validateHierarchyRows(*hierarchy, state)
}

func validatePointRows(spec PointVisualizationSpec, state InlineVisualizationDataState) error {
	if len(spec.Identity) == 0 {
		return fmt.Errorf("point visualization requires identity fields")
	}
	dataset, ok := inlineDataset(state, spec.X.Dataset)
	if !ok {
		return fmt.Errorf("point dataset %q is missing", spec.X.Dataset)
	}
	xIndex, yIndex := columnIndex(dataset.Columns, spec.X.Field), columnIndex(dataset.Columns, spec.Y.Field)
	if xIndex < 0 || yIndex < 0 {
		return fmt.Errorf("point x or y column is missing")
	}
	identityIndexes := make([]int, len(spec.Identity))
	for index, identity := range spec.Identity {
		if identity.Dataset != dataset.ID {
			return fmt.Errorf("point identity fields must share the x/y dataset")
		}
		identityIndexes[index] = columnIndex(dataset.Columns, identity.Field)
		if identityIndexes[index] < 0 {
			return fmt.Errorf("point identity column %q is missing", identity.Field)
		}
	}
	seen := make(map[string]struct{}, len(dataset.Rows))
	for rowIndex, row := range dataset.Rows {
		if row[xIndex] == nil || row[yIndex] == nil {
			return fmt.Errorf("point row %d has a null x or y value", rowIndex)
		}
		parts := make([]string, len(identityIndexes))
		for index, column := range identityIndexes {
			if row[column] == nil {
				return fmt.Errorf("point row %d has a null identity value", rowIndex)
			}
			parts[index] = fmt.Sprintf("%T:%v", row[column], row[column])
		}
		key := strings.Join(parts, "\x1f")
		if _, exists := seen[key]; exists {
			return fmt.Errorf("point row %d has duplicate identity", rowIndex)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateHierarchyRows(spec HierarchyVisualizationSpec, state InlineVisualizationDataState) error {
	if spec.Parent == nil || spec.Parent.Dataset != spec.Node.Dataset {
		return fmt.Errorf("hierarchy node and parent fields must share a dataset")
	}
	dataset, ok := inlineDataset(state, spec.Node.Dataset)
	if !ok {
		return fmt.Errorf("hierarchy dataset %q is missing", spec.Node.Dataset)
	}
	nodeIndex, parentIndex := columnIndex(dataset.Columns, spec.Node.Field), columnIndex(dataset.Columns, spec.Parent.Field)
	if nodeIndex < 0 || parentIndex < 0 {
		return fmt.Errorf("hierarchy node or parent column is missing")
	}
	parents := make(map[string]string, len(dataset.Rows))
	for rowIndex, row := range dataset.Rows {
		node, ok := row[nodeIndex].(string)
		if !ok || strings.TrimSpace(node) == "" {
			return fmt.Errorf("hierarchy row %d has an empty node", rowIndex)
		}
		parent := ""
		if row[parentIndex] != nil {
			var parentOK bool
			parent, parentOK = row[parentIndex].(string)
			if !parentOK || strings.TrimSpace(parent) == "" {
				return fmt.Errorf("hierarchy row %d has an invalid parent", rowIndex)
			}
		}
		id := HierarchyNodeIdentity(parent, node)
		if _, exists := parents[id]; exists {
			return fmt.Errorf("duplicate hierarchy node identity %q", id)
		}
		parents[id] = parent
	}
	for id, parent := range parents {
		if parent != "" {
			if _, exists := parents[parent]; !exists {
				return fmt.Errorf("hierarchy node %q references missing parent %q", id, parent)
			}
		}
	}
	for id := range parents {
		seen := map[string]struct{}{id: {}}
		for parent := parents[id]; parent != ""; parent = parents[parent] {
			if _, exists := seen[parent]; exists {
				return fmt.Errorf("hierarchy contains a cycle at %q", parent)
			}
			seen[parent] = struct{}{}
		}
	}
	return nil
}

func validateNetworkRows(spec HierarchyVisualizationSpec, state InlineVisualizationDataState) error {
	if spec.Source == nil || spec.Target == nil || spec.Source.Dataset != spec.Target.Dataset {
		return fmt.Errorf("network source and target fields must share a dataset")
	}
	dataset, ok := inlineDataset(state, spec.Source.Dataset)
	if !ok {
		return fmt.Errorf("network dataset %q is missing", spec.Source.Dataset)
	}
	sourceIndex, targetIndex := columnIndex(dataset.Columns, spec.Source.Field), columnIndex(dataset.Columns, spec.Target.Field)
	if sourceIndex < 0 || targetIndex < 0 {
		return fmt.Errorf("network source or target column is missing")
	}
	for rowIndex, row := range dataset.Rows {
		for _, endpoint := range []struct {
			name  string
			value any
		}{{"source", row[sourceIndex]}, {"target", row[targetIndex]}} {
			value, ok := endpoint.value.(string)
			if !ok || strings.TrimSpace(value) == "" {
				return fmt.Errorf("network row %d has an invalid %s endpoint", rowIndex, endpoint.name)
			}
		}
	}
	return nil
}

func inlineDataset(state InlineVisualizationDataState, id string) (VisualizationInlineDataset, bool) {
	for _, dataset := range state.Datasets {
		if dataset.ID == id {
			return dataset, true
		}
	}
	return VisualizationInlineDataset{}, false
}

func columnIndex(columns []string, id string) int {
	for index, column := range columns {
		if column == id {
			return index
		}
	}
	return -1
}

// HierarchyNodeIdentity returns the canonical identity used by frame builders,
// validators, and renderer adapters. Parent is already a canonical identity;
// node remains the author-facing display label.
func HierarchyNodeIdentity(parent, node string) string {
	escaped := strings.ReplaceAll(node, "\x1f", "\x1f\x1f")
	if parent == "" {
		return escaped
	}
	return parent + "\x1f" + escaped
}

func validateSelections(envelope VisualizationEnvelope, schemas map[string]VisualizationDatasetSchema) error {
	for index, selection := range envelope.Selection {
		datum := selection.Datum
		schema, ok := schemas[datum.Dataset]
		if !ok {
			return fmt.Errorf("selection %d references unknown dataset %q", index, datum.Dataset)
		}
		if datum.DataRevision != envelope.DataRevision {
			return fmt.Errorf("selection %d data revision mismatch", index)
		}
		if len(datum.Identity) == 0 {
			return fmt.Errorf("selection %d requires identity values", index)
		}
		identityFields := map[string]VisualizationField{}
		for _, field := range schema.Fields {
			if field.Role == VisualizationFieldRoleIdentity {
				identityFields[field.ID] = field
			}
		}
		if len(identityFields) == 0 {
			return fmt.Errorf("selection %d dataset has no identity fields", index)
		}
		for fieldID, value := range datum.Identity {
			field, ok := identityFields[fieldID]
			if !ok {
				return fmt.Errorf("selection %d references non-identity field %q", index, fieldID)
			}
			if err := validateScalar(field, value); err != nil {
				return fmt.Errorf("selection %d identity: %w", index, err)
			}
		}
		for fieldID := range identityFields {
			if _, ok := datum.Identity[fieldID]; !ok {
				return fmt.Errorf("selection %d omits identity field %q", index, fieldID)
			}
		}
	}
	return nil
}

func validateHighlights(highlights []VisualizationHighlightState) error {
	seen := map[string]struct{}{}
	for index, highlight := range highlights {
		if highlight.SourceVisualID == "" || highlight.InteractionID == "" || highlight.Label == "" {
			return fmt.Errorf("highlight %d requires source visual, interaction, and label", index)
		}
		key := highlight.SourceVisualID + "\x00" + highlight.InteractionID
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate highlight source %q", key)
		}
		seen[key] = struct{}{}
		if len(highlight.Entries) == 0 && highlight.SpatialGeometry == nil {
			return fmt.Errorf("highlight %d requires entries or spatial geometry", index)
		}
		if highlight.SpatialGeometry != nil && (highlight.SpatialLatitudeFieldID == nil || *highlight.SpatialLatitudeFieldID == "" || highlight.SpatialLongitudeFieldID == nil || *highlight.SpatialLongitudeFieldID == "") {
			return fmt.Errorf("highlight %d spatial geometry requires latitude and longitude fields", index)
		}
		for entryIndex, entry := range highlight.Entries {
			if len(entry.Mappings) == 0 {
				return fmt.Errorf("highlight %d entry %d has no mappings", index, entryIndex)
			}
			for mappingIndex, mapping := range entry.Mappings {
				if mapping.TargetFieldID == "" || !isHighlightScalar(mapping.Value) {
					return fmt.Errorf("highlight %d entry %d mapping %d is invalid", index, entryIndex, mappingIndex)
				}
			}
		}
	}
	return nil
}

func isHighlightScalar(value any) bool {
	switch value := value.(type) {
	case nil, string, bool:
		return true
	default:
		number, ok := scalarNumber(value)
		return ok && !math.IsNaN(number) && !math.IsInf(number, 0)
	}
}

// ValidateSpec validates semantic field references and data requirements
// without requiring a runtime data state.
func ValidateSpec(spec VisualizationSpec) error {
	base, err := specificationBase(spec)
	if err != nil {
		return err
	}
	_, err = validateSpecification(spec, base)
	return err
}

func specificationBase(spec VisualizationSpec) (VisualizationSpecBase, error) {
	base, err := spec.Base()
	if err != nil {
		return VisualizationSpecBase{}, err
	}
	result := *base
	// Closed variants repeat the JSON discriminator beside the embedded base.
	// After a JSON round trip the discriminator is decoded into the variant
	// field, so restore it on the common projection returned to callers.
	if result.Kind == "" {
		switch variant := spec.Value.(type) {
		case *CartesianVisualizationSpec:
			result.Kind = variant.Kind
		case *PointVisualizationSpec:
			result.Kind = variant.Kind
		case *GeographicVisualizationSpec:
			result.Kind = variant.Kind
		case *HierarchyVisualizationSpec:
			result.Kind = variant.Kind
		case *KPIVisualizationSpec:
			result.Kind = variant.Kind
		case *MatrixVisualizationSpec:
			result.Kind = variant.Kind
		case *PivotVisualizationSpec:
			result.Kind = variant.Kind
		case *PolarVisualizationSpec:
			result.Kind = variant.Kind
		case *ProportionalVisualizationSpec:
			result.Kind = variant.Kind
		case *TableVisualizationSpec:
			result.Kind = variant.Kind
		}
	}
	return result, nil
}

// SpecificationBase returns the common, renderer-independent contract shared
// by every closed visualization discriminator.
func SpecificationBase(spec VisualizationSpec) (VisualizationSpecBase, error) {
	return specificationBase(spec)
}

func validateSpecification(spec VisualizationSpec, base VisualizationSpecBase) (map[string]VisualizationDatasetSchema, error) {
	if base.Title == "" || base.Accessibility.Title == "" || base.Accessibility.Description == "" {
		return nil, fmt.Errorf("visualization title and accessibility text are required")
	}
	schemas := make(map[string]VisualizationDatasetSchema, len(base.Datasets))
	for _, schema := range base.Datasets {
		if err := validateSchema(schema); err != nil {
			return nil, err
		}
		if _, exists := schemas[schema.ID]; exists {
			return nil, fmt.Errorf("duplicate visualization dataset %q", schema.ID)
		}
		schemas[schema.ID] = schema
	}
	if len(schemas) == 0 {
		return nil, fmt.Errorf("visualization requires at least one dataset")
	}
	if err := validateVisualCalculations(base.Calculations, schemas); err != nil {
		return nil, err
	}
	for _, ref := range specificationRefs(spec) {
		if err := validateFieldRef(ref, schemas); err != nil {
			return nil, err
		}
	}
	if err := validateConditionalFormatting(spec, base, schemas); err != nil {
		return nil, err
	}
	if err := validateMetadataBindings(base.MetadataBindings, schemas); err != nil {
		return nil, err
	}
	if err := validateLabelPolicy(spec); err != nil {
		return nil, err
	}
	if err := validateKPISpecification(spec, schemas); err != nil {
		return nil, err
	}
	if err := validateCartesianDecisionContext(spec); err != nil {
		return nil, err
	}
	if err := validatePointSpecification(spec, schemas); err != nil {
		return nil, err
	}
	if err := validateGeographicSpecification(spec); err != nil {
		return nil, err
	}
	for _, interaction := range base.Interactions {
		if interaction.ID == "" {
			return nil, fmt.Errorf("visualization interaction ID is required")
		}
		if len(interaction.Mappings) == 0 {
			return nil, fmt.Errorf("interaction %q requires mappings", interaction.ID)
		}
		for _, mapping := range interaction.Mappings {
			if mapping.TargetFieldID == "" {
				return nil, fmt.Errorf("interaction %q mapping requires target field ID", interaction.ID)
			}
			if err := validateFieldRef(mapping.Source, schemas); err != nil {
				return nil, fmt.Errorf("interaction %q: %w", interaction.ID, err)
			}
			if mapping.Label != nil {
				if err := validateFieldRef(*mapping.Label, schemas); err != nil {
					return nil, fmt.Errorf("interaction %q label: %w", interaction.ID, err)
				}
			}
			if interaction.RequiresStableIdentity && !hasIdentityField(schemas[mapping.Source.Dataset]) {
				return nil, fmt.Errorf("interaction %q requires a stable identity field", interaction.ID)
			}
		}
	}
	return schemas, nil
}

func validateLabelPolicy(spec VisualizationSpec) error {
	var policy VisualizationLabelPolicy
	switch value := spec.Value.(type) {
	case *CartesianVisualizationSpec:
		policy = value.Presentation.LabelPolicy
	case *PointVisualizationSpec:
		policy = value.Presentation.LabelPolicy
	case *ProportionalVisualizationSpec:
		policy = value.Presentation.LabelPolicy
	case *HierarchyVisualizationSpec:
		policy = value.Presentation.LabelPolicy
	case *PolarVisualizationSpec:
		policy = value.Presentation.LabelPolicy
	case *GeographicVisualizationSpec:
		policy = value.Presentation.LabelPolicy
	default:
		return nil
	}
	switch policy.Density {
	case VisualizationLabelDensityHidden, VisualizationLabelDensityAutomatic, VisualizationLabelDensityDense, VisualizationLabelDensityAlways:
	default:
		return fmt.Errorf("unsupported label density %q", policy.Density)
	}
	seen := make(map[VisualizationLabelPriority]struct{}, len(policy.Priority))
	for _, priority := range policy.Priority {
		switch priority {
		case VisualizationLabelPrioritySelected, VisualizationLabelPriorityAnomaly, VisualizationLabelPriorityThreshold:
		default:
			return fmt.Errorf("unsupported label priority %q", priority)
		}
		if _, exists := seen[priority]; exists {
			return fmt.Errorf("duplicate label priority %q", priority)
		}
		seen[priority] = struct{}{}
	}
	if policy.MaxCharacters < 4 || policy.MaxCharacters > 200 {
		return fmt.Errorf("label max characters must be between 4 and 200")
	}
	if policy.MinimumSpacing < 0 || policy.MinimumSpacing > 64 {
		return fmt.Errorf("label minimum spacing must be between 0 and 64")
	}
	if policy.Density != VisualizationLabelDensityAlways && !policy.TooltipFallback {
		return fmt.Errorf("labels that can be suppressed require tooltip fallback")
	}
	return nil
}

func validatePointSpecification(spec VisualizationSpec, schemas map[string]VisualizationDatasetSchema) error {
	point, ok := spec.Value.(*PointVisualizationSpec)
	if !ok {
		return nil
	}
	if len(point.Identity) == 0 {
		return fmt.Errorf("point visualization requires identity fields")
	}
	xField, _ := visualizationField(point.X, schemas)
	yField, _ := visualizationField(point.Y, schemas)
	if !numericVisualizationField(xField) && xField.DataType != VisualizationDataTypeTemporal && xField.DataType != VisualizationDataTypeDate {
		return fmt.Errorf("point x field must be numeric or temporal")
	}
	if !numericVisualizationField(yField) {
		return fmt.Errorf("point y field must be numeric")
	}
	for _, identity := range point.Identity {
		field, _ := visualizationField(identity, schemas)
		if field.Role != VisualizationFieldRoleIdentity {
			return fmt.Errorf("point identity field %q must have identity role", identity.Field)
		}
		if identity.Dataset != point.X.Dataset || identity.Dataset != point.Y.Dataset {
			return fmt.Errorf("point identity, x, and y fields must share a dataset")
		}
	}
	if point.Size != nil {
		field, _ := visualizationField(*point.Size, schemas)
		if !numericVisualizationField(field) {
			return fmt.Errorf("point size field must be numeric")
		}
		if point.SizeScale == nil {
			return fmt.Errorf("point size field requires size scale")
		}
	}
	if point.Size == nil && point.SizeScale != nil {
		return fmt.Errorf("point size scale requires a size field")
	}
	if scale := point.SizeScale; scale != nil {
		if scale.Minimum != nil && scale.Maximum != nil && *scale.Minimum >= *scale.Maximum {
			return fmt.Errorf("point size scale minimum must be less than maximum")
		}
		if scale.MinimumPixels <= 0 || scale.MaximumPixels <= scale.MinimumPixels {
			return fmt.Errorf("point size scale pixel range must be positive and increasing")
		}
	}
	if point.Color != nil {
		field, _ := visualizationField(*point.Color, schemas)
		if point.ColorScale == nil {
			return fmt.Errorf("point color field requires color scale")
		}
		if point.ColorScale.Kind == VisualizationPointColorScaleKindQuantitative && !numericVisualizationField(field) {
			return fmt.Errorf("quantitative point color scale requires a numeric field")
		}
		if point.ColorScale.Kind == VisualizationPointColorScaleKindCategorical && numericVisualizationField(field) {
			return fmt.Errorf("categorical point color scale requires a dimension field")
		}
	}
	if point.Color == nil && point.ColorScale != nil {
		return fmt.Errorf("point color scale requires a color field")
	}
	if scale := point.ColorScale; scale != nil && scale.Minimum != nil && scale.Maximum != nil && *scale.Minimum >= *scale.Maximum {
		return fmt.Errorf("point color scale minimum must be less than maximum")
	}
	if point.Presentation.Opacity <= 0 || point.Presentation.Opacity > 1 {
		return fmt.Errorf("point opacity must be greater than zero and at most one")
	}
	if point.Presentation.LargeThreshold <= 0 {
		return fmt.Errorf("point large threshold must be positive")
	}
	brushes := map[VisualizationPointBrushGesture]struct{}{}
	for _, brush := range point.Presentation.Brush {
		if brush != VisualizationPointBrushGestureRectangle && brush != VisualizationPointBrushGestureLasso {
			return fmt.Errorf("point visualization has unsupported brush gesture %q", brush)
		}
		if _, exists := brushes[brush]; exists {
			return fmt.Errorf("point visualization has duplicate brush gesture %q", brush)
		}
		brushes[brush] = struct{}{}
	}
	if len(point.Presentation.Brush) > 0 && len(point.Interactions) == 0 {
		return fmt.Errorf("point brush requires an interaction")
	}
	decisionContext := VisualizationSpec{Value: &CartesianVisualizationSpec{
		VisualizationSpecBase: point.VisualizationSpecBase, Kind: "cartesian", Mark: VisualizationCartesianMarkLine,
		X: point.X, Y: []VisualizationFieldRef{point.Y}, Axes: point.Axes, ReferenceLines: point.ReferenceLines,
		ReferenceBands: point.ReferenceBands, EventAnnotations: point.EventAnnotations,
	}}
	return validateCartesianDecisionContext(decisionContext)
}

func validateKPISpecification(spec VisualizationSpec, schemas map[string]VisualizationDatasetSchema) error {
	kpi, ok := spec.Value.(*KPIVisualizationSpec)
	if !ok {
		return nil
	}
	valueField, _ := visualizationField(kpi.Value, schemas)
	if !numericVisualizationField(valueField) {
		return fmt.Errorf("KPI value field must be numeric")
	}
	for name, binding := range map[string]*VisualizationKPIValueBinding{
		"comparison": kpi.Comparison,
		"goal":       kpi.Goal,
	} {
		if binding == nil {
			continue
		}
		field, _ := visualizationField(binding.Field, schemas)
		if !numericVisualizationField(field) {
			return fmt.Errorf("KPI %s field must be numeric", name)
		}
		if !validVisualizationReferenceReducer(binding.Reducer) {
			return fmt.Errorf("KPI %s uses unsupported reducer %q", name, binding.Reducer)
		}
		if strings.TrimSpace(binding.Label) == "" {
			return fmt.Errorf("KPI %s requires a label", name)
		}
	}
	if kpi.Comparison != nil && kpi.Presentation.FavorableDirection == "" {
		return fmt.Errorf("KPI comparison requires favorable direction")
	}
	if (kpi.Presentation.Mode == VisualizationKPIModeBullet || kpi.Presentation.Mode == VisualizationKPIModeProgress) && kpi.Goal == nil {
		return fmt.Errorf("KPI mode %q requires a goal", kpi.Presentation.Mode)
	}
	if kpi.Trend != nil {
		field, _ := visualizationField(kpi.Trend.Value, schemas)
		if !numericVisualizationField(field) {
			return fmt.Errorf("KPI trend value field must be numeric")
		}
	}
	var previousMaximum *float64
	for index, valueRange := range kpi.Presentation.Ranges {
		if strings.TrimSpace(valueRange.Label) == "" {
			return fmt.Errorf("KPI qualitative range %d requires a label", index)
		}
		if valueRange.Minimum != nil && valueRange.Maximum != nil && *valueRange.Minimum >= *valueRange.Maximum {
			return fmt.Errorf("KPI qualitative range %d minimum must be less than maximum", index)
		}
		if index > 0 && valueRange.Minimum == nil {
			return fmt.Errorf("KPI qualitative range %d requires a minimum", index)
		}
		if index < len(kpi.Presentation.Ranges)-1 && valueRange.Maximum == nil {
			return fmt.Errorf("KPI qualitative range %d requires a maximum", index)
		}
		if previousMaximum != nil && valueRange.Minimum != nil && *valueRange.Minimum < *previousMaximum {
			return fmt.Errorf("KPI qualitative ranges overlap at index %d", index)
		}
		previousMaximum = valueRange.Maximum
	}
	return nil
}

func validateMetadataBindings(bindings *VisualizationMetadataBindings, schemas map[string]VisualizationDatasetSchema) error {
	if bindings == nil {
		return nil
	}
	for _, named := range []struct {
		name    string
		binding *VisualizationTextBinding
	}{
		{"title", bindings.Title},
		{"subtitle", bindings.Subtitle},
		{"description", bindings.Description},
		{"summary", bindings.Summary},
	} {
		if named.binding == nil {
			continue
		}
		if err := validateFieldRef(named.binding.Field, schemas); err != nil {
			return fmt.Errorf("visualization %s binding: %w", named.name, err)
		}
		if strings.TrimSpace(named.binding.Fallback) == "" {
			return fmt.Errorf("visualization %s binding requires a non-empty fallback", named.name)
		}
		if !validVisualizationReferenceReducer(named.binding.Reducer) {
			return fmt.Errorf("visualization %s binding uses unsupported reducer %q", named.name, named.binding.Reducer)
		}
		field, _ := visualizationField(named.binding.Field, schemas)
		if (named.binding.Reducer == VisualizationReferenceReducerMean || named.binding.Reducer == VisualizationReferenceReducerMedian) && !numericVisualizationField(field) {
			return fmt.Errorf("visualization %s binding reducer %q requires a numeric field", named.name, named.binding.Reducer)
		}
	}
	return nil
}

func validateConditionalFormatting(spec VisualizationSpec, base VisualizationSpecBase, schemas map[string]VisualizationDatasetSchema) error {
	if base.ConditionalFormatting == nil {
		return nil
	}
	if !specSupportsConditionalFormatting(spec) {
		return fmt.Errorf("visualization kind %q does not support conditional formatting", base.Kind)
	}
	ids := make(map[string]struct{}, len(*base.ConditionalFormatting))
	targets := make(map[string]struct{}, len(*base.ConditionalFormatting))
	for _, format := range *base.ConditionalFormatting {
		if strings.TrimSpace(format.ID) == "" {
			return fmt.Errorf("conditional formatting ID is required")
		}
		if _, exists := ids[format.ID]; exists {
			return fmt.Errorf("duplicate conditional formatting ID %q", format.ID)
		}
		ids[format.ID] = struct{}{}
		targetKey := string(format.Target) + "\x00" + format.Field.Dataset + "\x00" + format.Field.Field
		if _, exists := targets[targetKey]; exists {
			return fmt.Errorf("ambiguous conditional formatting target %q for field %q", format.Target, format.Field.Field)
		}
		targets[targetKey] = struct{}{}
		if err := validateConditionalFormattingTarget(base.Kind, format); err != nil {
			return fmt.Errorf("conditional formatting %q: %w", format.ID, err)
		}
		if err := validateFieldRef(format.Field, schemas); err != nil {
			return fmt.Errorf("conditional formatting %q field: %w", format.ID, err)
		}
		field, _ := visualizationField(format.Field, schemas)
		switch rule := format.Rule.Value.(type) {
		case *GradientVisualizationConditionalRule:
			if rule == nil {
				return fmt.Errorf("conditional formatting %q gradient rule is nil", format.ID)
			}
			if !numericVisualizationField(field) {
				return fmt.Errorf("conditional formatting %q gradient requires a numeric field", format.ID)
			}
			if !finite(rule.Minimum) || !finite(rule.Maximum) || rule.Minimum >= rule.Maximum {
				return fmt.Errorf("conditional formatting %q minimum must be less than maximum", format.ID)
			}
			if rule.Low.Color == nil || rule.High.Color == nil {
				return fmt.Errorf("conditional formatting %q gradient requires low and high colors", format.ID)
			}
			for _, named := range []struct {
				position string
				style    VisualizationConditionalStyle
			}{{"low", rule.Low}, {"high", rule.High}, {"null", rule.NullStyle}} {
				if err := validateVisualizationConditionalStyle(named.style, false); err != nil {
					return fmt.Errorf("conditional formatting %q %s style: %w", format.ID, named.position, err)
				}
			}
		case *RulesVisualizationConditionalRule:
			if rule == nil || len(rule.Rules) == 0 {
				return fmt.Errorf("conditional formatting %q requires rules", format.ID)
			}
			if !numericVisualizationField(field) {
				return fmt.Errorf("conditional formatting %q rules require a numeric field", format.ID)
			}
			for index, threshold := range rule.Rules {
				if !finite(threshold.Value) || !validVisualizationComparisonOperator(threshold.Operator) {
					return fmt.Errorf("conditional formatting %q rule %d is invalid", format.ID, index)
				}
				if err := validateVisualizationConditionalStyle(threshold.Style, true); err != nil {
					return fmt.Errorf("conditional formatting %q rule %d style: %w", format.ID, index, err)
				}
			}
			for _, named := range []struct {
				position     string
				style        VisualizationConditionalStyle
				redundantCue bool
			}{{"null", rule.NullStyle, false}, {"default", rule.DefaultStyle, true}} {
				if err := validateVisualizationConditionalStyle(named.style, named.redundantCue); err != nil {
					return fmt.Errorf("conditional formatting %q %s style: %w", format.ID, named.position, err)
				}
			}
		case *FieldVisualizationConditionalRule:
			if rule == nil {
				return fmt.Errorf("conditional formatting %q field rule is nil", format.ID)
			}
			if err := validateFieldRef(rule.Source, schemas); err != nil {
				return fmt.Errorf("conditional formatting %q source: %w", format.ID, err)
			}
			if len(rule.Values) == 0 {
				return fmt.Errorf("conditional formatting %q requires values", format.ID)
			}
			values := make([]string, 0, len(rule.Values))
			for value := range rule.Values {
				values = append(values, value)
			}
			sort.Strings(values)
			for _, value := range values {
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("conditional formatting %q has an empty field value", format.ID)
				}
				if err := validateVisualizationConditionalStyle(rule.Values[value], true); err != nil {
					return fmt.Errorf("conditional formatting %q value %q style: %w", format.ID, value, err)
				}
			}
			for _, named := range []struct {
				position     string
				style        VisualizationConditionalStyle
				redundantCue bool
			}{{"null", rule.NullStyle, false}, {"default", rule.DefaultStyle, true}} {
				if err := validateVisualizationConditionalStyle(named.style, named.redundantCue); err != nil {
					return fmt.Errorf("conditional formatting %q %s style: %w", format.ID, named.position, err)
				}
			}
		case nil:
			return fmt.Errorf("conditional formatting %q rule is required", format.ID)
		default:
			return fmt.Errorf("conditional formatting %q has unsupported rule %T", format.ID, rule)
		}
	}
	return nil
}

func specSupportsConditionalFormatting(spec VisualizationSpec) bool {
	switch value := spec.Value.(type) {
	case *PointVisualizationSpec:
		return true
	case *CartesianVisualizationSpec:
		switch value.Mark {
		case VisualizationCartesianMarkLine, VisualizationCartesianMarkArea, VisualizationCartesianMarkBar,
			VisualizationCartesianMarkColumn, VisualizationCartesianMarkCombo,
			VisualizationCartesianMarkWaterfall, VisualizationCartesianMarkHeatmap:
			return true
		default:
			return false
		}
	case *KPIVisualizationSpec, *TableVisualizationSpec, *MatrixVisualizationSpec, *PivotVisualizationSpec:
		return true
	default:
		return false
	}
}

func validateConditionalFormattingTarget(kind string, format VisualizationConditionalFormat) error {
	switch format.Target {
	case VisualizationConditionalTargetMarkFill, VisualizationConditionalTargetMarkStroke, VisualizationConditionalTargetSeriesColor:
		if kind == "kpi" || kind == "table" || kind == "matrix" || kind == "pivot" {
			return fmt.Errorf("target %q is incompatible with %s visualizations", format.Target, kind)
		}
	case VisualizationConditionalTargetCellForeground, VisualizationConditionalTargetCellBackground:
		if kind != "table" && kind != "matrix" && kind != "pivot" {
			return fmt.Errorf("target %q is only valid for tabular visualizations", format.Target)
		}
	case VisualizationConditionalTargetKpiValue, VisualizationConditionalTargetVisualBackground:
		if kind != "kpi" {
			return fmt.Errorf("target %q is only valid for KPI visualizations", format.Target)
		}
	case VisualizationConditionalTargetLabelForeground, VisualizationConditionalTargetIcon:
	default:
		return fmt.Errorf("unsupported target %q", format.Target)
	}
	return nil
}

func visualizationField(ref VisualizationFieldRef, schemas map[string]VisualizationDatasetSchema) (VisualizationField, bool) {
	for _, field := range schemas[ref.Dataset].Fields {
		if field.ID == ref.Field {
			return field, true
		}
	}
	return VisualizationField{}, false
}

func numericVisualizationField(field VisualizationField) bool {
	return field.DataType == VisualizationDataTypeInteger || field.DataType == VisualizationDataTypeDecimal
}

func validateVisualizationConditionalStyle(style VisualizationConditionalStyle, redundantCue bool) error {
	if style.Color == nil && style.Icon == nil {
		return fmt.Errorf("style requires color or icon")
	}
	if style.Color != nil && !validVisualizationColorIntent(*style.Color) {
		return fmt.Errorf("unsupported color intent %q", *style.Color)
	}
	if style.Icon != nil && !validVisualizationIconIntent(*style.Icon) {
		return fmt.Errorf("unsupported icon intent %q", *style.Icon)
	}
	if redundantCue && style.Color != nil && style.Icon == nil {
		return fmt.Errorf("data-driven color requires a redundant icon cue")
	}
	return nil
}

func validVisualizationIconIntent(intent VisualizationIconIntent) bool {
	switch intent {
	case VisualizationIconIntentCircle, VisualizationIconIntentSquare, VisualizationIconIntentDiamond,
		VisualizationIconIntentTriangleUp, VisualizationIconIntentTriangleDown,
		VisualizationIconIntentArrowUp, VisualizationIconIntentArrowDown, VisualizationIconIntentWarning:
		return true
	default:
		return false
	}
}

func validVisualizationComparisonOperator(operator VisualizationComparisonOperator) bool {
	switch operator {
	case VisualizationComparisonOperatorLessThan, VisualizationComparisonOperatorLessOrEqual,
		VisualizationComparisonOperatorGreaterThan, VisualizationComparisonOperatorGreaterOrEqual,
		VisualizationComparisonOperatorEqual, VisualizationComparisonOperatorNotEqual:
		return true
	default:
		return false
	}
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func specificationRefs(spec VisualizationSpec) []VisualizationFieldRef {
	visitor := &specificationReferenceVisitor{refs: make([]VisualizationFieldRef, 0, 8)}
	if err := spec.Visit(visitor); err != nil {
		return nil
	}
	return visitor.refs
}

type specificationReferenceVisitor struct {
	refs []VisualizationFieldRef
}

func (visitor *specificationReferenceVisitor) add(ref *VisualizationFieldRef) {
	if ref != nil {
		visitor.refs = append(visitor.refs, *ref)
	}
}

func (visitor *specificationReferenceVisitor) VisitCartesianVisualizationSpec(value *CartesianVisualizationSpec) error {
	visitor.refs = append(visitor.refs, value.X)
	visitor.refs = append(visitor.refs, value.Y...)
	visitor.add(value.Series)
	if value.Tooltip != nil {
		visitor.refs = append(visitor.refs, *value.Tooltip...)
	}
	if value.ReferenceLines != nil {
		for _, line := range *value.ReferenceLines {
			visitor.addReferenceValue(line.Value)
		}
	}
	if value.ReferenceBands != nil {
		for _, band := range *value.ReferenceBands {
			visitor.addReferenceValue(band.From)
			visitor.addReferenceValue(band.To)
		}
	}
	if value.EventAnnotations != nil {
		for _, annotation := range *value.EventAnnotations {
			visitor.addReferenceValue(annotation.Value)
		}
	}
	return nil
}

func (visitor *specificationReferenceVisitor) VisitPointVisualizationSpec(value *PointVisualizationSpec) error {
	visitor.refs = append(visitor.refs, value.Identity...)
	visitor.refs = append(visitor.refs, value.X, value.Y)
	visitor.add(value.Size)
	visitor.add(value.Color)
	visitor.add(value.Series)
	visitor.add(value.Label)
	if value.Tooltip != nil {
		visitor.refs = append(visitor.refs, *value.Tooltip...)
	}
	if value.ReferenceLines != nil {
		for _, line := range *value.ReferenceLines {
			visitor.addReferenceValue(line.Value)
		}
	}
	if value.ReferenceBands != nil {
		for _, band := range *value.ReferenceBands {
			visitor.addReferenceValue(band.From)
			visitor.addReferenceValue(band.To)
		}
	}
	if value.EventAnnotations != nil {
		for _, annotation := range *value.EventAnnotations {
			visitor.addReferenceValue(annotation.Value)
		}
	}
	return nil
}

func (visitor *specificationReferenceVisitor) addReferenceValue(value VisualizationReferenceValue) {
	if field, ok := value.Value.(*FieldVisualizationReferenceValue); ok && field != nil {
		visitor.refs = append(visitor.refs, field.Field)
	}
}

func (visitor *specificationReferenceVisitor) VisitProportionalVisualizationSpec(value *ProportionalVisualizationSpec) error {
	visitor.refs = append(visitor.refs, value.Category, value.Value)
	visitor.add(value.Series)
	return nil
}

func (visitor *specificationReferenceVisitor) VisitHierarchyVisualizationSpec(value *HierarchyVisualizationSpec) error {
	visitor.refs = append(visitor.refs, value.Node)
	visitor.add(value.Parent)
	visitor.add(value.Source)
	visitor.add(value.Target)
	visitor.add(value.Value)
	return nil
}

func (visitor *specificationReferenceVisitor) VisitPolarVisualizationSpec(value *PolarVisualizationSpec) error {
	visitor.add(value.Category)
	visitor.refs = append(visitor.refs, value.Value)
	visitor.add(value.Series)
	return nil
}

func (visitor *specificationReferenceVisitor) VisitTableVisualizationSpec(value *TableVisualizationSpec) error {
	for _, column := range value.Columns {
		visitor.refs = append(visitor.refs, column.Field)
	}
	if value.DefaultSort != nil {
		for _, sort := range *value.DefaultSort {
			visitor.refs = append(visitor.refs, sort.Field)
		}
	}
	return nil
}

func (visitor *specificationReferenceVisitor) VisitMatrixVisualizationSpec(value *MatrixVisualizationSpec) error {
	visitor.refs = append(visitor.refs, value.Rows...)
	visitor.refs = append(visitor.refs, value.Columns...)
	visitor.refs = append(visitor.refs, value.Measures...)
	return nil
}

func (visitor *specificationReferenceVisitor) VisitPivotVisualizationSpec(value *PivotVisualizationSpec) error {
	visitor.refs = append(visitor.refs, value.Rows...)
	visitor.refs = append(visitor.refs, value.Columns...)
	visitor.refs = append(visitor.refs, value.Measures...)
	return nil
}

func (visitor *specificationReferenceVisitor) VisitKPIVisualizationSpec(value *KPIVisualizationSpec) error {
	visitor.refs = append(visitor.refs, value.Value)
	if value.Comparison != nil {
		visitor.refs = append(visitor.refs, value.Comparison.Field)
	}
	if value.Goal != nil {
		visitor.refs = append(visitor.refs, value.Goal.Field)
	}
	if value.Trend != nil {
		visitor.refs = append(visitor.refs, value.Trend.Category, value.Trend.Value)
	}
	return nil
}

func (visitor *specificationReferenceVisitor) VisitGeographicVisualizationSpec(value *GeographicVisualizationSpec) error {
	for _, layer := range value.Layers {
		base, err := layer.Base()
		if err == nil {
			visitor.add(base.Label)
			visitor.refs = append(visitor.refs, base.Tooltip...)
		}
		switch layer := layer.Value.(type) {
		case *VisualizationPointLayer:
			visitor.refs = append(visitor.refs, layer.Latitude, layer.Longitude)
			visitor.add(layer.Value)
			visitor.add(layer.Category)
		case *VisualizationChoroplethLayer:
			visitor.refs = append(visitor.refs, layer.Join)
			visitor.add(layer.Value)
			visitor.add(layer.Category)
		case *VisualizationHeatLayer:
			visitor.refs = append(visitor.refs, layer.Latitude, layer.Longitude)
			visitor.add(layer.Value)
		case *VisualizationDensityLayer:
			visitor.refs = append(visitor.refs, layer.Latitude, layer.Longitude)
			visitor.add(layer.Value)
		case *VisualizationPathLayer:
			visitor.refs = append(visitor.refs, layer.Latitude, layer.Longitude, layer.Path, layer.Order)
			visitor.add(layer.Value)
			visitor.add(layer.Category)
		}
	}
	return nil
}

func validateCartesianDecisionContext(spec VisualizationSpec) error {
	value, ok := spec.Value.(*CartesianVisualizationSpec)
	if !ok {
		return nil
	}
	if err := validateCartesianSeriesPresentation(*value); err != nil {
		return err
	}
	axes := map[VisualizationCartesianAxis]struct{}{}
	if value.Axes != nil {
		for _, axis := range *value.Axes {
			if axis.ID != VisualizationCartesianAxisX && axis.ID != VisualizationCartesianAxisPrimaryY && axis.ID != VisualizationCartesianAxisSecondaryY {
				return fmt.Errorf("unsupported cartesian axis %q", axis.ID)
			}
			if _, exists := axes[axis.ID]; exists {
				return fmt.Errorf("duplicate cartesian axis %q", axis.ID)
			}
			axes[axis.ID] = struct{}{}
			if axis.ID == VisualizationCartesianAxisSecondaryY && value.Mark != VisualizationCartesianMarkCombo {
				return fmt.Errorf("secondary_y axis requires combo mark")
			}
			if axis.Minimum != nil && axis.Maximum != nil && *axis.Minimum >= *axis.Maximum {
				return fmt.Errorf("axis %q minimum must be less than maximum", axis.ID)
			}
			if axis.Scale == VisualizationAxisScaleLog {
				if axis.Zero == VisualizationAxisZeroPolicyInclude {
					return fmt.Errorf("axis %q log scale cannot include zero", axis.ID)
				}
				if axis.Minimum != nil && *axis.Minimum <= 0 || axis.Maximum != nil && *axis.Maximum <= 0 {
					return fmt.Errorf("axis %q log scale requires positive bounds", axis.ID)
				}
			}
		}
	}
	ids := map[string]struct{}{}
	addID := func(id string) error {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("decision context ID is required")
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("duplicate decision context ID %q", id)
		}
		ids[id] = struct{}{}
		return nil
	}
	validateAxis := func(axis VisualizationCartesianAxis) error {
		if axis != VisualizationCartesianAxisX && axis != VisualizationCartesianAxisPrimaryY && axis != VisualizationCartesianAxisSecondaryY {
			return fmt.Errorf("unsupported decision context axis %q", axis)
		}
		if axis == VisualizationCartesianAxisSecondaryY && value.Mark != VisualizationCartesianMarkCombo {
			return fmt.Errorf("secondary_y decision context requires combo mark")
		}
		return nil
	}
	if value.ReferenceLines != nil {
		if !cartesianMarkSupportsReferences(value.Mark) {
			return fmt.Errorf("cartesian mark %q does not support reference lines", value.Mark)
		}
		for _, line := range *value.ReferenceLines {
			if err := addID(line.ID); err != nil {
				return err
			}
			if err := validateAxis(line.Axis); err != nil {
				return err
			}
			if err := validateVisualizationReferenceValue(line.Value); err != nil {
				return fmt.Errorf("reference line %q: %w", line.ID, err)
			}
		}
	}
	if value.ReferenceBands != nil {
		if !cartesianMarkSupportsReferences(value.Mark) {
			return fmt.Errorf("cartesian mark %q does not support reference bands", value.Mark)
		}
		for _, band := range *value.ReferenceBands {
			if err := addID(band.ID); err != nil {
				return err
			}
			if err := validateAxis(band.Axis); err != nil {
				return err
			}
			if err := validateVisualizationReferenceValue(band.From); err != nil {
				return fmt.Errorf("reference band %q from: %w", band.ID, err)
			}
			if err := validateVisualizationReferenceValue(band.To); err != nil {
				return fmt.Errorf("reference band %q to: %w", band.ID, err)
			}
			from, fromOK := band.From.Value.(*NumberVisualizationReferenceValue)
			to, toOK := band.To.Value.(*NumberVisualizationReferenceValue)
			if fromOK && toOK && from.Value >= to.Value {
				return fmt.Errorf("reference band %q from must be less than to", band.ID)
			}
		}
	}
	if value.EventAnnotations != nil {
		for _, annotation := range *value.EventAnnotations {
			if err := addID(annotation.ID); err != nil {
				return err
			}
			if annotation.Axis != VisualizationCartesianAxisX {
				return fmt.Errorf("event annotation %q must use x axis", annotation.ID)
			}
			if strings.TrimSpace(annotation.Label) == "" {
				return fmt.Errorf("event annotation %q requires a label", annotation.ID)
			}
			if err := validateVisualizationReferenceValue(annotation.Value); err != nil {
				return fmt.Errorf("event annotation %q: %w", annotation.ID, err)
			}
		}
	}
	return nil
}

func validateCartesianSeriesPresentation(spec CartesianVisualizationSpec) error {
	stacking := VisualizationStackingModeNone
	if spec.Presentation.Stacked {
		stacking = VisualizationStackingModeNormal
	}
	if spec.Presentation.Stacking != nil {
		stacking = *spec.Presentation.Stacking
	}
	switch stacking {
	case VisualizationStackingModeNone:
	case VisualizationStackingModeNormal, VisualizationStackingModePercent:
		switch spec.Mark {
		case VisualizationCartesianMarkLine, VisualizationCartesianMarkArea, VisualizationCartesianMarkBar,
			VisualizationCartesianMarkColumn, VisualizationCartesianMarkCombo:
		default:
			return fmt.Errorf("cartesian mark %q does not support stacking", spec.Mark)
		}
	default:
		return fmt.Errorf("unsupported stacking mode %q", stacking)
	}
	if stacking == VisualizationStackingModePercent && spec.Series == nil && len(spec.Y) < 2 {
		return fmt.Errorf("percent stacking requires multiple series")
	}
	if stacking == VisualizationStackingModePercent && spec.Presentation.ComboSeries != nil {
		for _, series := range *spec.Presentation.ComboSeries {
			if series.Axis == VisualizationAxisSecondary {
				return fmt.Errorf("percent stacking cannot use dual axes")
			}
		}
	}
	if spec.Presentation.SeriesIntent == nil {
		return nil
	}
	if spec.Series == nil && len(spec.Y) < 2 {
		return fmt.Errorf("series intent requires multiple series")
	}
	values := map[string]struct{}{}
	orders := map[int32]struct{}{}
	for _, intent := range *spec.Presentation.SeriesIntent {
		if strings.TrimSpace(intent.Value) == "" {
			return fmt.Errorf("series intent value is required")
		}
		if _, exists := values[intent.Value]; exists {
			return fmt.Errorf("duplicate series intent %q", intent.Value)
		}
		values[intent.Value] = struct{}{}
		if intent.Order != nil {
			if *intent.Order < 0 {
				return fmt.Errorf("series intent %q has negative order", intent.Value)
			}
			if _, exists := orders[*intent.Order]; exists {
				return fmt.Errorf("duplicate series order %d", *intent.Order)
			}
			orders[*intent.Order] = struct{}{}
		}
		if intent.Color != nil && !validVisualizationColorIntent(*intent.Color) {
			return fmt.Errorf("series intent %q has unsupported color %q", intent.Value, *intent.Color)
		}
	}
	return nil
}

func validVisualizationColorIntent(intent VisualizationColorIntent) bool {
	switch intent {
	case VisualizationColorIntentAccent, VisualizationColorIntentNeutral, VisualizationColorIntentInk,
		VisualizationColorIntentSuccess, VisualizationColorIntentWarning, VisualizationColorIntentDanger,
		VisualizationColorIntentData1, VisualizationColorIntentData2, VisualizationColorIntentData3,
		VisualizationColorIntentData4, VisualizationColorIntentData5, VisualizationColorIntentData6,
		VisualizationColorIntentData7, VisualizationColorIntentData8:
		return true
	default:
		return false
	}
}

func cartesianMarkSupportsReferences(mark VisualizationCartesianMark) bool {
	switch mark {
	case VisualizationCartesianMarkLine, VisualizationCartesianMarkArea, VisualizationCartesianMarkBar,
		VisualizationCartesianMarkColumn, VisualizationCartesianMarkCombo,
		VisualizationCartesianMarkWaterfall:
		return true
	default:
		return false
	}
}

func validateVisualizationReferenceValue(value VisualizationReferenceValue) error {
	switch typed := value.Value.(type) {
	case *NumberVisualizationReferenceValue:
		if typed == nil || math.IsNaN(typed.Value) || math.IsInf(typed.Value, 0) {
			return fmt.Errorf("number value must be finite")
		}
	case *TextVisualizationReferenceValue:
		if typed == nil || strings.TrimSpace(typed.Value) == "" {
			return fmt.Errorf("text value is required")
		}
	case *FieldVisualizationReferenceValue:
		if typed == nil {
			return fmt.Errorf("field value is required")
		}
		if !validVisualizationReferenceReducer(typed.Reducer) {
			return fmt.Errorf("field reference has unsupported reducer %q", typed.Reducer)
		}
	default:
		return fmt.Errorf("reference value variant is required")
	}
	return nil
}

func validVisualizationReferenceReducer(reducer VisualizationReferenceReducer) bool {
	switch reducer {
	case VisualizationReferenceReducerFirst, VisualizationReferenceReducerLast, VisualizationReferenceReducerMinimum,
		VisualizationReferenceReducerMaximum, VisualizationReferenceReducerMean, VisualizationReferenceReducerMedian:
		return true
	default:
		return false
	}
}

func validateGeographicSpecification(spec VisualizationSpec) error {
	value, ok := spec.Value.(*GeographicVisualizationSpec)
	if !ok {
		return nil
	}
	if len(value.Layers) == 0 {
		return fmt.Errorf("geographic visualization requires at least one layer")
	}
	seen := map[string]struct{}{}
	for _, layer := range value.Layers {
		base, err := layer.Base()
		if err != nil {
			return err
		}
		if base.ID == "" {
			return fmt.Errorf("geographic layer ID is required")
		}
		if _, exists := seen[base.ID]; exists {
			return fmt.Errorf("duplicate geographic layer %q", base.ID)
		}
		seen[base.ID] = struct{}{}
		if base.Visibility.MinimumZoom < 0 || base.Visibility.MaximumZoom <= base.Visibility.MinimumZoom {
			return fmt.Errorf("geographic layer %q has invalid visibility", base.ID)
		}
		switch typed := layer.Value.(type) {
		case *VisualizationChoroplethLayer:
			if err := validateGeometryAsset(typed.Geometry); err != nil {
				return fmt.Errorf("choropleth layer %q: %w", base.ID, err)
			}
		case *VisualizationReferenceLayer:
			if err := validateGeometryAsset(typed.Geometry); err != nil {
				return fmt.Errorf("reference layer %q: %w", base.ID, err)
			}
		case *VisualizationPointLayer:
			if typed.Size.MinimumRadius < 0 || typed.Size.MaximumRadius < typed.Size.MinimumRadius {
				return fmt.Errorf("point layer %q has invalid size scale", base.ID)
			}
			if typed.Cluster.Radius <= 0 || typed.Cluster.MinimumPoints < 2 {
				return fmt.Errorf("point layer %q has invalid cluster configuration", base.ID)
			}
		case *VisualizationHeatLayer, *VisualizationDensityLayer, *VisualizationPathLayer:
		default:
			kind, _ := layer.Kind()
			return fmt.Errorf("unsupported geographic layer kind %q", kind)
		}
	}
	if value.Presentation.Basemap != nil {
		asset := value.Presentation.Basemap
		if asset.ID == "" || asset.StyleURL == "" || asset.ArchiveURL == "" || len(asset.StyleDigest) != 71 || len(asset.ArchiveDigest) != 71 || asset.Attribution == "" {
			return fmt.Errorf("geographic basemap has incomplete provenance")
		}
	}
	return nil
}

func validateGeometryAsset(geometry VisualizationGeometryAsset) error {
	if geometry.ID == "" || geometry.Source == "" || geometry.License == "" || geometry.Attribution == "" || geometry.IdentifierSystem == "" || geometry.URL == "" || len(geometry.Digest) != 71 || geometry.Digest[:7] != "sha256:" {
		return fmt.Errorf("incomplete geometry provenance")
	}
	return nil
}

func validateSchema(schema VisualizationDatasetSchema) error {
	if schema.ID == "" || len(schema.Fields) == 0 {
		return fmt.Errorf("visualization dataset ID and fields are required")
	}
	seen := make(map[string]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.ID == "" || field.Label == "" {
			return fmt.Errorf("visualization dataset %q has a field without ID or label", schema.ID)
		}
		if _, exists := seen[field.ID]; exists {
			return fmt.Errorf("visualization dataset %q has duplicate field %q", schema.ID, field.ID)
		}
		seen[field.ID] = struct{}{}
	}
	return nil
}

func validateFieldRef(ref VisualizationFieldRef, schemas map[string]VisualizationDatasetSchema) error {
	schema, ok := schemas[ref.Dataset]
	if !ok {
		return fmt.Errorf("unknown visualization dataset %q", ref.Dataset)
	}
	for _, field := range schema.Fields {
		if field.ID == ref.Field {
			return nil
		}
	}
	return fmt.Errorf("unknown visualization field %q in dataset %q", ref.Field, ref.Dataset)
}

func hasIdentityField(schema VisualizationDatasetSchema) bool {
	for _, field := range schema.Fields {
		if field.Role == VisualizationFieldRoleIdentity {
			return true
		}
	}
	return false
}

func validateInlineState(state InlineVisualizationDataState, schemas map[string]VisualizationDatasetSchema, budget VisualizationDataBudget) error {
	seen := make(map[string]struct{}, len(state.Datasets))
	for _, dataset := range state.Datasets {
		schema, ok := schemas[dataset.ID]
		if !ok {
			return fmt.Errorf("inline data targets unknown dataset %q", dataset.ID)
		}
		if _, exists := seen[dataset.ID]; exists {
			return fmt.Errorf("duplicate inline dataset %q", dataset.ID)
		}
		seen[dataset.ID] = struct{}{}
		if int64(len(dataset.Rows)) > budget.MaxRows {
			return fmt.Errorf("dataset %q exceeds row budget %d", dataset.ID, budget.MaxRows)
		}
		if budget.RequiredCompleteness == VisualizationCompletenessComplete && dataset.Completeness != VisualizationCompletenessComplete && dataset.Completeness != VisualizationCompletenessEmpty {
			return fmt.Errorf("dataset %q does not satisfy complete data requirement", dataset.ID)
		}
		if err := validateRows(schema, dataset.Columns, dataset.Rows); err != nil {
			return fmt.Errorf("dataset %q: %w", dataset.ID, err)
		}
	}
	for datasetID := range schemas {
		if _, ok := seen[datasetID]; !ok {
			return fmt.Errorf("inline data is missing dataset %q", datasetID)
		}
	}
	return nil
}

func validateWindowedState(state WindowedVisualizationDataState, budget VisualizationDataBudget) error {
	if err := validateSchema(state.Schema); err != nil {
		return err
	}
	if state.AvailableRows < 0 || state.RowCap <= 0 || state.ChunkSize <= 0 || state.ResetVersion < 0 {
		return fmt.Errorf("invalid window bounds")
	}
	if state.RowCap > budget.MaxRows {
		return fmt.Errorf("window row cap %d exceeds budget %d", state.RowCap, budget.MaxRows)
	}
	switch state.Cardinality.Kind {
	case VisualizationCardinalityKindUnknown:
		if state.Cardinality.Count != nil {
			return fmt.Errorf("unknown window cardinality must omit count")
		}
	case VisualizationCardinalityKindExact:
		if state.Cardinality.Count == nil || *state.Cardinality.Count < state.AvailableRows {
			return fmt.Errorf("exact window cardinality is missing or smaller than available rows")
		}
	case VisualizationCardinalityKindLowerBound, VisualizationCardinalityKindEstimated:
		if state.Cardinality.Count == nil || *state.Cardinality.Count < 0 {
			return fmt.Errorf("window cardinality estimate is missing or negative")
		}
	default:
		return fmt.Errorf("unsupported window cardinality kind %q", state.Cardinality.Kind)
	}
	columns := make([]string, len(state.Schema.Fields))
	for index, field := range state.Schema.Fields {
		columns[index] = field.ID
	}
	for key, block := range state.Blocks {
		if key != block.ID || block.ID == "" {
			return fmt.Errorf("window block identity mismatch for %q", key)
		}
		if block.Start < 0 || block.RequestSeq < 0 || block.ResetVersion != state.ResetVersion {
			return fmt.Errorf("window block %q has stale or invalid coordinates", key)
		}
		if block.Start+int64(len(block.Rows)) > state.AvailableRows {
			return fmt.Errorf("window block %q exceeds available rows", key)
		}
		if err := validateRows(state.Schema, columns, block.Rows); err != nil {
			return fmt.Errorf("window block %q: %w", key, err)
		}
	}
	return nil
}

func validateSpatialWindowedState(state SpatialWindowedVisualizationDataState, budget VisualizationDataBudget) error {
	if err := validateSchema(state.Schema); err != nil {
		return err
	}
	if state.RowCap <= 0 || state.RowCap > budget.MaxRows || state.FeatureCap <= 0 || state.FeatureCap > 5000 || state.ResetVersion < 0 {
		return fmt.Errorf("invalid spatial window budgets")
	}
	if err := validateSpatialBounds(state.Extent); err != nil {
		return fmt.Errorf("invalid spatial extent: %w", err)
	}
	if state.Window == nil {
		return nil
	}
	window := state.Window
	if window.ID == "" || window.RequestSeq <= 0 || window.ResetVersion != state.ResetVersion || window.Width <= 0 || window.Width > 16384 || window.Height <= 0 || window.Height > 16384 || window.Zoom < 0 || window.Zoom > 24 || int64(len(window.Rows)) > state.FeatureCap {
		return fmt.Errorf("invalid or stale spatial window")
	}
	if err := validateSpatialBounds(window.Bounds); err != nil {
		return fmt.Errorf("invalid spatial window bounds: %w", err)
	}
	if window.Precision != VisualizationSpatialPrecisionRaw && window.Precision != VisualizationSpatialPrecisionAggregated {
		return fmt.Errorf("unsupported spatial precision %q", window.Precision)
	}
	columns := make([]string, len(state.Schema.Fields))
	for index, field := range state.Schema.Fields {
		columns[index] = field.ID
	}
	if err := validateRows(state.Schema, columns, window.Rows); err != nil {
		return fmt.Errorf("spatial window %q: %w", window.ID, err)
	}
	return nil
}

func validateSpatialTiledState(state SpatialTiledVisualizationDataState, schemas map[string]VisualizationDatasetSchema) error {
	if err := validateSchema(state.Schema); err != nil {
		return err
	}
	schema, ok := schemas[state.Schema.ID]
	if !ok || !reflect.DeepEqual(schema, state.Schema) {
		return fmt.Errorf("spatial tiled schema must exactly match a specification dataset")
	}
	if state.Cardinality.Kind != VisualizationCardinalityKindExact || state.Cardinality.Count == nil || *state.Cardinality.Count < 0 {
		return fmt.Errorf("spatial tiled cardinality must be exact and non-negative")
	}
	if err := validateSpatialMetadataExtent(state.Extent); err != nil {
		return fmt.Errorf("invalid spatial tiled extent: %w", err)
	}
	if !strings.HasPrefix(state.TileURL, "/") || !strings.Contains(state.TileURL, "{z}") || !strings.Contains(state.TileURL, "{x}") || !strings.Contains(state.TileURL, "{y}") {
		return fmt.Errorf("spatial tiled URL must be an opaque same-origin XYZ template")
	}
	if state.MinimumZoom < 0 || state.MaximumZoom > 24 || state.MinimumZoom > state.MaximumZoom || state.RawMinimumZoom < state.MinimumZoom || state.RawMinimumZoom > state.MaximumZoom+1 {
		return fmt.Errorf("invalid spatial tiled zoom policy")
	}
	if state.FeatureCap <= 0 || state.FeatureCap > 5000 || state.MaximumTileBytes <= 0 || state.MaximumTileBytes > 512*1024 {
		return fmt.Errorf("invalid spatial tiled transport budgets")
	}
	if err := validateSpatialDomains(state.RawDomains, state.Schema); err != nil {
		return fmt.Errorf("invalid raw spatial domains: %w", err)
	}
	if err := validateSpatialDomains(state.AggregateDomains, state.Schema); err != nil {
		return fmt.Errorf("invalid aggregate spatial domains: %w", err)
	}
	return nil
}

func validateSpatialMetadataExtent(bounds VisualizationSpatialBounds) error {
	for _, coordinate := range []float64{bounds.West, bounds.South, bounds.East, bounds.North} {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return fmt.Errorf("coordinates must be finite")
		}
	}
	if bounds.West < -180 || bounds.West > 180 || bounds.East < -180 || bounds.East > 180 || bounds.South < -90 || bounds.South > 90 || bounds.North < -90 || bounds.North > 90 || bounds.South > bounds.North {
		return fmt.Errorf("coordinates are outside geographic bounds")
	}
	return nil
}

func validateSpatialDomains(domains []VisualizationSpatialScaleDomain, schema VisualizationDatasetSchema) error {
	fields := make(map[string]VisualizationField, len(schema.Fields))
	for _, field := range schema.Fields {
		fields[field.ID] = field
	}
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		field, ok := fields[domain.Field]
		if !ok {
			return fmt.Errorf("domain references unknown field %q", domain.Field)
		}
		if field.DataType != VisualizationDataTypeInteger && field.DataType != VisualizationDataTypeDecimal {
			return fmt.Errorf("domain field %q is not numeric", domain.Field)
		}
		if _, ok := seen[domain.Field]; ok {
			return fmt.Errorf("domain field %q is duplicated", domain.Field)
		}
		seen[domain.Field] = struct{}{}
		for _, value := range []*float64{domain.Minimum, domain.Maximum, domain.Total} {
			if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
				return fmt.Errorf("domain field %q contains a non-finite value", domain.Field)
			}
		}
		if domain.Minimum != nil && domain.Maximum != nil && *domain.Minimum > *domain.Maximum {
			return fmt.Errorf("domain field %q minimum exceeds maximum", domain.Field)
		}
	}
	return nil
}

func validateSpatialBounds(bounds VisualizationSpatialBounds) error {
	for _, coordinate := range []float64{bounds.West, bounds.South, bounds.East, bounds.North} {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return fmt.Errorf("coordinates must be finite")
		}
	}
	if bounds.West < -180 || bounds.West > 180 || bounds.East < -180 || bounds.East > 180 || bounds.South < -90 || bounds.South > 90 || bounds.North < -90 || bounds.North > 90 || bounds.South >= bounds.North || bounds.West == bounds.East {
		return fmt.Errorf("coordinates are outside geographic bounds")
	}
	return nil
}

func validateRows(schema VisualizationDatasetSchema, columns []string, rows [][]any) error {
	if len(columns) == 0 {
		return fmt.Errorf("columns are required")
	}
	fields := make(map[string]VisualizationField, len(schema.Fields))
	for _, field := range schema.Fields {
		fields[field.ID] = field
	}
	ordered := make([]VisualizationField, len(columns))
	seen := make(map[string]struct{}, len(columns))
	for index, column := range columns {
		field, ok := fields[column]
		if !ok {
			return fmt.Errorf("unknown column %q", column)
		}
		if _, exists := seen[column]; exists {
			return fmt.Errorf("duplicate column %q", column)
		}
		seen[column] = struct{}{}
		ordered[index] = field
	}
	for rowIndex, row := range rows {
		if len(row) != len(ordered) {
			return fmt.Errorf("row %d has width %d, want %d", rowIndex, len(row), len(ordered))
		}
		for columnIndex, value := range row {
			if err := validateScalar(ordered[columnIndex], value); err != nil {
				return fmt.Errorf("row %d column %q: %w", rowIndex, ordered[columnIndex].ID, err)
			}
		}
	}
	return nil
}

func validateScalar(field VisualizationField, value any) error {
	if value == nil {
		if field.Nullable {
			return nil
		}
		return fmt.Errorf("null is not allowed")
	}
	switch field.DataType {
	case VisualizationDataTypeString, VisualizationDataTypeTemporal, VisualizationDataTypeDate, VisualizationDataTypeGeographic:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected string scalar, got %T", value)
		}
	case VisualizationDataTypeBoolean:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean scalar, got %T", value)
		}
	case VisualizationDataTypeInteger:
		number, ok := scalarNumber(value)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number {
			return fmt.Errorf("expected finite integer scalar, got %v", value)
		}
	case VisualizationDataTypeDecimal:
		number, ok := scalarNumber(value)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("expected finite decimal scalar, got %v", value)
		}
	default:
		return fmt.Errorf("unsupported data type %q", field.DataType)
	}
	return nil
}

func scalarNumber(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	default:
		return 0, false
	}
}
