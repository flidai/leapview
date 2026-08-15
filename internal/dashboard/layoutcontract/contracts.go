// Package layoutcontract owns the responsive widget layout rules used during
// dashboard compilation. Browser consumers receive a generated copy of the
// canonical contract document instead of importing Go-internal source files.
package layoutcontract

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed contracts.json
var contractJSON []byte

type ContractID string

const (
	ContractKPI                  ContractID = "kpi"
	ContractSlicerDropdown       ContractID = "slicer.dropdown"
	ContractSlicerInput          ContractID = "slicer.input"
	ContractSlicerNumericRange   ContractID = "slicer.numeric_range"
	ContractSlicerDateRange      ContractID = "slicer.date_range"
	ContractSlicerRelativePeriod ContractID = "slicer.relative_period"
)

type Feature string

const (
	FeatureSubtitle   Feature = "subtitle"
	FeatureComparison Feature = "comparison"
	FeatureProgress   Feature = "progress"
	FeatureGoal       Feature = "goal"
	FeatureStatus     Feature = "status"
	FeatureTrend      Feature = "trend"
	FeatureNote       Feature = "note"
	FeatureSummary    Feature = "summary"
)

type Size struct {
	Width  int
	Height int
}

type Requirement struct {
	Layout  string
	Minimum Size
}

type Resolution struct {
	Fits         bool
	Layout       string
	Minimum      Size
	Requirements []Requirement
}

type contractDocument struct {
	Version int                       `json:"version"`
	Widgets map[ContractID]widgetSpec `json:"widgets"`
}

type widgetSpec struct {
	Layouts []layoutSpec `json:"layouts"`
	Chrome  sizeSpec     `json:"chrome"`
}

type layoutSpec struct {
	ID       string                  `json:"id"`
	Minimum  sizeSpec                `json:"minimum"`
	Features map[Feature]featureCost `json:"features"`
}

type sizeSpec struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type featureCost struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

var contracts = mustLoadContracts()

func Requirements(id ContractID, features []Feature) ([]Requirement, error) {
	spec, ok := contracts.Widgets[id]
	if !ok {
		return nil, fmt.Errorf("unknown dashboard layout contract %q", id)
	}
	enabled := make(map[Feature]struct{}, len(features))
	for _, feature := range features {
		enabled[feature] = struct{}{}
	}
	requirements := make([]Requirement, 0, len(spec.Layouts))
	for _, candidate := range spec.Layouts {
		widthAddition := 0
		height := candidate.Minimum.Height
		for feature := range enabled {
			cost, ok := candidate.Features[feature]
			if !ok {
				return nil, fmt.Errorf("layout %q does not support explicit feature %q", candidate.ID, feature)
			}
			widthAddition = max(widthAddition, cost.Width)
			height += cost.Height
		}
		requirements = append(requirements, Requirement{
			Layout: candidate.ID,
			Minimum: Size{
				Width:  candidate.Minimum.Width + widthAddition,
				Height: height,
			},
		})
	}
	return requirements, nil
}

func OuterRequirements(id ContractID, features []Feature) ([]Requirement, error) {
	requirements, err := Requirements(id, features)
	if err != nil {
		return nil, err
	}
	chrome := contracts.Widgets[id].Chrome
	for index := range requirements {
		requirements[index].Minimum.Width += chrome.Width
		requirements[index].Minimum.Height += chrome.Height
	}
	return requirements, nil
}

func Resolve(id ContractID, available Size, features []Feature) (Resolution, error) {
	requirements, err := Requirements(id, features)
	if err != nil {
		return Resolution{}, err
	}
	return resolve(available, requirements), nil
}

func ResolveOuter(id ContractID, available Size, features []Feature) (Resolution, error) {
	requirements, err := OuterRequirements(id, features)
	if err != nil {
		return Resolution{}, err
	}
	return resolve(available, requirements), nil
}

func resolve(available Size, requirements []Requirement) Resolution {
	for _, requirement := range requirements {
		if available.Width >= requirement.Minimum.Width && available.Height >= requirement.Minimum.Height {
			return Resolution{
				Fits: true, Layout: requirement.Layout, Minimum: requirement.Minimum,
				Requirements: append([]Requirement(nil), requirements...),
			}
		}
	}
	return Resolution{Requirements: append([]Requirement(nil), requirements...)}
}

func mustLoadContracts() contractDocument {
	var document contractDocument
	if err := json.Unmarshal(contractJSON, &document); err != nil {
		panic(fmt.Sprintf("decode dashboard layout contracts: %v", err))
	}
	if document.Version != 1 || len(document.Widgets) == 0 {
		panic("dashboard layout contracts require version 1 and at least one widget")
	}
	expected := []ContractID{
		ContractKPI,
		ContractSlicerDropdown,
		ContractSlicerInput,
		ContractSlicerNumericRange,
		ContractSlicerDateRange,
		ContractSlicerRelativePeriod,
	}
	for _, id := range expected {
		widget, ok := document.Widgets[id]
		if !ok || len(widget.Layouts) == 0 || widget.Chrome.Width < 0 || widget.Chrome.Height < 0 {
			panic(fmt.Sprintf("invalid dashboard layout contract %q", id))
		}
		seen := map[string]struct{}{}
		for _, candidate := range widget.Layouts {
			if candidate.ID == "" || candidate.Minimum.Width < 0 || candidate.Minimum.Height < 0 {
				panic(fmt.Sprintf("invalid layout in dashboard contract %q", id))
			}
			if _, duplicate := seen[candidate.ID]; duplicate {
				panic(fmt.Sprintf("duplicate layout %q in dashboard contract %q", candidate.ID, id))
			}
			seen[candidate.ID] = struct{}{}
			for feature, cost := range candidate.Features {
				if !knownFeature(feature) || cost.Width < 0 || cost.Height < 0 {
					panic(fmt.Sprintf("invalid feature %q in dashboard contract %q", feature, id))
				}
			}
		}
	}
	return document
}

func knownFeature(feature Feature) bool {
	switch feature {
	case FeatureSubtitle, FeatureComparison, FeatureProgress, FeatureGoal, FeatureStatus, FeatureTrend, FeatureNote, FeatureSummary:
		return true
	default:
		return false
	}
}
