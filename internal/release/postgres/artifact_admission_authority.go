package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/release/artifactadmission"
	releasedb "github.com/flidai/leapview/internal/release/postgres/internal/db"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrArtifactAdmissionInvalid        = errors.New("invalid OCI artifact admission")
	ErrArtifactAdmissionNotFound       = errors.New("OCI artifact admission not found")
	ErrArtifactAdmissionConflict       = errors.New("OCI artifact admission conflict")
	ErrArtifactAdmissionDigestMismatch = errors.New("OCI artifact admission digest mismatch")
	ErrArtifactAdmissionRevoked        = errors.New("OCI artifact admission revoked")
)

// PublishArtifactAdmission appends one maintenance-owned admission record.
// An exact replay returns the same transition identity; no existing record is
// ever replaced.
func (r *Repository) PublishArtifactAdmission(ctx context.Context, admission artifactadmission.Admission) (transitionpreflight.ArtifactIdentity, error) {
	if r == nil || r.db == nil {
		return transitionpreflight.ArtifactIdentity{}, ErrArtifactAdmissionInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return transitionpreflight.ArtifactIdentity{}, errors.New("release PostgreSQL database does not support transactions")
	}
	var identity transitionpreflight.ArtifactIdentity
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		identity, err = publishArtifactAdmission(ctx, tx, admission)
		return err
	})
	return identity, err
}

func publishArtifactAdmission(ctx context.Context, tx pgx.Tx, admission artifactadmission.Admission) (transitionpreflight.ArtifactIdentity, error) {
	document, err := admission.CanonicalJSON()
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, fmt.Errorf("%w: %v", ErrArtifactAdmissionInvalid, err)
	}
	canonical, err := artifactadmission.ParseCanonical(document)
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, fmt.Errorf("%w: %v", ErrArtifactAdmissionInvalid, err)
	}
	digest, err := canonical.Digest()
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, fmt.Errorf("%w: %v", ErrArtifactAdmissionInvalid, err)
	}
	q := releasedb.New(tx)
	inserted, err := q.InsertOCIArtifactAdmission(ctx, releasedb.InsertOCIArtifactAdmissionParams{
		ArtifactReference: canonical.Release.Image, RepositoryIdentity: canonical.Repository,
		OciDigest: canonical.OCIDigest, AdmissionVersion: canonical.Version,
		AdmissionDigest: digest, AdmissionBytes: document,
		AdmittedAt: pgtype.Timestamptz{Time: canonical.AdmittedAt, Valid: true},
	})
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, mapArtifactAdmissionDatabaseError(err)
	}
	row, err := q.GetOCIArtifactAdmission(ctx, canonical.Release.Image)
	if errors.Is(err, pgx.ErrNoRows) {
		if inserted == 0 {
			return transitionpreflight.ArtifactIdentity{}, ErrArtifactAdmissionConflict
		}
		return transitionpreflight.ArtifactIdentity{}, ErrArtifactAdmissionNotFound
	}
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, err
	}
	stored, storedDigest, err := readArtifactAdmissionRow(row)
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, err
	}
	if storedDigest != digest || !bytes.Equal(row.AdmissionBytes, document) || stored != canonical {
		return transitionpreflight.ArtifactIdentity{}, ErrArtifactAdmissionConflict
	}
	return stored.ArtifactIdentity()
}

// ResolveArtifact implements artifactadmission.Authority. It has no tag,
// latest, or caller-projection fallback.
func (r *Repository) ResolveArtifact(ctx context.Context, artifactReference string) (transitionpreflight.ArtifactIdentity, error) {
	if r == nil || r.db == nil {
		return transitionpreflight.ArtifactIdentity{}, ErrArtifactAdmissionInvalid
	}
	if err := validateExactArtifactReference(artifactReference); err != nil {
		return transitionpreflight.ArtifactIdentity{}, err
	}
	row, err := releasedb.New(r.db).GetOCIArtifactAdmission(ctx, artifactReference)
	if errors.Is(err, pgx.ErrNoRows) {
		return transitionpreflight.ArtifactIdentity{}, ErrArtifactAdmissionNotFound
	}
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, err
	}
	admission, _, err := readArtifactAdmissionRow(row)
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, err
	}
	return admission.ArtifactIdentity()
}

// RevokeArtifactAdmission appends a revocation fact without modifying the
// original admission. It exists on the maintenance repository surface only;
// the runtime role has no INSERT privilege.
func (r *Repository) RevokeArtifactAdmission(ctx context.Context, artifactReference, reason string, revokedAt time.Time) error {
	if r == nil || r.db == nil {
		return ErrArtifactAdmissionInvalid
	}
	if err := validateExactArtifactReference(artifactReference); err != nil {
		return err
	}
	if err := validateRevocation(reason, revokedAt); err != nil {
		return err
	}
	b, ok := r.db.(beginner)
	if !ok {
		return errors.New("release PostgreSQL database does not support transactions")
	}
	return pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		q := releasedb.New(tx)
		row, err := q.GetOCIArtifactAdmission(ctx, artifactReference)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrArtifactAdmissionNotFound
		}
		if err != nil {
			return err
		}
		_, digest, readErr := readArtifactAdmissionRowIgnoringRevocation(row)
		if readErr != nil {
			return readErr
		}
		canonicalTime := revokedAt.UTC().Round(0).Truncate(time.Microsecond)
		if _, err := q.InsertOCIArtifactAdmissionRevocation(ctx, releasedb.InsertOCIArtifactAdmissionRevocationParams{
			ArtifactReference: artifactReference, AdmissionDigest: digest,
			RevokedAt: pgtype.Timestamptz{Time: canonicalTime, Valid: true}, Reason: reason,
		}); err != nil {
			return mapArtifactAdmissionDatabaseError(err)
		}
		stored, err := q.GetOCIArtifactAdmission(ctx, artifactReference)
		if err != nil {
			return err
		}
		if stored.RevokedAdmissionDigest == nil || *stored.RevokedAdmissionDigest != digest ||
			!stored.RevokedAt.Valid || !stored.RevokedAt.Time.Equal(canonicalTime) ||
			stored.RevocationReason == nil || *stored.RevocationReason != reason {
			return ErrArtifactAdmissionConflict
		}
		return nil
	})
}

func readArtifactAdmissionRow(row releasedb.GetOCIArtifactAdmissionRow) (artifactadmission.Admission, string, error) {
	admission, digest, err := readArtifactAdmissionRowIgnoringRevocation(row)
	if err != nil {
		return artifactadmission.Admission{}, "", err
	}
	if row.RevokedAdmissionDigest != nil || row.RevokedAt.Valid || row.RevocationReason != nil {
		if row.RevokedAdmissionDigest == nil || *row.RevokedAdmissionDigest != digest || !row.RevokedAt.Valid || row.RevocationReason == nil {
			return artifactadmission.Admission{}, "", ErrArtifactAdmissionDigestMismatch
		}
		return artifactadmission.Admission{}, "", ErrArtifactAdmissionRevoked
	}
	return admission, digest, nil
}

func readArtifactAdmissionRowIgnoringRevocation(row releasedb.GetOCIArtifactAdmissionRow) (artifactadmission.Admission, string, error) {
	admission, err := artifactadmission.ParseCanonical(row.AdmissionBytes)
	if err != nil {
		return artifactadmission.Admission{}, "", fmt.Errorf("%w: %v", ErrArtifactAdmissionInvalid, err)
	}
	digest, err := admission.Digest()
	if err != nil {
		return artifactadmission.Admission{}, "", fmt.Errorf("%w: %v", ErrArtifactAdmissionInvalid, err)
	}
	if row.ArtifactReference != admission.Release.Image || row.RepositoryIdentity != admission.Repository ||
		row.OciDigest != admission.OCIDigest || row.AdmissionVersion != admission.Version ||
		row.AdmissionDigest != digest || !row.AdmittedAt.Valid || !row.AdmittedAt.Time.Equal(admission.AdmittedAt) {
		return artifactadmission.Admission{}, "", ErrArtifactAdmissionDigestMismatch
	}
	return admission, digest, nil
}

func validateExactArtifactReference(value string) error {
	if err := artifactadmission.ValidateReference(value); err != nil {
		return fmt.Errorf("%w: artifact reference: %v", ErrArtifactAdmissionInvalid, err)
	}
	return nil
}

func validateRevocation(reason string, revokedAt time.Time) error {
	if reason == "" || strings.TrimSpace(reason) != reason || len(reason) > 1024 ||
		!utf8.ValidString(reason) || strings.IndexFunc(reason, unicode.IsControl) >= 0 || revokedAt.IsZero() {
		return ErrArtifactAdmissionInvalid
	}
	return nil
}

func mapArtifactAdmissionDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrArtifactAdmissionConflict
		case "23503", "23514", "22001":
			return ErrArtifactAdmissionInvalid
		}
	}
	return err
}

var _ artifactadmission.Authority = (*Repository)(nil)
