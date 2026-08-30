package deploymentpostgres

// This file contains the value-only lowering used by native qualification.
// It intentionally has no runtime, catalog, or query dependencies: callers
// provide the exact immutable compiler artifact and observations captured by
// the physical build, and receive detached gate inputs.

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/analytics/gates"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

// nativeQualificationInputs lowers one exact candidate artifact and the
// source evidence captured while building it into the closed gate evaluator's
// typed inputs. No authored SQL or live runtime capability crosses this
// boundary. Both returned slices and every nested mutable value are detached.
//
// Live observations are authoritative whenever present. If no live
// observations are available, a complete, valid base gate record may supply
// the unchanged source schema/freshness evidence, matching the legacy
// sourceInputsFromManifest reuse semantics. Historical base query/row/time
// counters are deliberately not carried into the new gate budget.
func nativeQualificationInputs(
	artifacts release.CandidateArtifactSet,
	observations []analyticsmaterialize.SourceObservation,
) ([]gates.SourceInput, []gates.ModelInput, error) {
	manifest := artifacts.Compiler.Manifest
	if artifacts.Generation.DataMode != release.GenerationDataReuseBase && artifacts.Generation.DataMode != release.GenerationDataRefreshSources {
		return nil, nil, fmt.Errorf("%w: generation data mode %q is invalid", ErrNativeQualificationInvalid, artifacts.Generation.DataMode)
	}
	canonicalSources, aliases, err := nativeQualificationSourceIndex(manifest)
	if err != nil {
		return nil, nil, err
	}

	var sourceObservations map[string]analyticsmaterialize.SourceObservation
	if len(observations) == 0 {
		// A zero-source project has no live evidence to collect and therefore
		// does not need a base source record. This remains valid for refresh
		// candidates as well as reuse candidates.
		if len(manifest.Sources) == 0 {
			sourceObservations = make(map[string]analyticsmaterialize.SourceObservation)
		} else {
			// Reusing source schema/freshness evidence is only sound when the
			// candidate explicitly retains the sealed base data contract. A
			// refresh/rebuild candidate must provide live observations, even if a
			// base gate record happens to be attached to its artifacts.
			if artifacts.Generation.DataMode != release.GenerationDataReuseBase {
				return nil, nil, fmt.Errorf("%w: zero live source observations require reuse_base data mode", ErrNativeQualificationInvalid)
			}
			sourceObservations, err = nativeQualificationBaseObservations(artifacts, canonicalSources)
			if err != nil {
				return nil, nil, err
			}
			if sourceObservations == nil {
				sourceObservations = make(map[string]analyticsmaterialize.SourceObservation)
			}
		}
	} else {
		sourceObservations = make(map[string]analyticsmaterialize.SourceObservation, len(observations))
		if err := validateSourceObservations(observations); err != nil {
			return nil, nil, fmt.Errorf("%w: source observations: %v", ErrNativeQualificationInvalid, err)
		}
		for _, observation := range observations {
			canonicalID, ok := canonicalSources[observation.ID]
			if !ok {
				canonicalID = aliases[observation.ID]
			}
			if canonicalID == "" {
				return nil, nil, fmt.Errorf("%w: source observation %q is unknown", ErrNativeQualificationInvalid, observation.ID)
			}
			if _, duplicate := sourceObservations[canonicalID]; duplicate {
				return nil, nil, fmt.Errorf("%w: source observations contain duplicate canonical source %q", ErrNativeQualificationInvalid, canonicalID)
			}
			observation.ID = canonicalID
			observation.Schema = cloneColumnSchemas(observation.Schema)
			sourceObservations[canonicalID] = observation
		}
	}

	if len(sourceObservations) != len(manifest.Sources) {
		return nil, nil, fmt.Errorf("%w: source observations are incomplete: got %d, want %d", ErrNativeQualificationInvalid, len(sourceObservations), len(manifest.Sources))
	}
	for id := range manifest.Sources {
		if _, ok := sourceObservations[id]; !ok {
			return nil, nil, fmt.Errorf("%w: source observation %q is missing", ErrNativeQualificationInvalid, id)
		}
	}

	sourceIDs := make([]string, 0, len(manifest.Sources))
	for id := range manifest.Sources {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)
	sources := make([]gates.SourceInput, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		source, err := cloneQualificationSource(manifest.Sources[id])
		if err != nil {
			return nil, nil, fmt.Errorf("%w: clone source %q: %v", ErrNativeQualificationInvalid, id, err)
		}
		observation := sourceObservations[id]
		sources = append(sources, gates.SourceInput{
			ID:                 id,
			Source:             source,
			Observed:           cloneColumnSchemas(observation.Schema),
			Revision:           observation.Revision,
			RevisionObserved:   observation.RevisionObserved,
			FreshnessObserved:  observation.FreshnessObserved,
			FreshnessEmpty:     observation.FreshnessEmpty,
			SchemaFailure:      observation.SchemaFailure,
			FreshnessFailure:   observation.FreshnessFailure,
			ObservationQueries: observation.ObservationQueries,
			ObservationRows:    observation.ObservationRows,
			ObservationMillis:  observation.ObservationMillis,
		})
	}

	modelIDs := make([]string, 0, len(manifest.Models))
	for id := range manifest.Models {
		modelIDs = append(modelIDs, id)
	}
	sort.Strings(modelIDs)
	models := make([]gates.ModelInput, 0, len(modelIDs))
	for _, id := range modelIDs {
		if err := validateQualificationResourceID(id, "compiler model"); err != nil {
			return nil, nil, err
		}
		models = append(models, gates.ModelInput{ID: id, Model: semanticmodel.CloneTable(manifest.Models[id])})
	}
	return sources, models, nil
}

// nativeQualificationSourceIndex validates the compiler source identity map
// and returns canonical resource IDs plus authored runtime aliases. A source
// alias collision is rejected even when the compiler artifact should already
// have ruled it out; qualification remains fail-closed when handed a forged
// artifact value.
func nativeQualificationSourceIndex(manifest projectmanifest.Project) (map[string]string, map[string]string, error) {
	canonical := make(map[string]string, len(manifest.Sources))
	sourceIDs := make([]string, 0, len(manifest.Sources))
	for id := range manifest.Sources {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)
	for _, id := range sourceIDs {
		if err := validateQualificationResourceID(id, "compiler source"); err != nil {
			return nil, nil, err
		}
		canonical[id] = id
	}

	aliases := make(map[string]string, len(manifest.NameIndex.Sources))
	authoredNames := make([]string, 0, len(manifest.NameIndex.Sources))
	for authoredName := range manifest.NameIndex.Sources {
		authoredNames = append(authoredNames, authoredName)
	}
	sort.Strings(authoredNames)
	for _, authoredName := range authoredNames {
		id := manifest.NameIndex.Sources[authoredName]
		if err := validateQualificationText(authoredName, "authored source name"); err != nil {
			return nil, nil, err
		}
		if err := validateQualificationResourceID(id, "source name-index target"); err != nil {
			return nil, nil, err
		}
		if _, ok := manifest.Sources[id]; !ok {
			return nil, nil, fmt.Errorf("%w: source name-index target %q is unknown", ErrNativeQualificationInvalid, id)
		}
		alias := projectmanifest.RuntimeSourceAlias(authoredName)
		if _, exists := aliases[alias]; exists {
			return nil, nil, fmt.Errorf("%w: authored sources map to duplicate runtime alias %q", ErrNativeQualificationInvalid, alias)
		}
		if previous, exists := canonical[alias]; exists && previous != id {
			return nil, nil, fmt.Errorf("%w: runtime source alias %q collides with canonical source %q", ErrNativeQualificationInvalid, alias, previous)
		}
		aliases[alias] = id
	}
	return canonical, aliases, nil
}

// nativeQualificationBaseObservations converts valid base gate source
// evidence into the same detached observation shape used by live evidence.
// Base counters are intentionally omitted: they describe work performed by a
// previous generation and must not consume this candidate's gate budget.
func nativeQualificationBaseObservations(
	artifacts release.CandidateArtifactSet,
	manifestSources map[string]string,
) (map[string]analyticsmaterialize.SourceObservation, error) {
	base := artifacts.Generation.BaseGateEvidence
	if base == nil {
		if len(manifestSources) == 0 {
			return make(map[string]analyticsmaterialize.SourceObservation), nil
		}
		return nil, fmt.Errorf("%w: source observations are incomplete", ErrNativeQualificationInvalid)
	}
	canonical, err := base.Canonical()
	if err != nil {
		return nil, fmt.Errorf("%w: base gate evidence is invalid: %v", ErrNativeQualificationInvalid, err)
	}
	if canonical.Outcome != release.GateSuccess && canonical.Outcome != release.GateWarning {
		return nil, fmt.Errorf("%w: base gate evidence is not reusable (%s)", ErrNativeQualificationInvalid, canonical.Outcome)
	}
	if len(canonical.Sources) != len(manifestSources) {
		return nil, fmt.Errorf("%w: base gate source evidence is incomplete", ErrNativeQualificationInvalid)
	}
	observations := make(map[string]analyticsmaterialize.SourceObservation, len(canonical.Sources))
	for _, item := range canonical.Sources {
		if _, known := manifestSources[item.ID]; !known {
			return nil, fmt.Errorf("%w: base gate source evidence %q is unknown", ErrNativeQualificationInvalid, item.ID)
		}
		if _, duplicate := observations[item.ID]; duplicate {
			return nil, fmt.Errorf("%w: base gate source evidence contains duplicate %q", ErrNativeQualificationInvalid, item.ID)
		}
		observation := analyticsmaterialize.SourceObservation{
			ID:                item.ID,
			Schema:            cloneColumnSchemas(item.ObservedSchema),
			FreshnessObserved: item.ObservedAt,
			FreshnessEmpty:    item.FreshnessOutcome == release.GateEmpty,
			SchemaFailure:     qualificationObservationFailure(item.SchemaFailure),
			FreshnessFailure:  qualificationObservationFailure(item.FreshnessFailure),
		}
		if source, ok := artifacts.Compiler.Manifest.Sources[item.ID]; ok && source.Freshness != nil && source.Freshness.Basis == "revision" {
			observation.Revision = source.Freshness.Revision
			observation.RevisionObserved = item.ObservedAt
		}
		observations[item.ID] = observation
	}
	for id := range manifestSources {
		if _, ok := observations[id]; !ok {
			return nil, fmt.Errorf("%w: base gate source evidence %q is missing", ErrNativeQualificationInvalid, id)
		}
	}
	return observations, nil
}

func qualificationObservationFailure(value release.GateObservationFailure) analyticsmaterialize.ObservationFailure {
	switch value {
	case release.GateObservationFailureUnavailable:
		return analyticsmaterialize.ObservationUnavailable
	case release.GateObservationFailureTimeout:
		return analyticsmaterialize.ObservationTimeout
	case release.GateObservationFailureBounds:
		return analyticsmaterialize.ObservationBounds
	default:
		return ""
	}
}

func validateQualificationResourceID(value, label string) error {
	if err := validateQualificationText(value, label); err != nil {
		return err
	}
	if projectgraph.ResourceID(value).Validate() != nil {
		return fmt.Errorf("%w: %s %q is not canonical", ErrNativeQualificationInvalid, label, value)
	}
	return nil
}

func validateQualificationText(value, label string) error {
	if err := validateTextField(value, label, maxNativeObservationIDBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrNativeQualificationInvalid, err)
	}
	return nil
}

func cloneColumnSchemas(values []semanticmodel.ColumnSchema) []semanticmodel.ColumnSchema {
	if values == nil {
		return nil
	}
	clone := make([]semanticmodel.ColumnSchema, len(values))
	for i, value := range values {
		clone[i] = value
		if value.Nullable != nil {
			nullable := *value.Nullable
			clone[i].Nullable = &nullable
		}
	}
	return clone
}

func cloneQualificationSource(value semanticmodel.Source) (semanticmodel.Source, error) {
	clone := value
	if value.Fields != nil {
		clone.Fields = make(map[string]semanticmodel.SourceField, len(value.Fields))
		for name, field := range value.Fields {
			fieldCopy := field
			if field.Nullable != nil {
				nullable := *field.Nullable
				fieldCopy.Nullable = &nullable
			}
			clone.Fields[name] = fieldCopy
		}
	}
	clone.Schema.Columns = cloneColumnSchemas(value.Schema.Columns)
	if value.Freshness != nil {
		freshness := *value.Freshness
		if value.Freshness.RevisionAt != nil {
			revisionAt := *value.Freshness.RevisionAt
			freshness.RevisionAt = &revisionAt
		}
		if value.Freshness.WarningAfter != nil {
			warningAfter := *value.Freshness.WarningAfter
			freshness.WarningAfter = &warningAfter
		}
		if value.Freshness.ErrorAfter != nil {
			errorAfter := *value.Freshness.ErrorAfter
			freshness.ErrorAfter = &errorAfter
		}
		clone.Freshness = &freshness
	}
	var err error
	clone.PathLocation, err = cloneQualificationPathLocation(value.PathLocation)
	if err != nil {
		return semanticmodel.Source{}, err
	}
	clone.EffectivePathLocation, err = cloneQualificationPathLocation(value.EffectivePathLocation)
	if err != nil {
		return semanticmodel.Source{}, err
	}
	return clone, nil
}

func cloneQualificationPathLocation(value *projectcontracts.PathSourceLocation) (*projectcontracts.PathSourceLocation, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var clone projectcontracts.PathSourceLocation
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}
