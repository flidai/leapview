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
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestApplyAccessAdministrationCommandRejectsConcurrentGroupRevision(t *testing.T) {
	for _, surface := range []string{"admin", "REST"} {
		t.Run(surface, func(t *testing.T) { testConcurrentGroupRevision(t, surface) })
	}
}

func testConcurrentGroupRevision(t *testing.T, surface string) {
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
	target, err := repository.UpsertGroup(ctx, access.GroupInput{Provider: "local", Name: "Target"})
	require.NoError(t, err)
	control, err := repository.UpsertGroup(ctx, access.GroupInput{Provider: "local", Name: "Control"})
	require.NoError(t, err)
	readGroup := func(id string) (access.Group, error) {
		groups, err := repository.ListGroups(ctx)
		if err != nil {
			return access.Group{}, err
		}
		for _, group := range groups {
			if group.ID == id {
				return group, nil
			}
		}
		return access.Group{}, fmt.Errorf("fixture group %q missing", id)
	}
	handlerFor := func(writer *accesspostgres.Repository) accesshttp.Handler {
		return accesshttp.Handler{
			Repository: func() (access.Repository, error) { return writer, nil },
			CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
				return []access.Capability{access.CapabilityProjectAdmin}, nil
			},
			CurrentPrincipal: func(*http.Request) (accesshttp.Principal, bool) {
				return accesshttp.Principal{ID: actor.ID, Kind: access.PrincipalKindUser}, true
			},
		}
	}
	loadRevision := func(group access.Group) (string, error) {
		if surface == "admin" {
			return access.GroupRevision(group)
		}
		router := chi.NewRouter()
		router.Get("/api/v1/groups/{group}", handlerFor(repository).GetGroup)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/groups/"+group.ID, nil).WithContext(ctx))
		if response.Code != http.StatusOK || response.Header().Get("ETag") == "" {
			return "", fmt.Errorf("group revision: status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Header().Get("ETag"), nil
	}
	revision, err := loadRevision(target)
	require.NoError(t, err)
	staleRevision := errors.New("stale group revision")
	invoke := func(ctx context.Context, writer *accesspostgres.Repository, name, revision string) error {
		if surface == "admin" {
			result, err := ApplyAccessAdministrationCommand(ctx, writer, actor.ID, AccessAdministrationCommand{
				Action: "update_group", GroupID: target.ID, DisplayName: name, Revision: revision,
			})
			if err != nil && strings.Contains(err.Error(), "group changed; refresh and try again") {
				return staleRevision
			}
			if err == nil && result.Message != "Group updated." {
				return fmt.Errorf("unexpected admin result: %#v", result)
			}
			return err
		}
		operation := "updateGroup"
		contract, ok := accessgen.GetAPIGenCommandRuntimeContract(operation)
		if !ok {
			return fmt.Errorf("missing command contract: %s", operation)
		}
		commandContext, guard, err := apigencommand.BeginInvocation(ctx, contract, apigencommand.Invocation{
			OperationID: operation, Surface: apigencommand.SurfaceAPI,
			TargetValues: map[string]string{"group": target.ID}, ConcurrencyToken: revision,
		})
		if err != nil {
			return err
		}
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/groups/"+target.ID, strings.NewReader(fmt.Sprintf(`{"displayName":%q}`, name))).WithContext(commandContext)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("If-Match", revision)
		response := httptest.NewRecorder()
		router := chi.NewRouter()
		router.Patch("/api/v1/groups/{group}", handlerFor(writer).UpdateGroup)
		router.ServeHTTP(response, request)
		if response.Code == http.StatusPreconditionFailed {
			return staleRevision
		}
		if response.Code != http.StatusOK || !guard.Completed() {
			return fmt.Errorf("group command: status=%d completed=%t body=%s", response.Code, guard.Completed(), response.Body.String())
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
	err = blocker.QueryRow(ctx, `SELECT id::text FROM access.access_group WHERE id=$1::uuid FOR UPDATE`, target.ID).Scan(&lockedID)
	require.NoError(t, err)
	require.Equal(t, target.ID, lockedID)

	type outcome struct {
		name string
		err  error
	}
	outcomes := make(chan outcome, 2)
	writeContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	var writers sync.WaitGroup
	// On every failure path, release the row gate and join the writers before
	// returning their connections to the pool.
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
			t.Log("both group writers reached a PostgreSQL lock wait")
			break
		}
		select {
		case value := <-outcomes:
			t.Fatalf("group writer %q completed before lock readiness: %v", value.name, value.err)
		default:
		}
		select {
		case <-readinessContext.Done():
			t.Fatalf("both group writers did not reach lock waits: %v", readinessContext.Err())
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
			t.Fatalf("group writers did not finish: %v", writeContext.Err())
		}
	}
	stored, err := readGroup(target.ID)
	require.NoError(t, err)
	filter := access.AuditEventFilter{IncludeUnscoped: true, PrincipalID: actor.ID, Action: "group.updated", ResourceKind: "group", ResourceID: target.ID}
	events, err := repository.ListAuditEvents(ctx, filter)
	require.NoError(t, err)
	t.Logf("group outcomes: successes=%d stale=%d audits=%d finalName=%q", successes, stale, len(events), stored.Name)
	require.Equal(t, 1, successes, "one initial revision must authorize only one concurrent group rename")
	require.Equal(t, 1, stale)
	require.Equal(t, winner, stored.Name)
	require.Equal(t, target.Provider, stored.Provider)
	require.Equal(t, target.ExternalID, stored.ExternalID)
	require.Len(t, events, 1)
	require.Equal(t, "success", events[0].Status)
	capability := access.CapabilityProjectAdmin
	if surface == "REST" {
		capability = ""
	}
	require.Equal(t, capability, events[0].Capability)
	unchanged, err := readGroup(control.ID)
	require.NoError(t, err)
	require.Equal(t, control, unchanged)

	freshRevision, err := loadRevision(stored)
	require.NoError(t, err)
	require.NotEqual(t, revision, freshRevision)
	require.NoError(t, invoke(ctx, repository, "Recovered", freshRevision))
	recovered, err := readGroup(target.ID)
	require.NoError(t, err)
	require.Equal(t, "Recovered", recovered.Name)
	events, err = repository.ListAuditEvents(ctx, filter)
	require.NoError(t, err)
	require.Len(t, events, 2)
}

func TestGroupForMutationRequiresTransactionAndActiveGroup(t *testing.T) {
	ctx := t.Context()
	pool := postgrestest.Open(t, accesspostgres.ApplySchema)
	config := accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("admin-settings-test-key", 2))}
	repository, err := accesspostgres.NewAccess(pool, config)
	require.NoError(t, err)
	group, err := repository.UpsertGroup(ctx, access.GroupInput{Provider: "local", ExternalID: "local-team", Name: "Local team"})
	require.NoError(t, err)
	// The broad Repository interface deliberately does not expose the optional
	// locking reader. A normal repository read cannot stand in for that reader.
	withoutReader := struct{ access.Repository }{Repository: repository}
	row, err := access.GroupForMutation(ctx, withoutReader, group.ID)
	require.ErrorContains(t, err, "group revision locking is unavailable")
	require.Equal(t, access.Group{}, row)
	row, err = access.GroupForMutation(ctx, repository, group.ID)
	require.ErrorContains(t, err, "caller-owned transaction")
	require.Equal(t, access.Group{}, row)

	connection, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer connection.Release()
	connectionRepository, err := accesspostgres.NewAccess(connection.Conn(), config)
	require.NoError(t, err)
	row, err = access.GroupForMutation(ctx, connectionRepository, group.ID)
	require.ErrorContains(t, err, "caller-owned transaction")
	require.Equal(t, access.Group{}, row)
	tx, err := connection.Conn().Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	transactionRepository, err := accesspostgres.NewAccess(tx, config)
	require.NoError(t, err)
	row, err = access.GroupForMutation(ctx, transactionRepository, group.ID)
	require.NoError(t, err)
	require.Equal(t, group, row)
	expectedRevision, err := access.GroupRevision(group)
	require.NoError(t, err)
	lockedRevision, err := access.GroupRevision(row)
	require.NoError(t, err)
	require.Equal(t, expectedRevision, lockedRevision)

	row, err = access.GroupForMutation(ctx, transactionRepository, "00000000-0000-4000-8000-000000000001")
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Equal(t, access.Group{}, row)
	require.NoError(t, transactionRepository.DeleteGroup(ctx, group.ID))
	row, err = access.GroupForMutation(ctx, transactionRepository, group.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Equal(t, access.Group{}, row)
}
