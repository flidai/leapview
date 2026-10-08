package settings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesshttp "github.com/flidai/leapview/internal/access/http"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestApplyAccessAdministrationCommandRejectsConcurrentPrincipalRevision(t *testing.T) {
	for _, surface := range []string{"admin", "REST", "self", "service"} {
		t.Run(surface, func(t *testing.T) { testConcurrentPrincipalRevision(t, surface) })
	}
}

func testConcurrentPrincipalRevision(t *testing.T, surface string) {
	t.Helper()
	ctx := t.Context()
	pool := postgrestest.Open(t, accesspostgres.ApplySchema)
	config := accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("admin-settings-test-key", 2))}
	repository, err := accesspostgres.NewAccess(pool, config)
	require.NoError(t, err)
	actor, err := repository.UpsertPrincipal(ctx, access.PrincipalInput{Email: "admin@example.com", DisplayName: "Admin"})
	require.NoError(t, err)
	_, err = repository.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: actor.ID, Email: actor.Email, DisplayName: actor.DisplayName, Role: access.PlatformRoleAdmin})
	require.NoError(t, err)
	createPrincipal := func(email, name string) (access.Principal, error) {
		if surface == "service" {
			return repository.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: name})
		}
		user, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: email, DisplayName: name})
		return user.Principal, err
	}
	target, err := createPrincipal("target@example.com", "Target")
	require.NoError(t, err)
	control, err := createPrincipal("control@example.com", "Control")
	require.NoError(t, err)
	initial, err := repository.PrincipalByID(ctx, target.ID)
	require.NoError(t, err)
	controlBefore, err := repository.PrincipalByID(ctx, control.ID)
	require.NoError(t, err)
	actorID, action, resourceKind := actor.ID, "principal.updated", "principal"
	capability := access.CapabilityProjectAdmin
	if surface != "admin" {
		capability = ""
	}
	if surface == "self" {
		actorID, action = target.ID, "principal.profile.updated"
	} else if surface == "service" {
		action, resourceKind = "service_principal.updated", "service_principal"
	}
	handlerFor := func(writer *accesspostgres.Repository) accesshttp.Handler {
		return accesshttp.Handler{
			Repository: func() (access.Repository, error) { return writer, nil },
			CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
				return []access.Capability{access.CapabilityProjectAdmin}, nil
			},
			CurrentPrincipal: func(*http.Request) (accesshttp.Principal, bool) {
				return accesshttp.Principal{ID: actorID, Kind: access.PrincipalKindUser}, true
			},
		}
	}
	loadRevision := func(principal access.Principal) (string, error) {
		if surface == "service" {
			router := chi.NewRouter()
			router.Get("/api/v1/service-principals/{servicePrincipal}", handlerFor(repository).GetServicePrincipal)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/service-principals/"+principal.ID, nil).WithContext(ctx))
			if response.Code != http.StatusOK || response.Header().Get("ETag") == "" {
				return "", fmt.Errorf("service profile revision: status=%d body=%s", response.Code, response.Body.String())
			}
			return response.Header().Get("ETag"), nil
		}
		if surface != "self" {
			return access.PrincipalRevision(principal)
		}
		response := httptest.NewRecorder()
		handlerFor(repository).GetCurrentPrincipal(response, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil).WithContext(ctx))
		if response.Code != http.StatusOK || response.Header().Get("ETag") == "" {
			return "", fmt.Errorf("self profile revision: status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Header().Get("ETag"), nil
	}
	revision, err := loadRevision(initial)
	require.NoError(t, err)
	_, err = access.PrincipalForMutation(ctx, repository, target.ID)
	require.ErrorContains(t, err, "caller-owned transaction", "a pool read must not claim to retain a revision lock")
	staleRevision := errors.New("stale profile revision")
	invoke := func(ctx context.Context, writer *accesspostgres.Repository, name, revision string) error {
		if surface == "admin" {
			result, err := ApplyAccessAdministrationCommand(ctx, writer, actorID, AccessAdministrationCommand{
				Action: "update_principal", PrincipalID: target.ID, DisplayName: name, Revision: revision,
			})
			if err != nil && strings.Contains(err.Error(), "principal changed; refresh and try again") {
				return staleRevision
			}
			if err == nil && result.Message != "Principal updated." {
				return fmt.Errorf("unexpected admin result: %#v", result)
			}
			return err
		}
		operation, path := "updatePrincipal", "/api/v1/principals/"+target.ID
		targetValues := map[string]string{"principal": target.ID}
		handler := handlerFor(writer)
		router := chi.NewRouter()
		if surface == "self" {
			operation, path = "updateCurrentPrincipal", "/api/v1/me"
			router.Patch(path, handler.UpdateCurrentPrincipal)
		} else if surface == "service" {
			operation, path = "updateServicePrincipal", "/api/v1/service-principals/"+target.ID
			targetValues = map[string]string{"servicePrincipal": target.ID}
			router.Patch("/api/v1/service-principals/{servicePrincipal}", handler.UpdateServicePrincipal)
		} else {
			router.Patch("/api/v1/principals/{principal}", handler.UpdatePrincipal)
		}
		contract, ok := accessgen.GetAPIGenCommandRuntimeContract(operation)
		if !ok {
			return fmt.Errorf("missing command contract: %s", operation)
		}
		commandContext, guard, err := apigencommand.BeginInvocation(ctx, contract, apigencommand.Invocation{
			OperationID: operation, Surface: apigencommand.SurfaceAPI,
			TargetValues: targetValues, ConcurrencyToken: revision,
		})
		if err != nil {
			return err
		}
		request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(fmt.Sprintf(`{"displayName":%q}`, name))).WithContext(commandContext)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("If-Match", revision)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code == http.StatusPreconditionFailed {
			return staleRevision
		}
		if response.Code != http.StatusOK || !guard.Completed() {
			return fmt.Errorf("profile command: status=%d completed=%t body=%s", response.Code, guard.Completed(), response.Body.String())
		}
		return nil
	}

	first, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer first.Release()
	second, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer second.Release()
	firstRepository, err := accesspostgres.NewAccess(first.Conn(), config)
	require.NoError(t, err)
	secondRepository, err := accesspostgres.NewAccess(second.Conn(), config)
	require.NoError(t, err)
	pids := []int32{int32(first.Conn().PgConn().PID()), int32(second.Conn().PgConn().PID())}
	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var lockedID string
	err = blocker.QueryRow(ctx, `SELECT id::text FROM access.principal WHERE id=$1::uuid FOR UPDATE`, target.ID).Scan(&lockedID)
	require.NoError(t, err)
	require.Equal(t, target.ID, lockedID)

	type outcome struct {
		name string
		err  error
	}
	outcomes := make(chan outcome, 2)
	writeContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	var writers sync.WaitGroup
	// Release the fixture lock and join both writers before releasing their
	// connections, including when readiness or an assertion fails.
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		writers.Wait()
	}()
	for i, writer := range []*accesspostgres.Repository{firstRepository, secondRepository} {
		name := []string{"First writer", "Second writer"}[i]
		writers.Add(1)
		go func() {
			defer writers.Done()
			outcomes <- outcome{name: name, err: invoke(writeContext, writer, name, revision)}
		}()
	}
	// Observe both production transactions actually waiting in PostgreSQL;
	// elapsed time or a goroutine-start signal is not a readiness oracle.
	readinessContext, stopReadiness := context.WithTimeout(ctx, 5*time.Second)
	defer stopReadiness()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		err = blocker.QueryRow(readinessContext, `
			SELECT count(*) FROM unnest($1::int[]) AS pid
			WHERE cardinality(pg_blocking_pids(pid)) > 0`, pids).Scan(&waiting)
		require.NoError(t, err)
		if waiting == 2 {
			t.Log("both profile writers reached a PostgreSQL lock wait")
			break
		}
		select {
		case value := <-outcomes:
			t.Fatalf("profile writer %q completed before lock readiness: %v", value.name, value.err)
		default:
		}
		select {
		case <-readinessContext.Done():
			t.Fatalf("both profile writers did not reach lock waits: %v", readinessContext.Err())
		case <-ticker.C:
		}
	}
	require.NoError(t, blocker.Commit(ctx))

	var successes, stale int
	var winner string
	for range 2 {
		select {
		case value := <-outcomes:
			if value.err == nil {
				successes++
				winner = value.name
			} else {
				require.ErrorIs(t, value.err, staleRevision)
				stale++
			}
		case <-writeContext.Done():
			t.Fatalf("profile writers did not finish: %v", writeContext.Err())
		}
	}
	stored, err := repository.PrincipalByID(ctx, target.ID)
	require.NoError(t, err)
	filter := access.AuditEventFilter{IncludeUnscoped: true, PrincipalID: actorID, Action: action, ResourceKind: resourceKind, ResourceID: target.ID}
	events, err := repository.ListAuditEvents(ctx, filter)
	require.NoError(t, err)
	t.Logf("profile outcomes: successes=%d stale=%d audits=%d finalName=%q", successes, stale, len(events), stored.DisplayName)
	require.Equal(t, 1, successes, "one initial revision must authorize only one concurrent profile write")
	require.Equal(t, 1, stale)
	require.Equal(t, winner, stored.DisplayName)
	require.Equal(t, initial.Kind, stored.Kind)
	require.Len(t, events, 1)
	require.Equal(t, "success", events[0].Status)
	require.Equal(t, capability, events[0].Capability)
	unchanged, err := repository.PrincipalByID(ctx, control.ID)
	require.NoError(t, err)
	require.Equal(t, controlBefore, unchanged)

	freshRevision, err := loadRevision(stored)
	require.NoError(t, err)
	require.NoError(t, invoke(ctx, repository, "Recovered", freshRevision))
	recovered, err := repository.PrincipalByID(ctx, target.ID)
	require.NoError(t, err)
	require.Equal(t, "Recovered", recovered.DisplayName)
	events, err = repository.ListAuditEvents(ctx, filter)
	require.NoError(t, err)
	require.Len(t, events, 2)
}
