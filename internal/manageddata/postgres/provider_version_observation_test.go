package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This validates recovery evidence binding. It does not prove successful physical disaster recovery.
func TestProviderVersionObservationPersistenceQualification(t *testing.T) {
	pool, _, _, _ := openManagedDataTestPool(t)
	repository := New(pool)

	t.Run("insert retry conflicts and malformed input", func(t *testing.T) {
		observation := persistedProviderObservation("profile-persistence", "project-a/objects/a", "version-a")
		first, err := repository.RecordProviderVersionObservation(t.Context(), observation)
		if err != nil {
			t.Fatal(err)
		}
		second, err := repository.RecordProviderVersionObservation(t.Context(), observation)
		if err != nil {
			t.Fatalf("identical retry: %v", err)
		}
		if !sameProviderVersionObservation(first, observation) || !sameProviderVersionObservation(second, observation) {
			t.Fatalf("persisted observations differ: first=%#v second=%#v", first, second)
		}

		for name, mutate := range map[string]func(*storage.ProviderVersionObservation){
			"version": func(candidate *storage.ProviderVersionObservation) { candidate.VersionID = "version-conflict" },
			"digest":  func(candidate *storage.ProviderVersionObservation) { candidate.SHA256 = strings.Repeat("b", 64) },
			"profile": func(candidate *storage.ProviderVersionObservation) { candidate.Profile.Bucket = "different-bucket" },
		} {
			t.Run("conflicting "+name, func(t *testing.T) {
				candidate := observation
				mutate(&candidate)
				if _, err := repository.RecordProviderVersionObservation(t.Context(), candidate); !errors.Is(err, storage.ErrObservationConflict) {
					t.Fatalf("conflict error = %v", err)
				}
				stored, err := repository.ProviderVersionObservation(t.Context(), observation.Profile.ProfileID, observation.ObjectKey)
				if err != nil || !sameProviderVersionObservation(stored, observation) {
					t.Fatalf("conflict replaced winner: stored=%#v err=%v", stored, err)
				}
			})
		}

		malformed := observation
		malformed.ObjectKey = ""
		if _, err := repository.RecordProviderVersionObservation(t.Context(), malformed); !errors.Is(err, storage.ErrProviderVersion) {
			t.Fatalf("malformed observation error = %v", err)
		}
	})

	t.Run("caller transaction rollback leaves no partial record", func(t *testing.T) {
		observation := persistedProviderObservation("profile-rollback", "project-a/objects/rollback", "version-rollback")
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.WithTx(tx).RecordProviderVersionObservation(t.Context(), observation); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err := tx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ProviderVersionObservation(t.Context(), observation.Profile.ProfileID, observation.ObjectKey); !errors.Is(err, ErrNotFound) {
			t.Fatalf("rolled-back observation read = %v", err)
		}
		var profiles int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM managed_data.provider_observation_profile WHERE profile_id=$1`, observation.Profile.ProfileID).Scan(&profiles); err != nil {
			t.Fatal(err)
		}
		if profiles != 0 {
			t.Fatalf("rollback left %d provider profile rows", profiles)
		}
	})

	t.Run("concurrent identical and conflicting inserts", func(t *testing.T) {
		identical := persistedProviderObservation("profile-concurrent-identical", "project-a/objects/identical", "version-identical")
		errs := concurrentlyRecordProviderObservations(t, repository, []storage.ProviderVersionObservation{identical, identical, identical, identical})
		for _, err := range errs {
			if err != nil {
				t.Fatalf("concurrent identical insert = %v", err)
			}
		}

		left := persistedProviderObservation("profile-concurrent-conflict", "project-a/objects/conflict", "version-left")
		right := left
		right.VersionID = "version-right"
		right.CapturedAt = right.CapturedAt.Add(time.Microsecond)
		errs = concurrentlyRecordProviderObservations(t, repository, []storage.ProviderVersionObservation{left, right})
		var succeeded, conflicted int
		for _, err := range errs {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, storage.ErrObservationConflict):
				conflicted++
			default:
				t.Fatalf("concurrent conflict error = %v", err)
			}
		}
		if succeeded != 1 || conflicted != 1 {
			t.Fatalf("concurrent conflict outcomes succeeded=%d conflicted=%d", succeeded, conflicted)
		}
		stored, err := repository.ProviderVersionObservation(t.Context(), left.Profile.ProfileID, left.ObjectKey)
		if err != nil {
			t.Fatal(err)
		}
		if !sameProviderVersionObservation(stored, left) && !sameProviderVersionObservation(stored, right) {
			t.Fatalf("concurrent winner is corrupted: %#v", stored)
		}
	})

	t.Run("database mutation is blocked", func(t *testing.T) {
		observation := persistedProviderObservation("profile-immutable", "project-a/objects/immutable", "version-immutable")
		if _, err := repository.RecordProviderVersionObservation(t.Context(), observation); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `UPDATE managed_data.provider_version_observation SET version_id='replacement' WHERE profile_id=$1 AND object_key=$2`, observation.Profile.ProfileID, observation.ObjectKey); err == nil {
			t.Fatal("runtime role updated an immutable observation")
		}
	})

	t.Run("restart readback", func(t *testing.T) {
		observation := persistedProviderObservation("profile-restart", "project-a/objects/restart", "version-restart")
		if _, err := repository.RecordProviderVersionObservation(t.Context(), observation); err != nil {
			t.Fatal(err)
		}
		connectionString := pool.Config().ConnString()
		pool.Close()
		reopened, err := pgxpool.New(t.Context(), connectionString)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		stored, err := New(reopened).ProviderVersionObservation(t.Context(), observation.Profile.ProfileID, observation.ObjectKey)
		if err != nil || !sameProviderVersionObservation(stored, observation) {
			t.Fatalf("restart readback: stored=%#v err=%v", stored, err)
		}
	})
}

func persistedProviderObservation(profileID, objectKey, versionID string) storage.ProviderVersionObservation {
	return storage.ProviderVersionObservation{
		Profile: storage.ProviderProfileIdentity{
			ProfileID: profileID, Implementation: "s3", AccountIdentity: "qualification-account",
			Endpoint: "https://s3.example.test", Region: "test-region-1", Bucket: "qualification-bucket", Namespace: "project-a",
		},
		ObjectKey: objectKey, VersionID: versionID, SHA256: strings.Repeat("a", 64), Size: 123,
		CapturedAt: time.Date(2026, 9, 10, 10, 0, 0, 123000, time.UTC),
	}
}

func concurrentlyRecordProviderObservations(t *testing.T, repository *Repository, observations []storage.ProviderVersionObservation) []error {
	t.Helper()
	start := make(chan struct{})
	errs := make([]error, len(observations))
	var group sync.WaitGroup
	for index, observation := range observations {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, errs[index] = repository.RecordProviderVersionObservation(context.Background(), observation)
		}()
	}
	close(start)
	group.Wait()
	return errs
}
