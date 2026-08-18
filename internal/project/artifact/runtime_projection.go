package artifact

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"github.com/flidai/leapview/internal/project/manifest"
)

// runtimeProjection is the artifact-owned private payload for fields that are
// deliberately omitted by semanticmodel's generic JSON contract. Keeping the
// payload to those fields makes canonical bytes stable before and after decode:
// every other manifest field has exactly one JSON representation.
type runtimeProjection struct {
	Sources        map[string]runtimeSourceProjection           `json:"sources"`
	Models         map[string]semanticmodel.ExecutionDefinition `json:"models"`
	SemanticModels map[string]runtimeModelProjection            `json:"semanticModels"`
}

type runtimeModelProjection struct {
	Sources map[string]runtimeSourceProjection           `json:"sources"`
	Tables  map[string]semanticmodel.ExecutionDefinition `json:"tables"`
}

type runtimeSourceProjection struct {
	PathLocation          *projectcontracts.PathSourceLocation `json:"pathLocation,omitempty"`
	EffectivePathLocation *projectcontracts.PathSourceLocation `json:"effectivePathLocation,omitempty"`
}

func prepareRuntimeProjection(value manifest.Project) (manifest.Project, runtimeProjection, error) {
	if err := validatePortableConnections(value); err != nil {
		return manifest.Project{}, runtimeProjection{}, err
	}
	portable := cloneValue(value)
	projection := runtimeProjection{
		Sources:        make(map[string]runtimeSourceProjection, len(value.Sources)),
		Models:         make(map[string]semanticmodel.ExecutionDefinition, len(value.Models)),
		SemanticModels: make(map[string]runtimeModelProjection, len(value.SemanticModels)),
	}
	for id, source := range value.Sources {
		runtime, err := runtimeSourceFromModel(source)
		if err != nil {
			return manifest.Project{}, runtimeProjection{}, fmt.Errorf("source %q: %w", id, err)
		}
		if err := validateRuntimeSource("source "+id, source, runtime); err != nil {
			return manifest.Project{}, runtimeProjection{}, err
		}
		projection.Sources[id] = runtime
	}
	for id, table := range value.Models {
		if err := validateRuntimeExecution("model "+id, table.Execution); err != nil {
			return manifest.Project{}, runtimeProjection{}, err
		}
		projection.Models[id] = table.Execution
	}
	for id, model := range value.SemanticModels {
		if model == nil {
			projection.SemanticModels[id] = runtimeModelProjection{}
			continue
		}
		runtime, err := runtimeModelFromModel(model)
		if err != nil {
			return manifest.Project{}, runtimeProjection{}, fmt.Errorf("semantic model %q: %w", id, err)
		}
		projection.SemanticModels[id] = runtime
	}
	return portable, projection, nil
}

func runtimeModelFromModel(value *semanticmodel.Model) (runtimeModelProjection, error) {
	result := runtimeModelProjection{
		Sources: make(map[string]runtimeSourceProjection, len(value.Sources)),
		Tables:  make(map[string]semanticmodel.ExecutionDefinition, len(value.Tables)),
	}
	for name, source := range value.Sources {
		runtime, err := runtimeSourceFromModel(source)
		if err != nil {
			return runtimeModelProjection{}, fmt.Errorf("source %q: %w", name, err)
		}
		if err := validateRuntimeSource("source "+name, source, runtime); err != nil {
			return runtimeModelProjection{}, err
		}
		result.Sources[name] = runtime
	}
	for name, table := range value.Tables {
		if err := validateRuntimeExecution("semantic model table "+name, table.Execution); err != nil {
			return runtimeModelProjection{}, err
		}
		result.Tables[name] = table.Execution
	}
	return result, nil
}

func runtimeSourceFromModel(value semanticmodel.Source) (runtimeSourceProjection, error) {
	model := &semanticmodel.Model{Sources: map[string]semanticmodel.Source{"source": value}}
	snapshot, err := model.RuntimeSnapshot()
	if err != nil {
		return runtimeSourceProjection{}, err
	}
	value = snapshot.Sources["source"]
	return runtimeSourceProjection{PathLocation: value.PathLocation, EffectivePathLocation: value.EffectivePathLocation}, nil
}

func validatePortableConnections(value manifest.Project) error {
	for id, connection := range value.Connections {
		if err := validatePortableConnection(id, connection); err != nil {
			return err
		}
	}
	for modelID, model := range value.SemanticModels {
		if model == nil {
			continue
		}
		for id, connection := range model.Connections {
			if err := validatePortableConnection(modelID+"."+id, connection); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePortableConnection(id string, value semanticmodel.Connection) error {
	if value.Auth != nil || value.Credentials != (semanticmodel.ConnectionCredentials{}) ||
		value.Host != "" || value.Port != 0 || value.Database != "" || value.Username != "" || value.SSLMode != "" ||
		value.RuntimeOptions != (semanticmodel.ConnectionRuntimeOptions{}) || value.Path != "" || value.Root != "" || value.Scope != "" {
		return fmt.Errorf("connection %q contains target-owned state", id)
	}
	return nil
}

func applyRuntimeProjection(value *manifest.Project, projection runtimeProjection) error {
	if value == nil {
		return fmt.Errorf("manifest is required")
	}
	if err := validateRuntimeProjectionCoverage(*value, projection); err != nil {
		return err
	}
	for id, source := range projection.Sources {
		current := value.Sources[id]
		if err := validateRuntimeSource("source "+id, current, source); err != nil {
			return err
		}
		applyRuntimeSource(&current, source)
		value.Sources[id] = current
	}
	for id, execution := range projection.Models {
		if err := validateRuntimeExecution("model "+id, execution); err != nil {
			return err
		}
		current := value.Models[id]
		current.Execution = execution
		value.Models[id] = current
	}
	for id, modelProjection := range projection.SemanticModels {
		model := value.SemanticModels[id]
		if model == nil {
			continue
		}
		for name, sourceProjection := range modelProjection.Sources {
			current := model.Sources[name]
			if err := validateRuntimeSource("semantic model "+id+" source "+name, current, sourceProjection); err != nil {
				return err
			}
			applyRuntimeSource(&current, sourceProjection)
			model.Sources[name] = current
		}
		for name, execution := range modelProjection.Tables {
			if err := validateRuntimeExecution("semantic model "+id+" table "+name, execution); err != nil {
				return err
			}
			current := model.Tables[name]
			current.Execution = execution
			model.Tables[name] = current
		}
	}
	return nil
}

func validateRuntimeExecution(scope string, execution semanticmodel.ExecutionDefinition) error {
	hasSource := strings.TrimSpace(execution.Source) != ""
	hasSQL := strings.TrimSpace(execution.SQL) != ""
	if hasSource == hasSQL {
		return fmt.Errorf("%s execution must contain exactly one of source or sql", scope)
	}
	return nil
}

func validateRuntimeSource(scope string, source semanticmodel.Source, projection runtimeSourceProjection) error {
	pathBacked := strings.EqualFold(strings.TrimSpace(source.LocationType), "path") || strings.TrimSpace(source.Path) != ""
	if pathBacked {
		if projection.PathLocation == nil || projection.EffectivePathLocation == nil {
			return fmt.Errorf("%s path source requires path and effective path locations", scope)
		}
		return nil
	}
	if projection.PathLocation != nil || projection.EffectivePathLocation != nil {
		return fmt.Errorf("%s relation source cannot carry path locations", scope)
	}
	return nil
}

func validateRuntimeProjectionCoverage(value manifest.Project, projection runtimeProjection) error {
	if projection.Sources == nil || projection.Models == nil || projection.SemanticModels == nil {
		return fmt.Errorf("runtime projection is required")
	}
	if err := exactKeys("sources", value.Sources, projection.Sources); err != nil {
		return err
	}
	if err := exactKeys("models", value.Models, projection.Models); err != nil {
		return err
	}
	if err := exactKeys("semanticModels", value.SemanticModels, projection.SemanticModels); err != nil {
		return err
	}
	for id, model := range value.SemanticModels {
		projectionModel := projection.SemanticModels[id]
		if model == nil {
			if len(projectionModel.Sources) != 0 || len(projectionModel.Tables) != 0 {
				return fmt.Errorf("runtime semantic model %q must be empty for nil model", id)
			}
			continue
		}
		if err := exactKeys("semantic model "+id+" sources", model.Sources, projectionModel.Sources); err != nil {
			return err
		}
		if err := exactKeys("semantic model "+id+" tables", model.Tables, projectionModel.Tables); err != nil {
			return err
		}
	}
	return nil
}

func exactKeys[T any, U any](scope string, values map[string]T, overlays map[string]U) error {
	if len(values) != len(overlays) {
		return fmt.Errorf("runtime projection %s key set does not match manifest", scope)
	}
	for key := range values {
		if _, ok := overlays[key]; !ok {
			return fmt.Errorf("runtime projection %s is missing key %q", scope, key)
		}
	}
	return nil
}

func applyRuntimeSource(value *semanticmodel.Source, projection runtimeSourceProjection) {
	value.PathLocation = projection.PathLocation
	value.EffectivePathLocation = projection.EffectivePathLocation
}

func cloneRuntimeModels(values map[string]*semanticmodel.Model) map[string]*semanticmodel.Model {
	if values == nil {
		return nil
	}
	clone := make(map[string]*semanticmodel.Model, len(values))
	for id, value := range values {
		if value == nil {
			clone[id] = nil
			continue
		}
		copy, err := value.RuntimeSnapshot()
		if err != nil {
			panic(fmt.Sprintf("clone artifact runtime model %q: %v", id, err))
		}
		clone[id] = copy
	}
	return clone
}

func cloneRuntimeTables(values map[string]semanticmodel.Table) map[string]semanticmodel.Table {
	if values == nil {
		return nil
	}
	clone := make(map[string]semanticmodel.Table, len(values))
	for id, value := range values {
		clone[id] = semanticmodel.CloneTable(value)
	}
	return clone
}
