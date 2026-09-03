package productsettings

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerBootstrapAndManagePlatformGuard(t *testing.T) {
	service, err := testProductService()
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
