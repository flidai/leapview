package project

import (
	"encoding/json"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

// SourceAssetReadModel is the credential-free source definition used by the
// project browser. Source payloads persisted in the serving graph contain
// only graph metadata; this projection keeps the authored source shape at the
// runtime read boundary without exposing secrets.
type SourceAssetReadModel struct {
	Format       string                               `json:"Format,omitempty"`
	Description  string                               `json:"Description,omitempty"`
	Path         string                               `json:"Path,omitempty"`
	Connection   string                               `json:"Connection,omitempty"`
	Object       string                               `json:"Object,omitempty"`
	PathLocation *projectcontracts.PathSourceLocation `json:"PathLocation,omitempty"`
	Fields       map[string]semanticmodel.SourceField `json:"Fields,omitempty"`
	Schema       semanticmodel.TableSchema            `json:"Schema,omitempty"`
}

// ConnectionAssetReadModel is the non-secret connection definition used by
// the project browser. Credentials are reduced to a boolean configured state;
// provider names, secret paths, and resolved auth values never cross this
// boundary.
type ConnectionAssetReadModel struct {
	Kind                  string                           `json:"Kind,omitempty"`
	Description           string                           `json:"Description,omitempty"`
	Path                  string                           `json:"Path,omitempty"`
	Root                  string                           `json:"Root,omitempty"`
	Scope                 string                           `json:"Scope,omitempty"`
	ReaderDefaults        *projectcontracts.ReaderDefaults `json:"ReaderDefaults,omitempty"`
	CredentialsConfigured bool                             `json:"credentials_configured"`
	CredentialsRequired   bool                             `json:"credentials_required"`
}

// RefreshPipelineScheduleReadModel carries the public schedule fields needed
// by the pipeline list/detail surfaces. The schedule's parsed cron internals
// are intentionally not serialized.
type RefreshPipelineScheduleReadModel struct {
	Cron     string `json:"Cron,omitempty"`
	Timezone string `json:"Timezone,omitempty"`
}

// RefreshPipelineAssetReadModel is the public refresh-pipeline projection.
type RefreshPipelineAssetReadModel struct {
	ID              string                             `json:"ID,omitempty"`
	Name            string                             `json:"Name,omitempty"`
	SemanticModel   string                             `json:"SemanticModel,omitempty"`
	SemanticModelID string                             `json:"SemanticModelID,omitempty"`
	Schedules       []RefreshPipelineScheduleReadModel `json:"Schedules,omitempty"`
}

func SourceAssetPayload(source semanticmodel.Source) map[string]any {
	return encodeAssetReadModel(SourceAssetReadModel{
		Format: source.Format, Description: source.Description, Path: source.Path,
		Connection: source.Connection, Object: source.Object, PathLocation: source.PathLocation,
		Fields: source.Fields, Schema: source.Schema,
	})
}

func ConnectionAssetPayload(connection semanticmodel.Connection) map[string]any {
	return encodeAssetReadModel(ConnectionAssetReadModel{
		Kind: connection.Kind, Description: connection.Description, Path: connection.Path,
		Root: connection.Root, Scope: connection.Scope, ReaderDefaults: connection.ReaderDefaults,
		CredentialsConfigured: semanticmodel.ConnectionCredentialsConfigured(connection),
		// An omitted provider is intentionally treated as requiring runtime
		// configuration. Only an explicit `none` provider proves that the
		// connection is anonymous; this avoids presenting an unresolved target
		// binding as healthy on the browser list.
		CredentialsRequired: strings.TrimSpace(connection.Credentials.Provider) != "none",
	})
}

func RefreshPipelineAssetPayload(pipeline refreshschedule.Definition) map[string]any {
	schedules := make([]RefreshPipelineScheduleReadModel, 0, len(pipeline.Schedules))
	for _, schedule := range pipeline.Schedules {
		schedules = append(schedules, RefreshPipelineScheduleReadModel{Cron: schedule.Expression, Timezone: schedule.Timezone})
	}
	return encodeAssetReadModel(RefreshPipelineAssetReadModel{
		ID: pipeline.ID.String(), Name: pipeline.Name,
		SemanticModel: pipeline.SemanticModelID.String(), SemanticModelID: pipeline.SemanticModelID.String(),
		Schedules: schedules,
	})
}

func encodeAssetReadModel(value any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil
	}
	return payload
}
