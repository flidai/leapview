package exploration

import "testing"

func TestRecordsModeShape(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ExplorationSpec)
		valid  bool
	}{
		{name: "records", valid: true},
		{name: "invalid mode", mutate: func(spec *ExplorationSpec) { mode := ExplorationQueryMode("unknown"); spec.Mode = &mode }},
		{name: "metrics", mutate: func(spec *ExplorationSpec) { spec.Metrics = []ExplorationMetricRef{{Field: "revenue"}} }},
		{name: "time", mutate: func(spec *ExplorationSpec) {
			spec.Time = &ExplorationTimeSelection{Field: "orders.created_at", Grain: ExplorationTimeGrainDay}
		}},
		{name: "grain", mutate: func(spec *ExplorationSpec) {
			grain := ExplorationTimeGrainDay
			spec.Dimensions = []ExplorationDimensionRef{{Field: "orders.created_at", Grain: &grain}}
		}},
		{name: "pivot", mutate: func(spec *ExplorationSpec) { spec.Pivot = &ExplorationPivotConfig{} }},
		{name: "missing dataset", mutate: func(spec *ExplorationSpec) { spec.DatasetID = nil }},
		{name: "missing fields", mutate: func(spec *ExplorationSpec) { spec.Dimensions = []ExplorationDimensionRef{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := validExplorationSpec()
			mode, dataset := ExplorationQueryModeRecords, "orders"
			spec.Mode, spec.DatasetID = &mode, &dataset
			spec.Metrics = []ExplorationMetricRef{}
			if test.mutate != nil {
				test.mutate(spec)
			}
			err := ValidateShape(spec)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateShape() = %v, want valid=%v", err, test.valid)
			}
		})
	}
}
