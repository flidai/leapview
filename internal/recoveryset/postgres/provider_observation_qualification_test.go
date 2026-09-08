//go:build fai520qualification

package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	providerObservationHelperEnv   = "LEAPVIEW_RECOVERYSET_PROVIDER_OBSERVATION_HELPER"
	providerObservationDSNEnv      = "LEAPVIEW_RECOVERYSET_PROVIDER_OBSERVATION_DSN"
	providerObservationSetEnv      = "LEAPVIEW_RECOVERYSET_PROVIDER_OBSERVATION_SET_ID"
	providerObservationAttemptEnv  = "LEAPVIEW_RECOVERYSET_PROVIDER_OBSERVATION_ATTEMPT_ID"
	providerObservationSetJSONEnv  = "LEAPVIEW_RECOVERYSET_PROVIDER_OBSERVATION_SET_JSON"
	providerObservationEvidenceEnv = "LEAPVIEW_RECOVERYSET_PROVIDER_OBSERVATION_EVIDENCE"
	providerObservationFrontierEnv = "LEAPVIEW_RECOVERYSET_PROVIDER_OBSERVATION_FRONTIER_DIGEST"
	providerObservationResultEnv   = "LEAPVIEW_RECOVERYSET_PROVIDER_OBSERVATION_RESULT_DIGEST"
)

func TestProviderObservationDurabilityAndExactReplay(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	db := recoverySetDB(t)
	repo := New(db)
	set := providerObservationRemoteFixture(t)
	assertProviderObservationRoots(t, set)

	created, err := repo.Create(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}
	if created.FrontierDigest == "" {
		t.Fatal("created frontier digest is empty")
	}
	read, err := repo.ReadExact(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRecoverySetCanonicalBytesEqual(t, created, read)
	if read.Status != recoveryset.StatusPrepared || read.PublishedValidationAttemptID != "" {
		t.Fatalf("validation observation changed frontier lifecycle state: status=%q published_attempt=%q", read.Status, read.PublishedValidationAttemptID)
	}

	started := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	firstAttempt := recoveryset.ValidationAttempt{
		AttemptID:     "018f3f83-7b2f-7b37-9f9e-000000000140",
		SetID:         created.ID,
		OwnerID:       "provider-observer",
		FenceEpoch:    created.FenceEpoch,
		AuditIdentity: created.AuditIdentity,
		Status:        recoveryset.ValidationRunning,
		StartedAt:     started,
	}
	if _, err := repo.BeginValidation(t.Context(), firstAttempt); err != nil {
		t.Fatal(err)
	}
	firstEnvelope, err := recoveryset.NewValidationEvidenceEnvelope(created, firstAttempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	missingProviderFrontier := firstEnvelope
	missingProviderFrontier.ObjectRoots = append([]recoveryset.ValidationEvidenceObjectRoot(nil), firstEnvelope.ObjectRoots...)
	missingProviderFrontier.ObjectRoots[0].ProviderRecoveryFrontier = ""
	if err := missingProviderFrontier.ValidateFor(created, firstAttempt.AttemptID); !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("missing remote provider recovery frontier = %v, want invalid", err)
	}
	changedRootVersion := firstEnvelope
	changedRootVersion.ObjectRoots = append([]recoveryset.ValidationEvidenceObjectRoot(nil), firstEnvelope.ObjectRoots...)
	changedRootVersion.ObjectRoots[0].VersionID += "-changed"
	if err := changedRootVersion.ValidateFor(created, firstAttempt.AttemptID); !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("changed remote root version = %v, want invalid", err)
	}
	changedProviderFrontier := firstEnvelope
	changedProviderFrontier.ObjectRoots = append([]recoveryset.ValidationEvidenceObjectRoot(nil), firstEnvelope.ObjectRoots...)
	changedProviderFrontier.ObjectRoots[0].ProviderRecoveryFrontier += "-changed"
	if err := changedProviderFrontier.ValidateFor(created, firstAttempt.AttemptID); !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("changed remote provider recovery frontier = %v, want invalid", err)
	}
	firstResult, err := recoveryset.NewValidationResult(firstEnvelope, started.Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordValidationResult(t.Context(), firstResult); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordValidationResult(t.Context(), firstResult); err != nil {
		t.Fatalf("identical validation-result replay: %v", err)
	}
	retryWithFreshClock := firstResult
	retryWithFreshClock.RecordedAt = retryWithFreshClock.RecordedAt.Add(time.Minute)
	if err := repo.RecordValidationResult(t.Context(), retryWithFreshClock); err != nil {
		t.Fatalf("validation-result replay with caller timestamp: %v", err)
	}
	storedResult, err := repo.ValidationResult(t.Context(), firstAttempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if storedResult.ResultDigest != firstResult.ResultDigest || !bytes.Equal(storedResult.Evidence, firstResult.Evidence) {
		t.Fatalf("stored validation result changed on replay: %#v", storedResult)
	}

	secondAttempt := firstAttempt
	secondAttempt.AttemptID = "018f3f83-7b2f-7b37-9f9e-000000000141"
	if _, err := repo.BeginValidation(t.Context(), secondAttempt); err != nil {
		t.Fatal(err)
	}
	secondEnvelope, err := recoveryset.NewValidationEvidenceEnvelope(created, secondAttempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := recoveryset.NewValidationResult(secondEnvelope, started.Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.ResultDigest == firstResult.ResultDigest {
		t.Fatal("different validation attempts reused the same attempt-bound result digest")
	}
	if err := repo.RecordValidationResult(t.Context(), secondResult); err != nil {
		t.Fatal(err)
	}

	conflictingEnvelope := firstEnvelope
	conflictingEnvelope.RelationNamespace = "candidate/conflicting"
	conflictingResult, err := recoveryset.NewValidationResult(conflictingEnvelope, firstResult.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordValidationResult(t.Context(), conflictingResult); !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("repository overwrite with conflicting evidence = %v, want invalid", err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE recovery.validation_result SET result_digest=$2 WHERE attempt_id=$1::uuid`, firstAttempt.AttemptID, secondResult.ResultDigest); err == nil {
		t.Fatal("SQL update of immutable validation evidence succeeded")
	}

	setJSON, err := created.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	runProviderObservationReadOnlyHelper(t, db.Config().ConnString(), created.ID, firstAttempt.AttemptID, setJSON, firstResult)
}

func TestProviderObservationFrontierContractRejectsConflicts(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	db := recoverySetDB(t)
	repo := New(db)
	base := providerObservationRemoteFixture(t)
	assertProviderObservationRoots(t, base)

	cases := map[string]func(recoveryset.RecoverySet) recoveryset.RecoverySet{
		"missing_ducklake_root": func(set recoveryset.RecoverySet) recoveryset.RecoverySet {
			set.ObjectRoots = []recoveryset.ObjectRoot{set.ObjectRoots[1]}
			return set
		},
		"missing_serving_artifact_root": func(set recoveryset.RecoverySet) recoveryset.RecoverySet {
			set.ObjectRoots = []recoveryset.ObjectRoot{set.ObjectRoots[0]}
			return set
		},
		"mismatched_ducklake_digest": func(set recoveryset.RecoverySet) recoveryset.RecoverySet {
			set.ObjectRoots = append([]recoveryset.ObjectRoot(nil), set.ObjectRoots...)
			set.ObjectRoots[0].Digest = providerObservationDigest('9')
			return set
		},
		"stale_frontier_digest": func(set recoveryset.RecoverySet) recoveryset.RecoverySet {
			set.FrontierDigest = providerObservationDigest('9')
			return set
		},
		"duplicate_conflicting_roots": func(set recoveryset.RecoverySet) recoveryset.RecoverySet {
			set.ObjectRoots = append([]recoveryset.ObjectRoot(nil), set.ObjectRoots...)
			set.ObjectRoots[1] = set.ObjectRoots[0]
			set.ObjectRoots[1].Digest = providerObservationDigest('9')
			return set
		},
		"managed_data_third_root": func(set recoveryset.RecoverySet) recoveryset.RecoverySet {
			set.ObjectRoots = append(append([]recoveryset.ObjectRoot(nil), set.ObjectRoots...), recoveryset.ObjectRoot{
				Kind: "managed-data", URI: "s3://bucket/managed-inventory", VersionID: "1", Digest: providerObservationDigest('7'), ProviderRecoveryFrontier: "managed-frontier-1",
			})
			return set
		},
		"managed_data_replaces_root": func(set recoveryset.RecoverySet) recoveryset.RecoverySet {
			set.ObjectRoots = append([]recoveryset.ObjectRoot(nil), set.ObjectRoots...)
			set.ObjectRoots[1].Kind = "managed-data"
			return set
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(base)
			if _, err := repo.Create(t.Context(), candidate); !errors.Is(err, recoveryset.ErrInvalid) {
				t.Fatalf("invalid frontier candidate = %v, want invalid", err)
			}
		})
	}

	created, err := repo.Create(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.ObjectRoots = append([]recoveryset.ObjectRoot(nil), base.ObjectRoots...)
	changed.ObjectRoots[0].VersionID = "2"
	if _, err := repo.Create(t.Context(), changed); !errors.Is(err, recoveryset.ErrConflict) {
		t.Fatalf("same-ID root replacement = %v, want conflict", err)
	}
	if read, err := repo.ReadExact(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	} else if read.ObjectRoots[0].VersionID != base.ObjectRoots[0].VersionID {
		t.Fatalf("same-ID conflict changed stored root version to %q", read.ObjectRoots[0].VersionID)
	}
}

func TestProviderObservationEvidenceRejectsArbitraryManagedFields(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	db := recoverySetDB(t)
	repo := New(db)
	set := providerObservationRemoteFixture(t)
	created, err := repo.Create(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "018f3f83-7b2f-7b37-9f9e-000000000142"
	started := time.Date(2026, 9, 8, 12, 5, 0, 0, time.UTC)
	if _, err := repo.BeginValidation(t.Context(), recoveryset.ValidationAttempt{
		AttemptID: attemptID, SetID: created.ID, OwnerID: "provider-observer", FenceEpoch: created.FenceEpoch,
		AuditIdentity: created.AuditIdentity, Status: recoveryset.ValidationRunning, StartedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(created, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, started)
	if err != nil {
		t.Fatal(err)
	}
	arbitraryManagedField := append(append([]byte(nil), result.Evidence[:len(result.Evidence)-1]...), []byte(`,"managed_observations":{"revision":"latest"}}`)...)
	if _, err := recoveryset.ParseValidationEvidenceEnvelope(arbitraryManagedField); !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("managed_observations evidence field = %v, want invalid", err)
	}
	if err := repo.RecordValidationResult(t.Context(), recoveryset.ValidationResult{
		AttemptID: attemptID, ResultDigest: result.ResultDigest, Evidence: arbitraryManagedField, RecordedAt: result.RecordedAt,
	}); !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("repository accepted opaque managed_observations evidence field = %v", err)
	}
}

func TestProviderObservationReadOnlyHelper(t *testing.T) {
	if os.Getenv(providerObservationHelperEnv) != "1" {
		return
	}
	dsn := os.Getenv(providerObservationDSNEnv)
	setID := os.Getenv(providerObservationSetEnv)
	attemptID := os.Getenv(providerObservationAttemptEnv)
	if dsn == "" || setID == "" || attemptID == "" {
		t.Fatal("provider observation helper environment is incomplete")
	}
	p, err := recoverySetPoolForHelper(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	repo := New(p)
	set, err := repo.ReadExact(t.Context(), setID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.ValidationResult(t.Context(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := recoveryset.ParseValidationEvidenceEnvelope(result.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.ValidateFor(set, attemptID); err != nil {
		t.Fatalf("reloaded root evidence does not validate against persisted frontier: %v", err)
	}
	setJSON, err := set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	resultEvidence, err := base64.StdEncoding.DecodeString(os.Getenv(providerObservationEvidenceEnv))
	if err != nil {
		t.Fatalf("decode expected evidence: %v", err)
	}
	expectedSetJSON, err := base64.StdEncoding.DecodeString(os.Getenv(providerObservationSetJSONEnv))
	if err != nil {
		t.Fatalf("decode expected set: %v", err)
	}
	if !bytes.Equal(setJSON, expectedSetJSON) || set.FrontierDigest != os.Getenv(providerObservationFrontierEnv) {
		t.Fatalf("subprocess recovery frontier differs: digest=%q bytes_equal=%t", set.FrontierDigest, bytes.Equal(setJSON, expectedSetJSON))
	}
	if set.Status != recoveryset.StatusPrepared || set.PublishedValidationAttemptID != "" {
		t.Fatalf("subprocess changed frontier lifecycle state: status=%q published_attempt=%q", set.Status, set.PublishedValidationAttemptID)
	}
	if result.ResultDigest != os.Getenv(providerObservationResultEnv) || !bytes.Equal(result.Evidence, resultEvidence) {
		t.Fatalf("subprocess validation evidence differs: digest=%q bytes_equal=%t", result.ResultDigest, bytes.Equal(result.Evidence, resultEvidence))
	}
}

func runProviderObservationReadOnlyHelper(t *testing.T, dsn, setID, attemptID string, setJSON []byte, result recoveryset.ValidationResult) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run", "^TestProviderObservationReadOnlyHelper$", "-test.timeout", "30s")
	command.Env = append(os.Environ(),
		providerObservationHelperEnv+"=1",
		providerObservationDSNEnv+"="+dsn,
		providerObservationSetEnv+"="+setID,
		providerObservationAttemptEnv+"="+attemptID,
		providerObservationSetJSONEnv+"="+base64.StdEncoding.EncodeToString(setJSON),
		providerObservationEvidenceEnv+"="+base64.StdEncoding.EncodeToString(result.Evidence),
		providerObservationFrontierEnv+"="+strings.TrimSpace(resultFrontierDigest(t, setJSON)),
		providerObservationResultEnv+"="+result.ResultDigest,
	)
	if output, err := command.CombinedOutput(); err != nil {
		redacted := strings.ReplaceAll(string(output), dsn, "[redacted-dsn]")
		if ctx.Err() != nil {
			t.Fatalf("provider observation read-only subprocess: %v (%v)", ctx.Err(), err)
		}
		t.Fatalf("provider observation read-only subprocess: %v\n%s", err, redacted)
	}
}

func recoverySetPoolForHelper(t *testing.T, dsn string) (*pgxpool.Pool, error) {
	// Keep pool construction in one place so the subprocess performs only the
	// existing repository read operations against a fresh connection pool.
	return pgxpool.New(t.Context(), dsn)
}

func resultFrontierDigest(t *testing.T, setJSON []byte) string {
	t.Helper()
	set := recoveryset.RecoverySet{}
	if err := json.Unmarshal(setJSON, &set); err != nil {
		t.Fatal(err)
	}
	return set.FrontierDigest
}

func providerObservationRemoteFixture(t *testing.T) recoveryset.RecoverySet {
	t.Helper()
	set := recoverySetFixture(t)
	set.Serving.ObjectRoot = "s3://bucket/objects/target"
	set.Serving.ArtifactRoot = "s3://bucket/artifacts/target"
	set.ObjectRoots = append([]recoveryset.ObjectRoot(nil), set.ObjectRoots...)
	for index := range set.ObjectRoots {
		switch set.ObjectRoots[index].Kind {
		case recoveryset.ObjectRootDuckLake:
			set.ObjectRoots[index].URI = set.Serving.ObjectRoot
			set.ObjectRoots[index].VersionID = "ducklake-version-1"
			set.ObjectRoots[index].ProviderRecoveryFrontier = "ducklake-frontier-1"
		case recoveryset.ObjectRootServingArtifact:
			set.ObjectRoots[index].URI = set.Serving.ArtifactRoot
			set.ObjectRoots[index].VersionID = "serving-artifact-version-1"
			set.ObjectRoots[index].ProviderRecoveryFrontier = "serving-artifact-frontier-1"
		}
	}
	return set
}

func assertProviderObservationRoots(t *testing.T, set recoveryset.RecoverySet) {
	t.Helper()
	if len(set.ObjectRoots) != 2 {
		t.Fatalf("provider observation fixture roots = %d, want exactly 2", len(set.ObjectRoots))
	}
	seen := map[string]bool{}
	for _, root := range set.ObjectRoots {
		seen[root.Kind] = true
	}
	if !seen[recoveryset.ObjectRootDuckLake] || !seen[recoveryset.ObjectRootServingArtifact] || len(seen) != 2 {
		t.Fatalf("provider observation fixture roots = %#v, want ducklake and serving-artifact only", set.ObjectRoots)
	}
}

func assertRecoverySetCanonicalBytesEqual(t *testing.T, want, got recoveryset.RecoverySet) {
	t.Helper()
	wantJSON, err := want.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := got.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wantJSON, gotJSON) || !want.IdentityEqual(got) || want.FrontierDigest != got.FrontierDigest {
		t.Fatalf("reopened recovery frontier differs: bytes_equal=%t digest=%q/%q", bytes.Equal(wantJSON, gotJSON), want.FrontierDigest, got.FrontierDigest)
	}
}

func providerObservationDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}
