package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/managedrecovery"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func managedJourneyEnrollment(t *testing.T, f *sourceCredentialHTTPJourney, set recoveryset.RecoverySet) {
	independent := postgrestest.StartTLS(t)
	database := independent.NewDatabase(t, "managed_recovery_authority")
	admin, err := pgxpool.New(t.Context(), productionAdmissionTLSURL(database.AdminURL(), independent.RootCertPath()))
	require.NoError(t, err)
	defer admin.Close()
	sourceSystemID := strings.TrimPrefix(set.ClusterPoints[0].ClusterIdentity, "postgres-system-id:")
	operatorURL, authoritySystemID := managedJourneyInitializeAuthority(t, independent, database, sourceSystemID)
	authority, err := pgxpool.New(t.Context(), productionAdmissionTLSURL(operatorURL, independent.RootCertPath()))
	require.NoError(t, err)
	defer authority.Close()
	source, err := pgxpool.New(t.Context(), productionAdmissionTLSURL(f.control.AdminURL(), f.harness.RootCertPath()))
	require.NoError(t, err)
	defer source.Close()
	now := time.Now().UTC().Truncate(time.Second)
	frontier, err := set.Digest()
	require.NoError(t, err)
	request := managedrecovery.ManagedEnrollmentRequest{InstanceHome: f.config.HomeDir, RecoverySetID: set.ID, FrontierDigest: frontier, RetentionRootID: set.ID, SourceSystemID: sourceSystemID, AuthoritySystemID: authoritySystemID, Actor: "actual-publication-enrollment-test", PlannedAt: now, ExpiresAt: now.Add(time.Hour),
		// This is a pending operator intent, never artifact qualification or a
		// claim that this test starts a protected release image.
		ArtifactIdentity: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("1", 64)}
	require.NoError(t, authority.QueryRow(t.Context(), "SELECT system_identifier::text FROM pg_control_system()").Scan(&request.AuthoritySystemID))
	_, err = managedrecovery.EnrollManagedRecovery(t.Context(), source, authority, request)
	require.Error(t, err, "a caller-built frontier must not replace original PrepareRecovery authority")
	_, err = recoverypostgres.New(authority).ReadExact(t.Context(), set.ID)
	require.ErrorIs(t, err, recoveryset.ErrNotFound)
	operations := adminpostgres.New(adminpostgres.Dependencies{LoadConfig: func() (config.Config, error) { return f.config, nil }})
	prepared, err := operations.PrepareRecovery(t.Context(), admincli.RecoveryPrepareRequest{Set: set, ExpiresAt: now.Add(2 * time.Hour)})
	require.NoError(t, err)
	require.Equal(t, frontier, prepared.Set.FrontierDigest)
	// Deny the second write in the independent transaction. The copied set
	// must not survive without its exact pending restore intent.
	_, err = admin.Exec(t.Context(), `ALTER TABLE refresh.recovery_qualification_occurrence ADD CONSTRAINT enrollment_test_deny CHECK (false) NOT VALID`)
	require.NoError(t, err)
	_, err = managedrecovery.EnrollManagedRecovery(t.Context(), source, authority, request)
	require.Error(t, err)
	_, err = recoverypostgres.New(authority).ReadExact(t.Context(), set.ID)
	require.ErrorIs(t, err, recoveryset.ErrNotFound)
	_, err = admin.Exec(t.Context(), `ALTER TABLE refresh.recovery_qualification_occurrence DROP CONSTRAINT enrollment_test_deny`)
	require.NoError(t, err)
	for _, mutate := range []func(*managedrecovery.ManagedEnrollmentRequest){
		func(r *managedrecovery.ManagedEnrollmentRequest) { r.SourceSystemID = r.AuthoritySystemID },
		func(r *managedrecovery.ManagedEnrollmentRequest) {
			r.FrontierDigest = "sha256:" + strings.Repeat("f", 64)
		},
		func(r *managedrecovery.ManagedEnrollmentRequest) { r.ExpiresAt = now.Add(3 * time.Hour) },
	} {
		invalid := request
		mutate(&invalid)
		_, err := managedrecovery.EnrollManagedRecovery(t.Context(), source, authority, invalid)
		require.Error(t, err)
	}
	var first managedrecovery.ManagedEnrollmentReceipt
	for attempt := 0; attempt < 2; attempt++ {
		receipt, err := managedrecovery.EnrollManagedRecovery(t.Context(), source, authority, request)
		require.NoError(t, err)
		if attempt == 0 {
			first = receipt
		} else {
			require.Equal(t, first, receipt)
		}
	}
	retained, err := recoverypostgres.New(authority).ReadExact(t.Context(), set.ID)
	require.NoError(t, err)
	want, err := prepared.Set.CanonicalJSON()
	require.NoError(t, err)
	actual, err := retained.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(want), string(actual))
	require.Equal(t, recoveryset.StatusPrepared, retained.Status)
	require.Empty(t, retained.PublishedValidationAttemptID)
	occurrence, err := refreshpostgres.NewRecoveryLedger(authority).Occurrence(t.Context(), first.OccurrenceID)
	require.NoError(t, err)
	require.Equal(t, recovery.StatusPending, occurrence.Status)
	require.Empty(t, occurrence.Evidence)
	require.Equal(t, first.PolicySHA256, occurrence.PolicySHA256)
	require.NoError(t, managedrecovery.VerifyManagedEnrollment(first, retained, occurrence, f.config.HomeDir))
	for _, mutate := range []func(*managedrecovery.ManagedEnrollmentReceipt){
		func(r *managedrecovery.ManagedEnrollmentReceipt) { r.Request.InstanceHome += "-foreign" },
		func(r *managedrecovery.ManagedEnrollmentReceipt) { r.Request.Actor = "another-operator" },
		func(r *managedrecovery.ManagedEnrollmentReceipt) {
			r.Request.AuthoritySystemID = r.Request.SourceSystemID
		},
		func(r *managedrecovery.ManagedEnrollmentReceipt) {
			r.Request.ExpiresAt = r.Request.ExpiresAt.Add(time.Minute)
		},
	} {
		changed := first
		mutate(&changed)
		require.Error(t, managedrecovery.VerifyManagedEnrollment(changed, retained, occurrence, changed.Request.InstanceHome))
	}
	require.Error(t, managedrecovery.VerifyManagedEnrollment(first, retained, occurrence, f.config.HomeDir+"-foreign"))
	privateRoot := t.TempDir()
	require.NoError(t, os.Chmod(privateRoot, 0700))
	privateAuthority := func(name, rawURL, caPath, systemID string) managedrecovery.AuthorityInput {
		t.Helper()
		parsed, err := url.Parse(rawURL)
		require.NoError(t, err)
		parsed.Scheme, parsed.RawQuery = "postgres", "sslmode=verify-full"
		input := managedrecovery.AuthorityInput{URLFile: filepath.Join(privateRoot, name+".url"), RootCAFile: filepath.Join(privateRoot, name+".ca"), Role: parsed.User.Username(), SystemIdentifier: systemID}
		ca, err := os.ReadFile(caPath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(input.URLFile, []byte(parsed.String()), 0600))
		require.NoError(t, os.WriteFile(input.RootCAFile, ca, 0600))
		return input
	}
	input := managedrecovery.ManagedEnrollmentInput{SchemaVersion: 1, Request: request, ReceiptFile: filepath.Join(privateRoot, "receipt.json"), Source: privateAuthority("source", f.control.AdminURL(), f.harness.RootCertPath(), request.SourceSystemID), Authority: privateAuthority("authority", operatorURL, independent.RootCertPath(), request.AuthoritySystemID)}
	inputPath := filepath.Join(privateRoot, "enrollment.json")
	value, err := json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(inputPath, value, 0600))
	input, err = managedrecovery.ReadManagedEnrollmentInput(inputPath)
	require.NoError(t, err)
	lock, err := instancelock.Acquire(f.config.HomeDir)
	require.NoError(t, err)
	defer lock.Release()
	for attempt := 0; attempt < 2; attempt++ {
		receipt, err := input.Enroll(t.Context())
		require.NoError(t, err)
		require.Equal(t, first, receipt)
	}
	value, err = os.ReadFile(input.ReceiptFile)
	require.NoError(t, err)
	var receipt managedrecovery.ManagedEnrollmentReceipt
	require.NoError(t, json.Unmarshal(value, &receipt))
	require.Equal(t, first, receipt)
}
