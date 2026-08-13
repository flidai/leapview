package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UnmarshalJSON keeps the visualization boundary closed even though the
// generated envelope is not itself a discriminated union.
func (envelope *VisualizationEnvelope) UnmarshalJSON(data []byte) error {
	type wireEnvelope VisualizationEnvelope
	var decoded wireEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode visualization envelope: %w", err)
	}
	*envelope = VisualizationEnvelope(decoded)
	return nil
}

// ValidateEnvelopeRevisions verifies the compatibility boundary that must hold
// before a browser renderer observes an envelope. Structural and semantic data
// validation belongs to the frame validators layered on top of this primitive.
func ValidateEnvelopeRevisions(envelope VisualizationEnvelope) error {
	if envelope.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported visualization schema version %d", envelope.SchemaVersion)
	}
	declared, err := ParseSpecRevision(envelope.SpecRevision)
	if err != nil {
		return err
	}
	computed, err := ComputeSpecRevision(envelope.Spec)
	if err != nil {
		return err
	}
	if declared != computed {
		return fmt.Errorf("visualization specification revision mismatch: declared %q, computed %q", declared, computed)
	}

	specRevision, dataRevision, generation, err := dataStateRevisions(envelope.DataState)
	if err != nil {
		return err
	}
	if specRevision != envelope.SpecRevision {
		return fmt.Errorf("visualization data state targets specification %q, want %q", specRevision, envelope.SpecRevision)
	}
	if dataRevision != envelope.DataRevision {
		return fmt.Errorf("visualization data state revision %d, want %d", dataRevision, envelope.DataRevision)
	}
	if dataRevision < 0 || generation < 0 {
		return fmt.Errorf("visualization data revision and generation must be non-negative")
	}

	for index, selection := range envelope.Selection {
		if selection.Datum.DataRevision != envelope.DataRevision {
			return fmt.Errorf("visualization selection %d targets data revision %d, want %d", index, selection.Datum.DataRevision, envelope.DataRevision)
		}
	}
	return nil
}

// WithStreamRevision returns an envelope whose revision-bearing state is
// updated atomically. Specifications and frame values remain immutable and may
// be shared; the revision-bearing structs and selection identities are copied
// so concurrent page streams cannot mutate one another.
func WithStreamRevision(envelope VisualizationEnvelope, dataRevision, generation int64) (VisualizationEnvelope, error) {
	if dataRevision < 0 || generation < 0 {
		return VisualizationEnvelope{}, fmt.Errorf("visualization data revision and generation must be non-negative")
	}
	revised := envelope
	revised.DataRevision = dataRevision
	switch value := envelope.DataState.Value.(type) {
	case *InlineVisualizationDataState:
		if value == nil {
			return VisualizationEnvelope{}, fmt.Errorf("visualization inline data state is nil")
		}
		state := cloneInlineState(*value, dataRevision, generation)
		revised.DataState.Value = &state
	case *WindowedVisualizationDataState:
		if value == nil {
			return VisualizationEnvelope{}, fmt.Errorf("visualization windowed data state is nil")
		}
		state := *value
		state.DataRevision, state.Generation = dataRevision, generation
		revised.DataState.Value = &state
	case *SpatialTiledVisualizationDataState:
		if value == nil {
			return VisualizationEnvelope{}, fmt.Errorf("visualization spatial tiled data state is nil")
		}
		state := *value
		state.DataRevision, state.Generation = dataRevision, generation
		revised.DataState.Value = &state
	default:
		return VisualizationEnvelope{}, fmt.Errorf("unsupported visualization data state variant %T", value)
	}
	revised.Selection = make([]VisualizationSelectionEntry, len(envelope.Selection))
	for index, entry := range envelope.Selection {
		revised.Selection[index] = entry
		revised.Selection[index].Datum.DataRevision = dataRevision
		if entry.Datum.Identity != nil {
			identity := make(map[string]any, len(entry.Datum.Identity))
			for key, item := range entry.Datum.Identity {
				identity[key] = item
			}
			revised.Selection[index].Datum.Identity = identity
		}
	}
	if err := ValidateEnvelope(revised); err != nil {
		return VisualizationEnvelope{}, err
	}
	return revised, nil
}

func cloneInlineState(value InlineVisualizationDataState, dataRevision, generation int64) InlineVisualizationDataState {
	state := value
	state.DataRevision, state.Generation = dataRevision, generation
	state.Datasets = append([]VisualizationInlineDataset(nil), value.Datasets...)
	for index := range state.Datasets {
		state.Datasets[index].DataRevision = dataRevision
		state.Datasets[index].Generation = generation
	}
	return state
}

func dataStateRevisions(state VisualizationDataState) (specRevision string, dataRevision, generation int64, err error) {
	visitor := &dataStateRevisionVisitor{}
	if err := state.Visit(visitor); err != nil {
		return "", 0, 0, err
	}
	return visitor.specRevision, visitor.dataRevision, visitor.generation, nil
}

type dataStateRevisionVisitor struct {
	specRevision string
	dataRevision int64
	generation   int64
}

func (visitor *dataStateRevisionVisitor) set(specRevision string, dataRevision, generation int64) {
	visitor.specRevision = specRevision
	visitor.dataRevision = dataRevision
	visitor.generation = generation
}

func (visitor *dataStateRevisionVisitor) VisitInlineVisualizationDataState(value *InlineVisualizationDataState) error {
	if err := validateInlineDatasetRevisions(*value); err != nil {
		return err
	}
	visitor.set(value.SpecRevision, value.DataRevision, value.Generation)
	return nil
}

func (visitor *dataStateRevisionVisitor) VisitWindowedVisualizationDataState(value *WindowedVisualizationDataState) error {
	visitor.set(value.SpecRevision, value.DataRevision, value.Generation)
	return nil
}

func (visitor *dataStateRevisionVisitor) VisitSpatialTiledVisualizationDataState(value *SpatialTiledVisualizationDataState) error {
	visitor.set(value.SpecRevision, value.DataRevision, value.Generation)
	return nil
}

func validateInlineDatasetRevisions(state InlineVisualizationDataState) error {
	for index, dataset := range state.Datasets {
		if dataset.SpecRevision != state.SpecRevision {
			return fmt.Errorf("visualization inline dataset %d targets specification %q, want %q", index, dataset.SpecRevision, state.SpecRevision)
		}
		if dataset.DataRevision != state.DataRevision {
			return fmt.Errorf("visualization inline dataset %d revision %d, want %d", index, dataset.DataRevision, state.DataRevision)
		}
		if dataset.Generation != state.Generation {
			return fmt.Errorf("visualization inline dataset %d generation %d, want %d", index, dataset.Generation, state.Generation)
		}
	}
	return nil
}
