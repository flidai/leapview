package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestListRolesReturnsCanonicalCatalogWithoutRepository(t *testing.T) {
	handler := Handler{}
	response := httptest.NewRecorder()
	handler.ListRoles(response, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/roles", nil))

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, stdhttp.StatusOK, response.Body.String())
	}
	var body struct {
		Items []struct {
			Name         string              `json:"name"`
			Capabilities []access.Capability `json:"capabilities"`
		} `json:"items"`
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	roles := access.CanonicalProjectRoles()
	if len(body.Items) != len(roles) {
		t.Fatalf("item count = %d, want %d", len(body.Items), len(roles))
	}
	for index, role := range roles {
		if body.Items[index].Name != string(role) {
			t.Errorf("item[%d].name = %q, want %q", index, body.Items[index].Name, role)
		}
		if want := access.ProjectRoleCapabilities(role); !reflect.DeepEqual(body.Items[index].Capabilities, want) {
			t.Errorf("item[%d].capabilities = %#v, want %#v", index, body.Items[index].Capabilities, want)
		}
	}
	if body.Page.NextCursor != "" {
		t.Fatalf("unpaginated nextCursor = %q, want empty", body.Page.NextCursor)
	}
}

func TestListRolesUsesDeterministicCursorPagination(t *testing.T) {
	handler := Handler{}
	roles := access.CanonicalProjectRoles()
	if len(roles) < 3 {
		t.Fatalf("canonical role catalog has %d roles, want at least 3", len(roles))
	}

	firstResponse := httptest.NewRecorder()
	handler.ListRoles(firstResponse, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/roles?limit=2", nil))
	if firstResponse.Code != stdhttp.StatusOK {
		t.Fatalf("first status = %d, want %d; body=%s", firstResponse.Code, stdhttp.StatusOK, firstResponse.Body.String())
	}
	var first struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if got, want := len(first.Items), 2; got != want {
		t.Fatalf("first item count = %d, want %d", got, want)
	}
	if first.Page.NextCursor == "" {
		t.Fatal("first nextCursor is empty")
	}

	secondResponse := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/roles?limit=2&pageToken="+first.Page.NextCursor, nil)
	handler.ListRoles(secondResponse, secondRequest)
	if secondResponse.Code != stdhttp.StatusOK {
		t.Fatalf("second status = %d, want %d; body=%s", secondResponse.Code, stdhttp.StatusOK, secondResponse.Body.String())
	}
	var second struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if got, want := len(second.Items), 2; got != want {
		t.Fatalf("second item count = %d, want %d", got, want)
	}
	if second.Items[0].Name != string(roles[2]) || second.Items[1].Name != string(roles[3]) {
		t.Fatalf("second names = %#v, want [%q %q]", second.Items, roles[2], roles[3])
	}

	// Role ordering and cursor encoding are stable across equivalent requests.
	repeatResponse := httptest.NewRecorder()
	handler.ListRoles(repeatResponse, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/roles?limit=2", nil))
	var repeat struct {
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(repeatResponse.Body.Bytes(), &repeat); err != nil {
		t.Fatalf("decode repeated response: %v", err)
	}
	if repeat.Page.NextCursor != first.Page.NextCursor {
		t.Fatalf("repeated nextCursor = %q, want %q", repeat.Page.NextCursor, first.Page.NextCursor)
	}
}

func TestListRolesRejectsMalformedCursor(t *testing.T) {
	response := httptest.NewRecorder()
	(Handler{}).ListRoles(response, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/roles?pageToken=not-a-cursor", nil))
	if response.Code != stdhttp.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, stdhttp.StatusBadRequest, response.Body.String())
	}
}
