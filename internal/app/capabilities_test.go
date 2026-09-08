package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
)

func TestCapabilitiesReportOnlyEnabledUploadProtocols(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{
		DefaultEnvironment: "prod",
		Assets:             staticasset.New(staticasset.Config{Version: "asset-cache-identity"}),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	rec := httptest.NewRecorder()
	apiGenDispatcherForTest(server).GetCapabilities(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response apigenapi.CapabilitiesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Environment != "prod" || response.DeliveryMode != apigenapi.DeliveryModeNativePostgres || len(response.UploadProtocols) != 0 {
		t.Fatalf("capabilities = %#v", response)
	}
	if response.BuildVersion != buildinfo.DevelopmentVersion ||
		response.BuildVersion == "asset-cache-identity" ||
		response.BuildRevision == "" ||
		response.BuildTime == "" ||
		!response.BuildDevelopment {
		t.Fatalf("capabilities build identity = %#v", response)
	}
	if response.BuildRevision != buildinfo.UnknownValue && len(response.BuildRevision) != 40 {
		t.Fatalf("capabilities build revision = %q", response.BuildRevision)
	}
	if response.BuildRevision != buildinfo.UnknownValue && strings.Trim(response.BuildRevision, "0123456789abcdef") != "" {
		t.Fatalf("capabilities build revision is not hexadecimal: %q", response.BuildRevision)
	}
	if response.Visualization.SchemaVersion != visualizationir.CurrentSchemaVersion || len(response.Visualization.Renderers) != 4 {
		t.Fatalf("visualization capabilities=%#v", response.Visualization)
	}
	for _, renderer := range response.Visualization.Renderers {
		if renderer.SchemaVersion != response.Visualization.SchemaVersion {
			t.Fatalf("renderer schema version=%d, want %d: %#v", renderer.SchemaVersion, response.Visualization.SchemaVersion, renderer)
		}
	}
}

func TestCapabilitiesRequireAuthenticationWithoutWorkspaceAuthorization(t *testing.T) {
	contract, ok := apigenapi.GetAPIGenOperationContracts()["getCapabilities"]
	if !ok {
		t.Fatal("getCapabilities contract is missing")
	}
	if !contract.Protected || contract.AuthzMode != "authenticated" {
		t.Fatalf("getCapabilities authorization = protected:%t mode:%q, want authenticated", contract.Protected, contract.AuthzMode)
	}
}
