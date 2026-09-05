package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

const initializationFingerprintKey = "0123456789abcdef0123456789abcdef"

func TestInitializeInstancePrepareFailureRollsBackEveryBootstrapRow(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte(initializationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	input := access.InstanceInitializationInput{
		Email: "prepare-rollback@example.test", Environment: "production", Now: time.Now().UTC(),
	}
	prepareErr := errors.New("persist recovery bundle")
	var prepared access.InitialInstanceCredentials
	got, err := repo.InitializeInstance(t.Context(), input, func(credentials access.InitialInstanceCredentials) error {
		prepared = credentials
		return prepareErr
	})
	if !errors.Is(err, prepareErr) {
		t.Fatalf("prepare failure = %v, want %v", err, prepareErr)
	}
	if prepared.Email != input.Email || prepared.TemporaryPassword == "" || prepared.PublisherToken == "" || prepared.PublisherTokenExpiresAt.IsZero() {
		t.Fatalf("prepare callback did not receive complete credentials: %#v", prepared)
	}
	if !emptyInitialCredentials(got) {
		t.Fatalf("prepare failure returned credentials: %#v", got)
	}
	initialized, err := repo.Initialized(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if initialized {
		t.Fatal("prepare failure left the instance initialized")
	}
	assertInitializationRows(t, db, input.Email, 0)

	var retried access.InitialInstanceCredentials
	got, err = repo.InitializeInstance(t.Context(), input, func(credentials access.InitialInstanceCredentials) error {
		retried = credentials
		return nil
	})
	if err != nil {
		t.Fatalf("retry after prepare failure: %v", err)
	}
	if got.Email != input.Email || got.TemporaryPassword == "" || got.PublisherToken == "" || got.PublisherTokenExpiresAt.IsZero() {
		t.Fatalf("retry credentials are incomplete: %#v", got)
	}
	if retried.Email != got.Email || retried.TemporaryPassword != got.TemporaryPassword || retried.PublisherToken != got.PublisherToken || !retried.PublisherTokenExpiresAt.Equal(got.PublisherTokenExpiresAt) {
		t.Fatalf("retry callback credentials differ from result: callback=%#v result=%#v", retried, got)
	}
	initialized, err = repo.Initialized(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !initialized {
		t.Fatal("successful retry did not leave the instance initialized")
	}
	assertInitializationRows(t, db, input.Email, 1)
}

func TestInitializeInstanceConcurrentCallsConvergeOnOneDurableBootstrap(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte(initializationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	input := access.InstanceInitializationInput{
		Email: "concurrent-bootstrap@example.test", Environment: "production", Now: time.Now().UTC(),
	}
	const callers = 8
	start := make(chan struct{})
	started := make(chan struct{}, callers)
	prepareEntered := make(chan struct{})
	releasePrepare := make(chan struct{})
	var prepareOnce sync.Once
	var releaseOnce sync.Once
	var preparedMu sync.Mutex
	prepared := make([]access.InitialInstanceCredentials, 0, 1)
	prepare := func(credentials access.InitialInstanceCredentials) error {
		prepareOnce.Do(func() { close(prepareEntered) })
		<-releasePrepare
		preparedMu.Lock()
		prepared = append(prepared, credentials)
		preparedMu.Unlock()
		return nil
	}
	release := func() { releaseOnce.Do(func() { close(releasePrepare) }) }
	defer release()

	type outcome struct {
		credentials access.InitialInstanceCredentials
		err         error
	}
	results := make(chan outcome, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			<-start
			credentials, callErr := repo.InitializeInstance(ctx, input, prepare)
			results <- outcome{credentials: credentials, err: callErr}
		}()
	}
	close(start)
	for i := 0; i < callers; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatalf("concurrent initialization did not start: %v", ctx.Err())
		}
	}
	select {
	case <-prepareEntered:
	case <-ctx.Done():
		t.Fatalf("concurrent initialization never reached credential preparation: %v", ctx.Err())
	}
	// Keep the winner's transaction open until every caller has entered the
	// operation, forcing the remaining calls to contend on the marker row.
	release()
	wg.Wait()
	close(results)

	var success outcome
	successes, conflicts := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			success = result
		case errors.Is(result.err, access.ErrInstanceAlreadyInitialized):
			conflicts++
			if !emptyInitialCredentials(result.credentials) {
				t.Errorf("losing initializer returned credentials: %#v", result.credentials)
			}
		default:
			t.Errorf("concurrent initializer returned unexpected error: %v", result.err)
		}
	}
	if successes != 1 || conflicts != callers-1 {
		t.Fatalf("concurrent initialization outcomes: successes=%d conflicts=%d, want 1/%d", successes, conflicts, callers-1)
	}
	if success.credentials.Email != input.Email || success.credentials.TemporaryPassword == "" || success.credentials.PublisherToken == "" || success.credentials.PublisherTokenExpiresAt.IsZero() {
		t.Fatalf("winning initializer credentials are incomplete: %#v", success.credentials)
	}
	preparedMu.Lock()
	preparedCount := len(prepared)
	var preparedCredentials access.InitialInstanceCredentials
	if preparedCount == 1 {
		preparedCredentials = prepared[0]
	}
	preparedMu.Unlock()
	if preparedCount != 1 {
		t.Fatalf("credential prepare callback count = %d, want 1", preparedCount)
	}
	if preparedCredentials.Email != success.credentials.Email || preparedCredentials.TemporaryPassword != success.credentials.TemporaryPassword || preparedCredentials.PublisherToken != success.credentials.PublisherToken || !preparedCredentials.PublisherTokenExpiresAt.Equal(success.credentials.PublisherTokenExpiresAt) {
		t.Fatalf("prepared winner credentials differ from returned result: callback=%#v result=%#v", preparedCredentials, success.credentials)
	}
	assertInitializationRows(t, db, input.Email, 1)
}

func emptyInitialCredentials(credentials access.InitialInstanceCredentials) bool {
	return credentials.Email == "" && credentials.TemporaryPassword == "" && credentials.PublisherToken == "" && credentials.PublisherTokenExpiresAt.IsZero()
}

func assertInitializationRows(t *testing.T, db auditDatabase, email string, want int64) {
	t.Helper()
	var marker, principals, credentials, roles, tokens, audits int64
	err := db.admin.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM access.platform_setting WHERE key = $1),
			(SELECT count(*) FROM access.principal WHERE lower(email) = lower($2)),
			(SELECT count(*) FROM access.local_credential WHERE principal_id IN (SELECT id FROM access.principal WHERE lower(email) = lower($2))),
			(SELECT count(*) FROM access.platform_role_binding WHERE role = $3 AND principal_id IN (SELECT id FROM access.principal WHERE lower(email) = lower($2)) AND revoked_at IS NULL),
			(SELECT count(*) FROM access.api_token WHERE name = $4 AND principal_id IN (SELECT id FROM access.principal WHERE lower(email) = lower($2)) AND revoked_at IS NULL),
			(SELECT count(*) FROM audit.audit_event WHERE action = $5)`,
		access.InstanceInitializedSetting, email, string(access.PlatformRoleAdmin), access.APITokenNameInitialPublisher, "instance.initialized").
		Scan(&marker, &principals, &credentials, &roles, &tokens, &audits)
	if err != nil {
		t.Fatalf("read initialization rows: %v", err)
	}
	if marker != want || principals != want || credentials != want || roles != want || tokens != want || audits != want {
		t.Fatalf("initialization rows = marker=%d principals=%d credentials=%d roles=%d tokens=%d audits=%d, want %d each", marker, principals, credentials, roles, tokens, audits, want)
	}
}
