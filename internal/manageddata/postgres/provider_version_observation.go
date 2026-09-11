package postgres

import (
	"context"
	"errors"
	"fmt"

	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RecordProviderVersionObservation persists the exact provider response from a
// successful managed-data write. The observation remains an unsigned input to
// a future recovery capture; recording it does not create Manifest v2 evidence.
func (r *Repository) RecordProviderVersionObservation(ctx context.Context, observation storage.ProviderVersionObservation) (storage.ProviderVersionObservation, error) {
	if err := storage.ValidateProviderVersionObservation(observation); err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	tx, owned, err := r.beginTransition(ctx)
	if err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(context.Background()) }()
	}
	stored, err := recordProviderVersionObservation(ctx, tx, observation)
	if err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return storage.ProviderVersionObservation{}, err
		}
	}
	return stored, nil
}

// ProviderVersionObservation loads one immutable observation by its logical
// provider object identity and validates every stored field again.
func (r *Repository) ProviderVersionObservation(ctx context.Context, profileID, objectKey string) (storage.ProviderVersionObservation, error) {
	db, err := requireDB(r)
	if err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	return readProviderVersionObservation(ctx, db, profileID, objectKey)
}

func recordProviderVersionObservation(ctx context.Context, db DBTX, observation storage.ProviderVersionObservation) (storage.ProviderVersionObservation, error) {
	queries := manageddb.New(db)
	profile := observation.Profile
	if err := queries.InsertProviderObservationProfile(ctx, manageddb.InsertProviderObservationProfileParams{
		ProfileID: profile.ProfileID, Implementation: profile.Implementation,
		AccountIdentity: profile.AccountIdentity, Endpoint: profile.Endpoint,
		Region: profile.Region, Bucket: profile.Bucket, Namespace: profile.Namespace,
	}); err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	storedProfile, err := queries.GetProviderObservationProfile(ctx, profile.ProfileID)
	if err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	if (storage.ProviderProfileIdentity{
		ProfileID: storedProfile.ProfileID, Implementation: storedProfile.Implementation,
		AccountIdentity: storedProfile.AccountIdentity, Endpoint: storedProfile.Endpoint,
		Region: storedProfile.Region, Bucket: storedProfile.Bucket, Namespace: storedProfile.Namespace,
	}) != profile {
		return storage.ProviderVersionObservation{}, fmt.Errorf("%w: provider profile identity resolves to different configuration", storage.ErrObservationConflict)
	}
	if err := queries.InsertProviderVersionObservation(ctx, manageddb.InsertProviderVersionObservationParams{
		ProfileID: profile.ProfileID, ObjectKey: observation.ObjectKey,
		VersionID: observation.VersionID, Sha256: observation.SHA256, SizeBytes: observation.Size,
		CapturedAt: pgtype.Timestamptz{Time: observation.CapturedAt, Valid: true},
	}); err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	stored, err := readProviderVersionObservation(ctx, db, profile.ProfileID, observation.ObjectKey)
	if err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	if !sameProviderVersionObservation(stored, observation) {
		return storage.ProviderVersionObservation{}, fmt.Errorf("%w: provider object identity already has different immutable metadata", storage.ErrObservationConflict)
	}
	return stored, nil
}

func readProviderVersionObservation(ctx context.Context, db DBTX, profileID, objectKey string) (storage.ProviderVersionObservation, error) {
	if profileID == "" || objectKey == "" {
		return storage.ProviderVersionObservation{}, storage.ErrProviderVersion
	}
	row, err := manageddb.New(db).GetProviderVersionObservation(ctx, manageddb.GetProviderVersionObservationParams{ProfileID: profileID, ObjectKey: objectKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return storage.ProviderVersionObservation{}, ErrNotFound
	}
	if err != nil {
		return storage.ProviderVersionObservation{}, err
	}
	if !row.CapturedAt.Valid {
		return storage.ProviderVersionObservation{}, fmt.Errorf("%w: stored capture timestamp is missing", storage.ErrProviderVersion)
	}
	observation := storage.ProviderVersionObservation{
		Profile: storage.ProviderProfileIdentity{
			ProfileID: row.ProfileID, Implementation: row.Implementation,
			AccountIdentity: row.AccountIdentity, Endpoint: row.Endpoint,
			Region: row.Region, Bucket: row.Bucket, Namespace: row.Namespace,
		},
		ObjectKey: row.ObjectKey, VersionID: row.VersionID, SHA256: row.Sha256,
		Size: row.SizeBytes, CapturedAt: row.CapturedAt.Time.UTC(),
	}
	if err := storage.ValidateProviderVersionObservation(observation); err != nil {
		return storage.ProviderVersionObservation{}, fmt.Errorf("stored provider-version observation: %w", err)
	}
	return observation, nil
}

func sameProviderVersionObservation(left, right storage.ProviderVersionObservation) bool {
	return left.Profile == right.Profile && left.ObjectKey == right.ObjectKey &&
		left.VersionID == right.VersionID && left.SHA256 == right.SHA256 &&
		left.Size == right.Size && left.CapturedAt.Equal(right.CapturedAt)
}
