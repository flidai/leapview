package app

import (
	"net/http"

	apicapabilities "github.com/flidai/leapview/internal/app/api/capabilities"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
)

func (a apiGenDispatcher) GetCapabilities(w http.ResponseWriter, _ *http.Request) {
	apicapabilities.Write(w, apicapabilities.Config{
		Environment:   a.defaultEnvironment,
		BuildIdentity: a.buildIdentity,
		TUS:           a.managedDataTus != nil,
		S3Multipart:   a.managedDataModule != nil && a.managedDataModule.SupportsS3Multipart(),
		Arrow:         a.arrowQueries,
	})
}

func supportsNativeArrow(metrics QueryMetrics) bool {
	return dashboardmodule.SupportsNativeArrow(metrics)
}
