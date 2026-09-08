package capabilities

import (
	"net/http"

	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

type Config struct {
	Environment   string
	BuildIdentity buildinfo.Identity
	TUS           bool
	S3Multipart   bool
	// Arrow reports whether the active query runtime can emit native Arrow
	// streams. The OpenAPI contract documents the media type, but the
	// capability response must describe this process's actual runtime.
	Arrow bool
}

func Write(w http.ResponseWriter, config Config) {
	identity := config.BuildIdentity
	if identity.Version == "" {
		identity = buildinfo.Current()
	}
	uploadProtocols := []apigenapi.UploadProtocol{}
	if config.TUS {
		uploadProtocols = append(uploadProtocols, apigenapi.UploadProtocolTus)
	}
	if config.S3Multipart {
		uploadProtocols = append(uploadProtocols, apigenapi.UploadProtocolS3Multipart)
	}
	queryFormats := []apigenapi.QueryFormat{apigenapi.QueryFormatApplicationJson}
	if config.Arrow {
		queryFormats = append(queryFormats, apigenapi.QueryFormatApplicationVndApacheArrowStream)
	}
	apitransport.WriteJSON(w, http.StatusOK, apigenapi.CapabilitiesResponse{
		ApiVersion: "v1", BuildVersion: identity.Version,
		BuildRevision: identity.Revision, BuildTime: identity.BuildTime,
		BuildDirty: identity.Dirty, BuildDevelopment: identity.Development,
		Authentication:  []apigenapi.AuthenticationMode{apigenapi.AuthenticationModeBearer},
		Environment:     config.Environment,
		DeliveryMode:    apigenapi.DeliveryModeNativePostgres,
		QueryFormats:    queryFormats,
		UploadProtocols: uploadProtocols,
		Visualization: apigenapi.VisualizationCapabilities{
			SchemaVersion: visualizationir.CurrentSchemaVersion,
			Renderers: []apigenapi.VisualizationRendererCapability{
				{Id: apigenapi.VisualizationRendererIDEcharts, Version: "6.1.0", SchemaVersion: visualizationir.CurrentSchemaVersion, Kinds: []apigenapi.VisualizationSpecKind{apigenapi.VisualizationSpecKindCartesian, apigenapi.VisualizationSpecKindPoint, apigenapi.VisualizationSpecKindProportional, apigenapi.VisualizationSpecKindHierarchy, apigenapi.VisualizationSpecKindPolar}},
				{Id: apigenapi.VisualizationRendererIDTanstack, Version: "9.0.0-beta.12", SchemaVersion: visualizationir.CurrentSchemaVersion, Kinds: []apigenapi.VisualizationSpecKind{apigenapi.VisualizationSpecKindTable, apigenapi.VisualizationSpecKindMatrix, apigenapi.VisualizationSpecKindPivot}},
				{Id: apigenapi.VisualizationRendererIDHtml, Version: "1", SchemaVersion: visualizationir.CurrentSchemaVersion, Kinds: []apigenapi.VisualizationSpecKind{apigenapi.VisualizationSpecKindKpi}},
				{Id: apigenapi.VisualizationRendererIDMaplibre, Version: "5.19.0", SchemaVersion: visualizationir.CurrentSchemaVersion, Kinds: []apigenapi.VisualizationSpecKind{apigenapi.VisualizationSpecKindGeographic}},
			},
		},
	})
}
