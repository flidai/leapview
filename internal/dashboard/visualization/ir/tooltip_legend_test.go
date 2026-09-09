package ir

import (
	"strings"
	"testing"
)

func tooltipLegendContractSpec() VisualizationSpec {
	base := VisualizationSpecBase{
		Kind: "cartesian", Title: "Orders",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "category", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Category"},
			{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Value"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 10, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Orders", Description: "Orders"},
	}
	return VisualizationSpec{Value: &CartesianVisualizationSpec{
		VisualizationSpecBase: base, Kind: "cartesian", Mark: VisualizationCartesianMarkBar,
		X:            VisualizationFieldRef{Dataset: "primary", Field: "category"},
		Y:            []VisualizationFieldRef{{Dataset: "primary", Field: "value"}},
		Presentation: CartesianVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom)},
	}}
}

func TestValidateSpecAcceptsTooltipAndLegendMetadata(t *testing.T) {
	spec := tooltipLegendContractSpec()
	minimum, maximum := int32(1), int32(2)
	specBase, _ := SpecificationBase(spec)
	specBase.TooltipItems = &[]VisualizationTooltipItem{{
		Field:  VisualizationFieldRef{Dataset: "primary", Field: "value"},
		Label:  stringPtr("Amount"),
		Format: &VisualizationFormat{Value: &NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: &minimum, MaximumFractionDigits: &maximum}},
	}}
	spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = specBase
	spec.Value.(*CartesianVisualizationSpec).Presentation.LegendTitle = stringPtr("Order status")
	spec.Value.(*CartesianVisualizationSpec).Presentation.LegendItems = &[]VisualizationLegendItem{{Value: "value", Label: stringPtr("Value")}}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec() error = %v", err)
	}
}

func TestValidateSpecAcceptsExplicitlyEmptyTooltipItems(t *testing.T) {
	spec := tooltipLegendContractSpec()
	base, _ := SpecificationBase(spec)
	empty := []VisualizationTooltipItem{}
	base.TooltipItems = &empty
	spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec() rejected empty root tooltipItems: %v", err)
	}

	// Row-backed geographic layers retain the explicit-empty distinction. Use a
	// path layer here because reference layers intentionally reject presence.
	rowBacked := referenceTooltipSpec(nil)
	geographic := rowBacked.Value.(*GeographicVisualizationSpec)
	layer := geographic.Layers[0]
	layerBase, err := layer.Base()
	if err != nil {
		t.Fatalf("row-backed layer base: %v", err)
	}
	layerEmpty := []VisualizationTooltipItem{}
	layerBase.TooltipItems = &layerEmpty
	geographic.Layers[0].Value = &VisualizationPathLayer{
		VisualizationGeographicLayerBase: *layerBase,
		Kind:                             "path",
		Latitude:                         VisualizationFieldRef{Dataset: "primary", Field: "label"},
		Longitude:                        VisualizationFieldRef{Dataset: "primary", Field: "label"},
		Path:                             VisualizationFieldRef{Dataset: "primary", Field: "label"},
		Order:                            VisualizationFieldRef{Dataset: "primary", Field: "label"},
	}
	if err := ValidateSpec(rowBacked); err != nil {
		t.Fatalf("ValidateSpec() rejected empty row-backed geographic tooltipItems: %v", err)
	}
}

func TestValidateSpecRejectsRootTooltipItemsFromAnotherDataset(t *testing.T) {
	spec := tooltipLegendContractSpec()
	base, _ := SpecificationBase(spec)
	base.Datasets = append(base.Datasets, VisualizationDatasetSchema{ID: "context", Fields: []VisualizationField{
		{ID: "note", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Note"},
	}})
	base.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "context", Field: "note"}}}
	spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base

	err := ValidateSpec(spec)
	if err == nil || !strings.Contains(err.Error(), `spec.tooltipItems[0].field dataset "context" does not match row dataset "primary"`) {
		t.Fatalf("ValidateSpec() error = %v, want row-dataset tooltip diagnostic", err)
	}
}

func TestValidateSpecRejectsLegacyTooltipRefsFromAnotherDataset(t *testing.T) {
	spec := tooltipLegendContractSpec()
	base, _ := SpecificationBase(spec)
	base.Datasets = append(base.Datasets, VisualizationDatasetSchema{ID: "context", Fields: []VisualizationField{
		{ID: "note", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Note"},
	}})
	spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base
	spec.Value.(*CartesianVisualizationSpec).Tooltip = &[]VisualizationFieldRef{{Dataset: "context", Field: "note"}}

	err := ValidateSpec(spec)
	if err == nil || !strings.Contains(err.Error(), `spec.tooltip[0].field dataset "context" does not match row dataset "primary"`) {
		t.Fatalf("ValidateSpec() error = %v, want row-dataset legacy tooltip diagnostic", err)
	}
}

func TestValidateSpecRejectsGeographicTooltipItemsFromAnotherDataset(t *testing.T) {
	base := VisualizationSpecBase{
		Kind: "geographic", Title: "Stores",
		Datasets: []VisualizationDatasetSchema{
			{ID: "primary", Fields: []VisualizationField{
				{ID: "lat", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Latitude"},
				{ID: "lon", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Longitude"},
			}},
			{ID: "context", Fields: []VisualizationField{
				{ID: "note", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Note"},
			}},
		},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Stores", Description: "Store locations"},
		Interactions:  []VisualizationInteraction{},
	}
	layerBase := VisualizationGeographicLayerBase{
		ID: "stores", Kind: "point",
		TooltipItems: &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "context", Field: "note"}}},
		Position:     VisualizationMapLayerPositionBelowLabels, Visibility: VisualizationMapVisibility{MaximumZoom: 24},
	}
	spec := VisualizationSpec{Value: &GeographicVisualizationSpec{
		VisualizationSpecBase: base, Kind: "geographic",
		Layers: []VisualizationGeographicLayer{{Value: &VisualizationPointLayer{
			VisualizationGeographicLayerBase: layerBase, Kind: "point",
			Latitude: VisualizationFieldRef{Dataset: "primary", Field: "lat"}, Longitude: VisualizationFieldRef{Dataset: "primary", Field: "lon"},
			Size: VisualizationMapSizeScale{MinimumRadius: 5, MaximumRadius: 28}, Cluster: VisualizationMapCluster{Radius: 50, MinimumPoints: 2},
		}}},
		Presentation: GeographicVisualizationPresentation{
			VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden),
			Camera:                    VisualizationMapCamera{Mode: VisualizationMapCameraModeFitData, Padding: 32, MaximumZoom: 14},
		},
	}}

	err := ValidateSpec(spec)
	if err == nil || !strings.Contains(err.Error(), `spec.layers[0].tooltipItems[0].field dataset "context" does not match row dataset "primary"`) {
		t.Fatalf("ValidateSpec() error = %v, want row-dataset tooltip diagnostic", err)
	}

	layer, err := spec.Value.(*GeographicVisualizationSpec).Layers[0].Base()
	if err != nil {
		t.Fatalf("geographic layer base: %v", err)
	}
	layer.TooltipItems = nil
	layer.Tooltip = []VisualizationFieldRef{{Dataset: "context", Field: "note"}}
	err = ValidateSpec(spec)
	if err == nil || !strings.Contains(err.Error(), `spec.layers[0].tooltip[0].field dataset "context" does not match row dataset "primary"`) {
		t.Fatalf("ValidateSpec() error = %v, want row-dataset legacy tooltip diagnostic", err)
	}
}

func TestValidateSpecRejectsConflictingLegacyAndStructuredTooltips(t *testing.T) {
	root := tooltipLegendContractSpec()
	rootBase, _ := SpecificationBase(root)
	rootBase.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "primary", Field: "value"}}}
	root.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = rootBase
	root.Value.(*CartesianVisualizationSpec).Tooltip = &[]VisualizationFieldRef{{Dataset: "primary", Field: "category"}}
	if err := ValidateSpec(root); err == nil || !strings.Contains(err.Error(), "spec.tooltipItems[0].field") || !strings.Contains(err.Error(), "spec.tooltip[0]") {
		t.Fatalf("root ValidateSpec() error = %v, want ordered tooltip mismatch diagnostic", err)
	}

	geographic := referenceTooltipSpec(nil)
	geographicBase, _ := SpecificationBase(geographic)
	geographicBase.Datasets[0].Fields = append(geographicBase.Datasets[0].Fields, VisualizationField{
		ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Value",
	})
	geographic.Value.(*GeographicVisualizationSpec).VisualizationSpecBase = geographicBase
	layer := geographic.Value.(*GeographicVisualizationSpec).Layers[0]
	layerBase, err := layer.Base()
	if err != nil {
		t.Fatalf("geographic layer base: %v", err)
	}
	layerBase.Tooltip = []VisualizationFieldRef{{Dataset: "primary", Field: "label"}}
	layerBase.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "primary", Field: "value"}}}
	geographic.Value.(*GeographicVisualizationSpec).Layers[0].Value = &VisualizationPathLayer{
		VisualizationGeographicLayerBase: *layerBase,
		Kind:                             "path",
		Latitude:                         VisualizationFieldRef{Dataset: "primary", Field: "label"},
		Longitude:                        VisualizationFieldRef{Dataset: "primary", Field: "label"},
		Path:                             VisualizationFieldRef{Dataset: "primary", Field: "label"},
		Order:                            VisualizationFieldRef{Dataset: "primary", Field: "label"},
	}
	if err := ValidateSpec(geographic); err == nil || !strings.Contains(err.Error(), "spec.layers[0].tooltipItems[0].field") || !strings.Contains(err.Error(), "spec.layers[0].tooltip[0]") {
		t.Fatalf("geographic ValidateSpec() error = %v, want ordered tooltip mismatch diagnostic", err)
	}
}

func TestValidateSpecRejectsExplicitlyEmptyUnsupportedTooltipItems(t *testing.T) {
	empty := []VisualizationTooltipItem{}

	polarSpec := tooltipLegendContractSpec()
	polarBase, _ := SpecificationBase(polarSpec)
	polarBase.Kind = "polar"
	polarBase.TooltipItems = &empty
	polarSpec.Value = &PolarVisualizationSpec{
		VisualizationSpecBase: polarBase, Kind: "polar", Mark: VisualizationPolarMarkGauge,
		Value:        VisualizationFieldRef{Dataset: "primary", Field: "value"},
		Presentation: PolarVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden)},
	}
	if err := ValidateSpec(polarSpec); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ValidateSpec() error = %v, want unsupported polar tooltip diagnostic", err)
	}

	geographicSpec := referenceTooltipSpec(nil)
	geographicBase, _ := SpecificationBase(geographicSpec)
	geographicBase.Kind = "geographic"
	geographicBase.TooltipItems = &empty
	geographicSpec.Value.(*GeographicVisualizationSpec).VisualizationSpecBase = geographicBase
	if err := ValidateSpec(geographicSpec); err == nil || !strings.Contains(err.Error(), "spec.tooltipItems is unsupported for geographic") {
		t.Fatalf("ValidateSpec() error = %v, want unsupported geographic tooltip diagnostic", err)
	}

	reference := referenceTooltipSpec(nil)
	layer := reference.Value.(*GeographicVisualizationSpec).Layers[0]
	layerBase, err := layer.Base()
	if err != nil {
		t.Fatalf("reference layer base: %v", err)
	}
	layerBase.TooltipItems = &empty
	if err := ValidateSpec(reference); err == nil || !strings.Contains(err.Error(), "spec.layers[0].tooltipItems") {
		t.Fatalf("ValidateSpec() error = %v, want reference tooltipItems diagnostic", err)
	}
}

func TestValidateSpecRejectsTooltipAndLegendMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*VisualizationSpec)
		want   string
	}{
		{name: "duplicate tooltip", mutate: func(spec *VisualizationSpec) {
			base, _ := SpecificationBase(*spec)
			base.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "primary", Field: "value"}}, {Field: VisualizationFieldRef{Dataset: "primary", Field: "value"}}}
			spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base
		}, want: "duplicates"},
		{name: "format type mismatch", mutate: func(spec *VisualizationSpec) {
			base, _ := SpecificationBase(*spec)
			base.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "primary", Field: "category"}, Format: &VisualizationFormat{Value: &PercentVisualizationFormat{Kind: "percent"}}}}
			spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base
		}, want: "incompatible"},
		{name: "unsupported currency", mutate: func(spec *VisualizationSpec) {
			base, _ := SpecificationBase(*spec)
			base.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "primary", Field: "value"}, Format: &VisualizationFormat{Value: &CurrencyVisualizationFormat{Kind: "currency", Currency: "JPY"}}}}
			spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base
		}, want: "unsupported"},
		{name: "unsupported duration", mutate: func(spec *VisualizationSpec) {
			base, _ := SpecificationBase(*spec)
			base.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "primary", Field: "value"}, Format: &VisualizationFormat{Value: &DurationVisualizationFormat{Kind: "duration", Unit: "weeks"}}}}
			spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base
		}, want: "unsupported"},
		{name: "legend item surrounding whitespace", mutate: func(spec *VisualizationSpec) {
			spec.Value.(*CartesianVisualizationSpec).Presentation.LegendItems = &[]VisualizationLegendItem{{Value: " value "}}
		}, want: "surrounding whitespace"},
		{name: "currency surrounding whitespace", mutate: func(spec *VisualizationSpec) {
			base, _ := SpecificationBase(*spec)
			base.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "primary", Field: "value"}, Format: &VisualizationFormat{Value: &CurrencyVisualizationFormat{Kind: "currency", Currency: " USD "}}}}
			spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base
		}, want: "surrounding whitespace"},
		{name: "duration surrounding whitespace", mutate: func(spec *VisualizationSpec) {
			base, _ := SpecificationBase(*spec)
			base.TooltipItems = &[]VisualizationTooltipItem{{Field: VisualizationFieldRef{Dataset: "primary", Field: "value"}, Format: &VisualizationFormat{Value: &DurationVisualizationFormat{Kind: "duration", Unit: " days"}}}}
			spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase = base
		}, want: "surrounding whitespace"},
		{name: "hidden legend metadata", mutate: func(spec *VisualizationSpec) {
			presentation := &spec.Value.(*CartesianVisualizationSpec).Presentation
			presentation.Legend = VisualizationLegendPositionHidden
			presentation.LegendTitle = stringPtr("Hidden")
		}, want: "hidden legend"},
		{name: "empty legend items", mutate: func(spec *VisualizationSpec) {
			items := []VisualizationLegendItem{}
			spec.Value.(*CartesianVisualizationSpec).Presentation.LegendItems = &items
		}, want: "at least one item"},
		{name: "title on unsupported mark", mutate: func(spec *VisualizationSpec) {
			presentation := &spec.Value.(*CartesianVisualizationSpec).Presentation
			presentation.LegendTitle = stringPtr("Heatmap")
			spec.Value.(*CartesianVisualizationSpec).Mark = VisualizationCartesianMarkHeatmap
		}, want: "unsupported"},
		{name: "items on candlestick", mutate: func(spec *VisualizationSpec) {
			presentation := &spec.Value.(*CartesianVisualizationSpec).Presentation
			presentation.LegendItems = &[]VisualizationLegendItem{{Value: "value"}}
			spec.Value.(*CartesianVisualizationSpec).Mark = VisualizationCartesianMarkCandlestick
		}, want: "legendItems is unsupported for candlestick"},
		{name: "unknown static legend item", mutate: func(spec *VisualizationSpec) {
			spec.Value.(*CartesianVisualizationSpec).Presentation.LegendItems = &[]VisualizationLegendItem{{Value: "missing"}}
		}, want: "legendItems[0].value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := tooltipLegendContractSpec()
			test.mutate(&spec)
			err := ValidateSpec(spec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateSpecRejectsUnsupportedTooltipFamily(t *testing.T) {
	spec := tooltipLegendContractSpec()
	base, _ := SpecificationBase(spec)
	base.Kind = "polar"
	base.TooltipItems = &[]VisualizationTooltipItem{}
	polar := &PolarVisualizationSpec{
		VisualizationSpecBase: base, Kind: "polar", Mark: VisualizationPolarMarkGauge,
		Value:        VisualizationFieldRef{Dataset: "primary", Field: "value"},
		Presentation: PolarVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden)},
	}
	spec.Value = polar
	if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ValidateSpec() error = %v, want unsupported tooltip diagnostic", err)
	}
}

func TestValidateSpecAllowsRuntimeLegendItemsForDynamicSeries(t *testing.T) {
	spec := tooltipLegendContractSpec()
	value := spec.Value.(*CartesianVisualizationSpec)
	value.Series = &VisualizationFieldRef{Dataset: "primary", Field: "category"}
	value.Presentation.LegendItems = &[]VisualizationLegendItem{{Value: "runtime-category"}}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec() rejected runtime legend item: %v", err)
	}
}

func stringPtr(value string) *string { return &value }
