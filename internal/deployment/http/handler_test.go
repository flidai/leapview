package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment/apiadapter"
)

func TestCreateResponseUsesProjectDeploymentWireContract(t *testing.T) {
	coordinator := &fakeCoordinator{response: apiadapter.Deployment{
		ID: "deployment_1", Project: "project", Environment: "prod", GenerationID: "generation_2",
		ArtifactDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RequestDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status:         apiadapter.StatusPending, CreatedAt: "2026-07-14T10:00:00Z",
	}}
	handler := NewHandler(Options{Coordinator: coordinator, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal"}, true }})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/project/deployments", strings.NewReader(`{"environment":"prod","generationId":"generation_2","artifactDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`))
	request.Header.Set("Content-Type", "application/json")

	handler.Create(recorder, request, "project", CreateHeaders{IdempotencyKey: "deploy-1"})
	if recorder.Code != stdhttp.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["project"] != "project" || body["status"] != "pending" || body["generationId"] != "generation_2" || body["artifactDigest"] != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("response = %#v", body)
	}
}

func TestCreateRejectsEnvironmentOutsideInstanceBeforeMutation(t *testing.T) {
	coordinator := &fakeCoordinator{}
	handler := NewHandler(Options{Coordinator: coordinator, InstanceEnvironment: "prod", CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal"}, true }})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/project/deployments", strings.NewReader(`{"environment":"staging","generationId":"generation_2","artifactDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.Create(recorder, request, "project", CreateHeaders{IdempotencyKey: "deploy-1"})
	if recorder.Code != stdhttp.StatusConflict || coordinator.creates != 0 {
		t.Fatalf("status=%d creates=%d body=%s", recorder.Code, coordinator.creates, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"requestedEnvironment":"staging"`) || !strings.Contains(recorder.Body.String(), `"instanceEnvironment":"prod"`) {
		t.Fatalf("environment conflict details missing: %s", recorder.Body.String())
	}
}

func TestUnexpectedCoordinatorErrorIsGenericInternalServerError(t *testing.T) {
	handler := NewHandler(Options{
		Coordinator: &fakeCoordinator{err: errors.New("secret sqlite path /srv/leapview.db")},
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal"}, true
		},
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project/deployments/deployment_1", nil)

	handler.Get(recorder, request, "project", "deployment_1")
	if recorder.Code != stdhttp.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "sqlite") || !strings.Contains(recorder.Body.String(), "internal server error") {
		t.Fatalf("body leaked backend error: %s", recorder.Body.String())
	}
}

func TestTypedInvalidCoordinatorErrorIsBadRequest(t *testing.T) {
	handler := NewHandler(Options{
		Coordinator: &fakeCoordinator{err: apiadapter.ErrInvalid},
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal"}, true
		},
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project/deployments/deployment_1", nil)

	handler.Get(recorder, request, "project", "deployment_1")
	if recorder.Code != stdhttp.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

type fakeCoordinator struct {
	response apiadapter.Deployment
	err      error
	creates  int
}

func (c *fakeCoordinator) Create(context.Context, apiadapter.CreateRequest) (apiadapter.Deployment, error) {
	c.creates++
	return c.response, c.err
}
func (c *fakeCoordinator) Get(context.Context, apiadapter.Scope) (apiadapter.Deployment, error) {
	return c.response, c.err
}
func (c *fakeCoordinator) Activate(context.Context, apiadapter.ActivateRequest) (apiadapter.Deployment, error) {
	return c.response, c.err
}
func (c *fakeCoordinator) Cancel(context.Context, apiadapter.Scope) (apiadapter.Deployment, error) {
	return c.response, c.err
}
