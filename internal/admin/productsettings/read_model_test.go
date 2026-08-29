package productsettings

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/admin/product"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
)

func TestSignalProjectsIdentityAndRedactedStatus(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := product.NewLegacySQLite(store.SQLDB(), &testBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	model := ReadModel{Service: service, Status: product.Status{
		Authentication: product.AuthenticationStatus{BrowserEnabled: true, ManagedBy: "deployment", OIDC: product.NamedAvailability{Available: true, Enabled: true, Provider: "corporate"}},
		API:            product.APIStatus{MCP: product.Availability{Available: true, Enabled: true}},
		System:         product.SystemStatus{InstanceID: "lvinst_test", CanonicalOrigin: "https://example.test", Environment: "production", Build: buildinfo.Identity{Version: "1.2.3"}, StorageBackend: "s3", Agent: product.AgentStatus{Available: true, Configured: true}, Limits: product.Limits{QueryResultMaxRows: 100}},
	}, ControlPlane: ping{}}
	data, err := model.Data(t.Context(), "system", true)
	if err != nil {
		t.Fatal(err)
	}
	signal := Signal(data)
	if signal.General.DisplayName != "LeapView" || signal.General.Revision != 1 || signal.General.InstanceID != "lvinst_test" {
		t.Fatalf("general = %#v", signal.General)
	}
	if !signal.CanManage || signal.Authentication.ManagedBy != "deployment" || signal.System.Runtime.Health != "healthy" {
		t.Fatalf("signal = %#v", signal)
	}
	if signal.API.Mcp.Enabled != true || signal.System.Limits.QueryResultMaxRows != 100 {
		t.Fatalf("api/system = %#v %#v", signal.API, signal.System)
	}
}

func TestPayloadExplicitlyClearsRemovedLogo(t *testing.T) {
	payload, err := json.Marshal(Payload(Signal(Data{})))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"logo":null`) {
		t.Fatalf("payload = %s, want explicit null logo", payload)
	}
}

func TestReadModelMarksControlPlaneUnavailable(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := product.NewLegacySQLite(store.SQLDB(), &testBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := (ReadModel{Service: service, ControlPlane: ping{err: context.Canceled}}).Data(t.Context(), "general", false)
	if err != nil {
		t.Fatal(err)
	}
	if data.Status.System.ControlPlane != "unavailable" || Signal(data).System.Runtime.Health != "degraded" {
		t.Fatalf("status = %#v signal=%#v", data.Status.System, Signal(data).System.Runtime)
	}
}

type ping struct{ err error }

func (p ping) Ping(context.Context) error { return p.err }

type testBlobs struct{}

func (testBlobs) Put(context.Context, product.Blob, io.Reader) (product.Blob, error) {
	return product.Blob{}, nil
}

func (testBlobs) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, product.ErrNotFound
}
