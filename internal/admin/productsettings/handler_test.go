package productsettings

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/admin/product"
	"github.com/flidai/leapview/internal/platform"
)

func TestHandlerBootstrapAndManagePlatformGuard(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := product.NewLegacySQLite(store.SQLDB(), emptyBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HTTPConfig{ReadModel: ReadModel{Service: service}, CanManage: func(*http.Request) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	signal, err := handler.Bootstrap(request, "authentication")
	if err != nil {
		t.Fatal(err)
	}
	if signal.Active != "authentication" || signal.CanManage {
		t.Fatalf("bootstrap = %#v", signal)
	}
	response := httptest.NewRecorder()
	handler.Command(response, httptest.NewRequest(http.MethodPost, "/admin/settings/command", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("guard status = %d body=%s", response.Code, response.Body.String())
	}
}

type emptyBlobs struct{}

func (emptyBlobs) Put(context.Context, product.Blob, io.Reader) (product.Blob, error) {
	return product.Blob{}, nil
}
func (emptyBlobs) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, product.ErrNotFound
}
