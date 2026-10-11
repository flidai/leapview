package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocolgen "github.com/flidai/leapview/internal/platform/http/api/gen"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// This is component evidence, not the installed NixOS promotion/SSH-fence
// qualification. Real providers restore the production HTTP publication; a
// test authority publishes the observed frontier through the canonical store.
// No host receipt or protected execution authority is manufactured here.
func TestManagedRecoveryReplacementApplication(t *testing.T) {
	for _, name := range []string{"LEAPVIEW_TEST_MANAGED_RESTIC", "LEAPVIEW_TEST_MANAGED_POSTGRES_BIN", "LEAPVIEW_TEST_MANAGED_PGBACKREST", "LEAPVIEW_TEST_MANAGED_BWRAP"} {
		if os.Getenv(name) == "" {
			t.Skip("explicit locked native provider tools required")
		}
	}
	started := time.Now()
	f, token := runFirstSourceProductionPublicationJourneyBeforeRestart(t, false, func(f *sourceCredentialHTTPJourney) {
		connection, err := pgx.Connect(t.Context(), f.control.AdminURL())
		require.NoError(t, err)
		defer connection.Close(context.Background())
		// Runtime readiness may precede the worker's durable completion. Let the
		// real approval job acknowledge its result before the fixture restarts.
		require.Eventually(t, func() bool {
			var complete bool
			err := connection.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM jobs.job_history WHERE kind='delivery.approval.activate' AND status='succeeded')`).Scan(&complete)
			return err == nil && complete
		}, time.Minute, 100*time.Millisecond, "real publication activation must durably complete before backup")
	})
	wantSnapshot := f.querySource(t, token, "30")
	require.NoError(t, f.target.Shutdown(context.Background()))
	f.target = nil
	original, err := pgx.Connect(t.Context(), f.control.AdminURL())
	require.NoError(t, err)
	wantAcknowledgments := managedReplacementAcknowledgments(t, original, nil)
	require.NoError(t, original.Close(t.Context()))
	native := managedJourneyReadback(t, f)
	roots := managedJourneyFileRestore(t, f, native.Set)
	native.Set.ObjectRoots = nil
	for _, root := range roots {
		native.Set.ObjectRoots = append(native.Set.ObjectRoots, root.root)
	}
	restored := managedJourneyPhysicalRestore(t, f, native)
	config, replacement := restored.startReplacement(t, f.config)
	defer replacement.Close(context.Background())
	before := managedReplacementAcknowledgments(t, replacement, &wantAcknowledgments)
	require.Equal(t, wantAcknowledgments, before)
	set := restored.native.Set
	config.RecoverySetID = set.ID
	config.MaintenanceSocket = filepath.Join(t.TempDir(), "maintenance.sock")
	// Frozen catalog URIs stay unchanged. Only private, stopped fixture roots
	// are moved; the app can no longer read original bytes at canonical paths.
	for _, root := range roots {
		managedReplacementInstallRoot(t, f.config.HomeDir, root)
	}
	// RED: the restored backup predates independent publication/adoption.
	// Readiness must not silently accept the original app's valid generation.
	missing := *f
	missing.config, missing.target = config, nil
	missing.start(t)
	missing.request(t, http.MethodGet, "/maintenance/readyz", "", nil, http.StatusServiceUnavailable)
	require.NoError(t, missing.target.Shutdown(context.Background()))
	set = managedReplacementAdopt(t, f, replacement, set)

	for restart := 0; restart < 2; restart++ {
		fresh := *f
		fresh.config, fresh.target = config, nil
		fresh.start(t)
		func() {
			defer func() { require.NoError(t, fresh.target.Shutdown(context.Background())) }()
			fresh.request(t, http.MethodGet, "/readyz", "", nil, http.StatusServiceUnavailable)
			fresh.request(t, http.MethodGet, "/maintenance/readyz", "", nil, http.StatusOK)
			managedReplacementControl(t, fresh.target, "/prepare", set.FrontierDigest, http.StatusOK)
			fresh.request(t, http.MethodGet, "/readyz", "", nil, http.StatusServiceUnavailable)
			// In-memory canonical HTTP only: no public listener is created.
			managedReplacementControl(t, fresh.target, "/open", set.FrontierDigest, http.StatusOK)
			fresh.request(t, http.MethodGet, "/readyz", "", nil, http.StatusOK)
			require.Equal(t, wantSnapshot, fresh.querySource(t, token, "30"))
		}()
		require.Equal(t, before, managedReplacementAcknowledgments(t, replacement, &wantAcknowledgments), "restored publication and completed jobs must not be rewritten")
	}
	for _, root := range roots {
		t.Run("missing restored "+root.root.Kind, func(t *testing.T) {
			absent := root.source + ".component-missing"
			require.NoError(t, os.Rename(root.source, absent))
			defer func() {
				require.NoError(t, os.RemoveAll(root.source))
				require.NoError(t, os.Rename(absent, root.source))
			}()
			if root.root.Kind == recoveryset.ObjectRootDuckLake {
				require.Error(t, root.manifest.Verify(root.source), "retained provider closure must detect missing restored files")
				fresh := *f
				fresh.config, fresh.target = config, nil
				fresh.start(t)
				defer fresh.target.Shutdown(context.Background())
				// Readiness checks sealed metadata, not every data-file byte. The
				// same authorized query must fail at execution without cached or
				// original bytes. No public listener is opened for this exercise.
				fresh.request(t, http.MethodGet, "/maintenance/readyz", "", nil, http.StatusOK)
				managedReplacementControl(t, fresh.target, "/prepare", set.FrontierDigest, http.StatusOK)
				managedReplacementControl(t, fresh.target, "/open", set.FrontierDigest, http.StatusOK)
				response := fresh.request(t, http.MethodPost, "/api/v1/semantic-models/semantic-model:sales/query", token, map[string]any{"metrics": []map[string]string{{"field": "total"}}}, http.StatusBadRequest)
				var failure struct {
					protocolgen.ProblemDetails
					Rows json.RawMessage `json:"rows"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &failure))
				require.Equal(t, "application/problem+json", response.Header().Get("Content-Type"))
				require.Equal(t, "HTTP_400", failure.Code)
				require.EqualValues(t, http.StatusBadRequest, failure.Status)
				require.Empty(t, failure.Rows)
				message := strings.ToLower(failure.Detail)
				require.True(t, strings.Contains(failure.Detail, root.source) && strings.Contains(message, ".parquet") && (strings.Contains(message, "no such file") || strings.Contains(message, "does not exist") || strings.Contains(message, "cannot open") || strings.Contains(message, "not found")), "governed query must report the missing retained parquet file, not an unrelated admission/authentication error")
				return
			}
			app, err := BuildProduction(t.Context(), config)
			if err != nil {
				return
			}
			defer app.Shutdown(context.Background())
			if err := app.Start(t.Context()); err != nil {
				return
			}
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/maintenance/readyz", nil))
			require.Equal(t, http.StatusServiceUnavailable, response.Code, "a fresh app must reject missing restored files")
		})
	}
	if !t.Failed() {
		t.Logf("component replacement application: restore/prepare/query/fresh-pool restart and drift checks passed in %d ms; protected activation and full managed profile unqualified", time.Since(started).Milliseconds())
	}
}

func managedReplacementInstallRoot(t *testing.T, home string, root managedJourneyRestoredRoot) {
	t.Helper()
	relative, err := filepath.Rel(home, root.source)
	require.NoError(t, err)
	require.False(t, relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)), "only this stopped fixture's private roots may move")
	aside := root.source + ".original-component-fixture"
	_, err = os.Lstat(aside)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Rename(root.source, aside))
	t.Cleanup(func() {
		if err := os.RemoveAll(root.source); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(aside, root.source); err != nil {
			t.Error(err)
		}
	})
	require.NoError(t, os.Rename(root.destination, root.source))
}

func managedReplacementControl(t *testing.T, app *Application, path, operation string, want int) {
	t.Helper()
	request := maintenanceControlRequest{Revision: app.lifecycle.maintenance.status().Revision, Operation: operation, LeaseMilliseconds: 60_000}
	body, err := json.Marshal(request)
	require.NoError(t, err)
	response := httptest.NewRecorder()
	app.MaintenanceHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	require.Equal(t, want, response.Code, "private maintenance operation %s", path)
}

type managedReplacementAcknowledgment struct {
	PublicationIDs, CompletedJobIDs []string
	Digest                          string
}

func managedReplacementAcknowledgments(t *testing.T, connection *pgx.Conn, selected *managedReplacementAcknowledgment) managedReplacementAcknowledgment {
	t.Helper()
	var retained managedReplacementAcknowledgment
	var publicationIDs, jobIDs []string
	if selected != nil {
		publicationIDs, jobIDs = selected.PublicationIDs, selected.CompletedJobIDs
	}
	require.NoError(t, connection.QueryRow(t.Context(), `SELECT md5(jsonb_build_object(
	 'publications',(SELECT jsonb_agg(to_jsonb(p) ORDER BY publication_id) FROM delivery.delivery_publication p WHERE $1::text[] IS NULL OR publication_id::text=ANY($1)),
	 'completedJobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs.job_history j WHERE status='succeeded' AND ($2::text[] IS NULL OR id=ANY($2))))::text),
	 ARRAY(SELECT publication_id::text FROM delivery.delivery_publication WHERE $1::text[] IS NULL OR publication_id::text=ANY($1) ORDER BY publication_id),
	 ARRAY(SELECT id FROM jobs.job_history WHERE status='succeeded' AND ($2::text[] IS NULL OR id=ANY($2)) ORDER BY id)`, publicationIDs, jobIDs).Scan(&retained.Digest, &retained.PublicationIDs, &retained.CompletedJobIDs))
	require.NotEmpty(t, retained.PublicationIDs, "real production publication acknowledgment required")
	require.NotEmpty(t, retained.CompletedJobIDs, "real completed publication job required")
	return retained
}

func managedReplacementAdopt(t *testing.T, f *sourceCredentialHTTPJourney, replacement *pgx.Conn, set recoveryset.RecoverySet) recoveryset.RecoverySet {
	t.Helper()
	// The original fixture is stopped; its test database is only an authority
	// for this component envelope after real provider readback. This is not the
	// independent host/fence authority exercised by the installed qualification.
	authority, err := pgx.Connect(t.Context(), f.control.AdminURL())
	require.NoError(t, err)
	defer authority.Close(context.Background())
	repository := recoverypostgres.New(authority)
	set, err = repository.Create(t.Context(), set)
	require.NoError(t, err)
	started := time.Now().UTC().Truncate(time.Microsecond)
	attempt := recoveryset.ValidationAttempt{AttemptID: uuid.NewString(), SetID: set.ID, OwnerID: "component-native-provider-readback", AuditIdentity: "component-replacement-application", FenceEpoch: set.FenceEpoch, StartedAt: started, Status: recoveryset.ValidationRunning}
	_, err = repository.BeginValidation(t.Context(), attempt)
	require.NoError(t, err)
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(set, attempt.AttemptID)
	require.NoError(t, err)
	result, err := recoveryset.NewValidationResult(envelope, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, repository.RecordValidationResult(t.Context(), result))
	attempt.Status, attempt.ResultDigest, attempt.CompletedAt = recoveryset.ValidationPassed, result.ResultDigest, time.Now().UTC()
	require.NoError(t, repository.CompleteValidation(t.Context(), attempt))
	set, err = repository.Publish(t.Context(), set.ID, "component-test-authority", set.FenceEpoch, attempt.AttemptID)
	require.NoError(t, err)
	tx, err := replacement.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	_, err = recoverypostgres.New(tx).AdoptPublishedTx(t.Context(), tx, set, attempt, result, "component-replacement")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	return set
}
