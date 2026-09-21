package module

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestDevelopmentProfileApplicationAPIAppliesDurableExactIntent(t *testing.T) {
	store := &profileAPIStore{}
	service, err := connectionbinding.NewProfileApplicationService(connectionbinding.ProfileApplicationServiceConfig{
		Store: store, Bindings: emptyProfileAdministration{}, NewBindingID: func() (connectionbinding.BindingID, error) { return "binding_test", nil },
		Now: func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profileDigest, err := connectionbinding.DevelopmentProfileDigest("local", nil)
	require.NoError(t, err)
	handler := developmentProfileApplicationAPIHandler{config: DevelopmentProfileApplicationAPIConfig{
		Service: service, Store: store, Enabled: true, CheckoutID: "checkout-1", RuntimeID: "runtime-1",
		ProfileName: "local", GraphDigest: digest, ProfileDigest: profileDigest, Environment: "dev", TargetID: "target-local",
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-local", true },
		Audit:            func(context.Context, string, string, string, string) error { return nil },
	}}
	body, err := json.Marshal(analyticsgen.DevelopmentProfileApplicationRequest{
		ApplicationId: "profile_test", Mode: analyticsgen.DevelopmentProfileApplicationModeNew,
		SourceDigest: digest, GraphDigest: digest, ProfileDigest: profileDigest, Connections: []analyticsgen.DevelopmentProfileConnectionIntent{},
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:test/targets/target-local/development-profile-application", bytesReader(body))
	request.Header.Set("Idempotency-Key", "0198f2c0-7c7a-7f00-8a11-000000000111")
	response := httptest.NewRecorder()
	handler.Apply(response, request, "project:test", "target-local")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result analyticsgen.DevelopmentProfileApplicationResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.NotNil(t, result.LastCompletedApplicationId)
	require.Equal(t, "profile_test", *result.LastCompletedApplicationId)
	require.Equal(t, connectionbinding.ProfileApplicationApplied, store.record.Status)
	require.Equal(t, "checkout-1", store.record.CheckoutID)
	require.Empty(t, store.record.ExpectedConnections)
}

func TestDevelopmentProfileApplicationAPIRejectsDigestPayloadMismatchBeforeMutation(t *testing.T) {
	store := &profileAPIStore{}
	service, err := connectionbinding.NewProfileApplicationService(connectionbinding.ProfileApplicationServiceConfig{
		Store: store, Bindings: emptyProfileAdministration{}, NewBindingID: func() (connectionbinding.BindingID, error) { return "binding_test", nil }, Now: time.Now,
	})
	require.NoError(t, err)
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profileDigest, err := connectionbinding.DevelopmentProfileDigest("local", nil)
	require.NoError(t, err)
	handler := developmentProfileApplicationAPIHandler{config: DevelopmentProfileApplicationAPIConfig{
		Service: service, Store: store, Enabled: true, CheckoutID: "checkout-1", RuntimeID: "runtime-1",
		ProfileName: "local", GraphDigest: digest, ProfileDigest: profileDigest, Environment: "dev", TargetID: "target-local",
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-local", true },
	}}
	body, err := json.Marshal(analyticsgen.DevelopmentProfileApplicationRequest{
		ApplicationId: "profile_test", Mode: analyticsgen.DevelopmentProfileApplicationModeNew,
		SourceDigest: digest, GraphDigest: digest, ProfileDigest: profileDigest,
		Connections: []analyticsgen.DevelopmentProfileConnectionIntent{{
			LogicalConnection: "connection:unexpected", ConnectorKind: "postgres",
			AuthenticationMode: analyticsgen.TargetConnectionAuthenticationModeNone, Endpoint: analyticsgen.TargetConnectionEndpoint{},
		}},
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/", bytesReader(body))
	request.Header.Set("Idempotency-Key", "0198f2c0-7c7a-7f00-8a11-000000000112")
	response := httptest.NewRecorder()
	handler.Apply(response, request, "project:test", "target-local")
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Empty(t, store.record.ID)
}

func TestDevelopmentProfileApplicationAPIFailsClosedWhenDisabled(t *testing.T) {
	handler := developmentProfileApplicationAPIHandler{}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.Get(response, request, "project:test", "target-local")
	require.Equal(t, http.StatusNotFound, response.Code)
}

func TestDevelopmentProfileApplicationResponseReportsOnlyRedactedProgress(t *testing.T) {
	record := profileApplicationRecordFixture(t, connectionbinding.ProfileApplicationIncomplete)
	record.AppliedConnections = record.AppliedConnections[:1]
	record.LastCompletedApplicationID = "profile-prior"
	record.LastCompletedAt = record.UpdatedAt.Add(-time.Minute)
	response := developmentProfileApplicationResponse(record)
	require.Equal(t, int32(2), response.RequiredConnectionCount)
	require.Equal(t, int32(1), response.AppliedConnectionCount)
	require.Equal(t, []string{"connection-b"}, response.IncompleteConnections)
	require.Equal(t, "local", response.ProfileName)
	require.NotNil(t, response.LastCompletedApplicationId)
	require.Equal(t, "profile-prior", *response.LastCompletedApplicationId)
	require.NotNil(t, response.LastCompletedAt)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "provider-")
	require.NotContains(t, string(encoded), "LEAPVIEW_DEV_CONNECTION")
}

func profileApplicationRecordFixture(t *testing.T, status connectionbinding.ProfileApplicationStatus) connectionbinding.ProfileApplicationRecord {
	t.Helper()
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	connections := []connectionbinding.ProfileApplicationConnection{
		{BindingID: "binding-a", ConnectionID: "connection-a", ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationNone, Endpoint: connectionbinding.EndpointConfig{}, BindingRevision: 1, ProviderVersion: connectionbinding.NoAuthProviderVersion},
		{BindingID: "binding-b", ConnectionID: "connection-b", ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationNone, Endpoint: connectionbinding.EndpointConfig{}, BindingRevision: 1, ProviderVersion: connectionbinding.NoAuthProviderVersion},
	}
	record, err := connectionbinding.NewProfileApplication(connectionbinding.ProfileApplicationRecord{
		ID: "profile-test", CheckoutID: "checkout-one", RuntimeID: "runtime-one", TargetID: "target-local",
		ProjectID: "project:one", Environment: "dev", ProfileName: "local", SourceDigest: digest, GraphDigest: digest, ProfileDigest: digest,
		RequiredConnections: []connectionbinding.ProfileApplicationRequiredConnection{{ConnectionID: "connection-a", ConnectorKind: "postgres"}, {ConnectionID: "connection-b", ConnectorKind: "postgres"}},
		ExpectedConnections: connections, AppliedConnections: connections, Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	return record
}

func bytesReader(value []byte) *bytes.Reader { return bytes.NewReader(value) }

type profileAPIStore struct {
	record connectionbinding.ProfileApplicationRecord
}

func (store *profileAPIStore) Application(context.Context, connectionbinding.ProfileApplicationScope, connectionbinding.TargetID) (connectionbinding.ProfileApplicationRecord, error) {
	if store.record.ID == "" {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationNotFound
	}
	return store.record, nil
}

func (store *profileAPIStore) Save(_ context.Context, record connectionbinding.ProfileApplicationRecord, expected int64) (connectionbinding.ProfileApplicationRecord, error) {
	if expected == 0 && store.record.ID != "" || expected != 0 && store.record.Revision != expected {
		return connectionbinding.ProfileApplicationRecord{}, connectionbinding.ErrProfileApplicationConflict
	}
	if expected != 0 {
		record.Revision = expected + 1
	}
	store.record = record
	return record, nil
}

func (store *profileAPIStore) Replace(context.Context, connectionbinding.ProfileApplicationRecord, int64) (connectionbinding.ProfileApplicationRecord, error) {
	return connectionbinding.ProfileApplicationRecord{}, errors.New("unexpected replace")
}

type emptyProfileAdministration struct{}

func (emptyProfileAdministration) List(context.Context, string, connectionbinding.BindingScope, connectionbinding.TargetID) ([]connectionbinding.TargetBinding, error) {
	return nil, nil
}
func (emptyProfileAdministration) Get(context.Context, string, connectionbinding.BindingKey) (connectionbinding.TargetBinding, error) {
	return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
}
func (emptyProfileAdministration) Create(context.Context, string, connectionbinding.TargetBindingInput) (connectionbinding.TargetBinding, error) {
	return connectionbinding.TargetBinding{}, errors.New("unexpected create")
}
func (emptyProfileAdministration) PlanConfigurationChange(context.Context, string, connectionbinding.BindingKey, connectionbinding.TargetBindingConfiguration) (connectionbinding.BindingChangePlan, error) {
	return connectionbinding.BindingChangePlan{}, errors.New("unexpected plan")
}
func (emptyProfileAdministration) UpdateConfiguration(context.Context, connectionbinding.UpdateConfigurationRequest) (connectionbinding.TargetBinding, error) {
	return connectionbinding.TargetBinding{}, errors.New("unexpected update")
}
func (emptyProfileAdministration) Test(context.Context, string, connectionbinding.BindingKey) (connectionbinding.BindingHealthStatus, error) {
	return connectionbinding.BindingHealthStatus{}, errors.New("unexpected test")
}
func (emptyProfileAdministration) Enable(context.Context, string, connectionbinding.BindingKey) (connectionbinding.TargetBinding, error) {
	return connectionbinding.TargetBinding{}, errors.New("unexpected enable")
}
func (emptyProfileAdministration) Disable(context.Context, string, connectionbinding.BindingKey) (connectionbinding.TargetBinding, error) {
	return connectionbinding.TargetBinding{}, errors.New("unexpected disable")
}
