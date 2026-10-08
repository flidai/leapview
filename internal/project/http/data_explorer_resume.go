package http

import (
	"net/http"
	"strings"

	projectview "github.com/flidai/leapview/internal/project"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
)

// Decode only the mounted-state markers and identities. Retained results and
// pending commands stay in the browser and are never replayed as server output.
type dataExplorerResumeSignals struct {
	Command *projectsignals.DataExplorerCommand `json:"dataExplorerCommand"`
	Agent   any                                 `json:"agent"`
	Page    *struct {
		AssetID       string `json:"assetId"`
		ActiveSection string `json:"activeSection"`
	} `json:"page"`
	Explorer *struct {
		SelectedObject *struct {
			Key        string `json:"key"`
			ResourceID string `json:"resourceId"`
		} `json:"selectedObject"`
		Explore struct {
			SelectedSemanticModel *struct {
				ID string `json:"id"`
			} `json:"selectedSemanticModel"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"explore"`
		Preview struct {
			Columns []any `json:"columns"`
		} `json:"preview"`
	} `json:"dataExplorer"`
}

func (s dataExplorerResumeSignals) mounted() bool {
	if s.Command == nil || s.Explorer == nil {
		return false
	}
	state := s.Explorer.Explore.Status.State
	return strings.TrimSpace(projectsignals.ValueOrZero(s.Command.ClientID)) != "" ||
		state == "success" || state == "error" || state == "cancelled" || len(s.Explorer.Preview.Columns) > 0
}

// Call only after the request's asset has been loaded through the current
// authorized catalog. These browser identities select state to preserve; they
// do not authorize an asset or supply a query to execute.
func mountedAssetDataExplorer(r *http.Request, asset projectview.DevelopAssetView) bool {
	var resumed dataExplorerResumeSignals
	if pagestream.ReadSignals(r, &resumed) != nil || !resumed.mounted() || resumed.Page == nil ||
		resumed.Page.AssetID != asset.ID || resumed.Page.ActiveSection != "data" {
		return false
	}
	command := resumed.Command
	switch asset.Type {
	case string(projectview.AssetTypeModel):
		selected := resumed.Explorer.SelectedObject
		objectKey := projectsignals.ValueOrZero(command.ObjectKey)
		return projectsignals.ValueOrZero(command.Mode) == "browse" && selected != nil && selected.ResourceID == asset.ID &&
			(objectKey == asset.ID || objectKey == selected.Key)
	case string(projectview.AssetTypeSemanticModel):
		selected := resumed.Explorer.Explore.SelectedSemanticModel
		if projectsignals.ValueOrZero(command.Mode) != "explore" || command.Explore == nil || selected == nil || selected.ID != asset.ID {
			return false
		}
		modelID := strings.TrimSpace(command.Explore.Spec.ModelID)
		legacyModelID := strings.TrimSpace(projectsignals.ValueOrZero(command.Explore.SemanticModelID))
		return (modelID == asset.ID || legacyModelID == asset.ID) &&
			(modelID == "" || modelID == asset.ID) && (legacyModelID == "" || legacyModelID == asset.ID)
	default:
		return false
	}
}
