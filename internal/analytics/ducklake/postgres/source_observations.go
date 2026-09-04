package postgres

// Durable source-observation capture for native DuckLake builds.  The
// capture is deliberately a value-only envelope: source sessions and
// credentials never cross this package boundary.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	dbgen "github.com/flidai/leapview/internal/analytics/ducklake/postgres/internal/db"
	materialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	MaxSourceObservationCount               = 4096
	MaxSourceObservationEnvelopeBytes       = 8 << 20
	maxSourceObservationIDBytes             = 512
	maxSourceObservationTextBytes           = 4096
	maxSourceObservationSchema              = 16384
	maxSourceObservationTotalSchema         = 65536
	maxSourceObservationQueries             = 1 << 31
	maxSourceObservationRows          int64 = 1 << 62
	maxSourceObservationMillis        int64 = 7 * 24 * 60 * 60 * 1000
)

// SourceObservationCapture is the immutable exact-attempt row.  Envelope and
// marker bytes must already be canonical JSON; ContentDigest addresses the
// envelope bytes exactly.
type SourceObservationCapture struct {
	AttemptID           string
	CommitMarker        json.RawMessage
	ObservationEnvelope json.RawMessage
	ContentDigest       string
	CapturedAt          time.Time
	CreatedAt           time.Time
}

// Observations decodes the persisted envelope without performing any source
// I/O and returns defensive copies.
func (c SourceObservationCapture) Observations() ([]materialize.SourceObservation, error) {
	return DecodeSourceObservationEnvelope(c.ObservationEnvelope)
}

// SourceObservationWriter is implemented by the DuckLake control repository
// and consumed by the native physical build callback. A persisted capture is
// pre-acknowledgement evidence, never proof that the external snapshot commit
// succeeded; recovery must resolve the exact commit marker independently.
type SourceObservationWriter interface {
	RecordSourceObservationCapture(context.Context, SourceObservationCapture) (SourceObservationCapture, error)
}

var _ SourceObservationWriter = (*Repository)(nil)

type sourceObservationEnvelope struct {
	SchemaVersion int                         `json:"schema_version"`
	Observations  []sourceObservationDocument `json:"observations"`
}

type sourceObservationDocument struct {
	ID                 string                         `json:"id"`
	Schema             []semanticmodel.ColumnSchema   `json:"schema"`
	Revision           string                         `json:"revision,omitempty"`
	RevisionObserved   string                         `json:"revision_observed,omitempty"`
	FreshnessObserved  string                         `json:"freshness_observed,omitempty"`
	FreshnessEmpty     bool                           `json:"freshness_empty,omitempty"`
	SchemaFailure      materialize.ObservationFailure `json:"schema_failure,omitempty"`
	FreshnessFailure   materialize.ObservationFailure `json:"freshness_failure,omitempty"`
	ObservationQueries int                            `json:"observation_queries"`
	ObservationRows    int64                          `json:"observation_rows"`
	ObservationMillis  int64                          `json:"observation_millis"`
}

// NewSourceObservationCapture validates and canonicalizes one observation
// envelope for an exact marker/attempt identity.  The supplied capture time
// is persisted as UTC microsecond precision, matching PostgreSQL timestamptz.
func NewSourceObservationCapture(attemptID string, marker catalogartifact.CommitMarker, observations []materialize.SourceObservation, capturedAt time.Time) (SourceObservationCapture, error) {
	if !validCanonicalSourceObservationUUID(attemptID) {
		return SourceObservationCapture{}, ErrInvalid
	}
	normalized, err := marker.Normalize()
	if err != nil || normalized.AttemptID != attemptID {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation commit marker identity differs", ErrConflict)
	}
	markerJSON, err := normalized.CanonicalJSON()
	if err != nil || len(markerJSON) > catalogartifact.MaxCommitMarkerBytes {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation commit marker is invalid", ErrInvalid)
	}
	envelope, err := CanonicalSourceObservationEnvelope(observations)
	if err != nil {
		return SourceObservationCapture{}, err
	}
	if capturedAt.IsZero() || capturedAt.Location() != time.UTC || !capturedAt.Equal(capturedAt.UTC()) {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation capture time must be UTC", ErrInvalid)
	}
	capturedAt = capturedAt.Truncate(time.Microsecond)
	digest := sha256.Sum256(envelope)
	return SourceObservationCapture{
		AttemptID: attemptID, CommitMarker: append(json.RawMessage(nil), markerJSON...),
		ObservationEnvelope: envelope, ContentDigest: "sha256:" + hex.EncodeToString(digest[:]), CapturedAt: capturedAt,
	}, nil
}

// CanonicalSourceObservationEnvelope produces the bounded, deterministic
// envelope used for persistence and replay. Observations are sorted by source
// ID; no live source is opened or re-observed here.
func CanonicalSourceObservationEnvelope(observations []materialize.SourceObservation) ([]byte, error) {
	if err := validateSourceObservations(observations); err != nil {
		return nil, err
	}
	documents := make([]sourceObservationDocument, 0, len(observations))
	for _, observation := range observations {
		document := sourceObservationDocument{
			ID: observation.ID, Schema: append([]semanticmodel.ColumnSchema(nil), observation.Schema...), Revision: observation.Revision,
			FreshnessEmpty: observation.FreshnessEmpty, SchemaFailure: observation.SchemaFailure, FreshnessFailure: observation.FreshnessFailure,
			ObservationQueries: observation.ObservationQueries, ObservationRows: observation.ObservationRows, ObservationMillis: observation.ObservationMillis,
		}
		if !observation.RevisionObserved.IsZero() {
			document.RevisionObserved = observation.RevisionObserved.UTC().Format(time.RFC3339Nano)
		}
		if !observation.FreshnessObserved.IsZero() {
			document.FreshnessObserved = observation.FreshnessObserved.UTC().Format(time.RFC3339Nano)
		}
		documents = append(documents, document)
	}
	sort.SliceStable(documents, func(i, j int) bool { return documents[i].ID < documents[j].ID })
	encoded, err := json.Marshal(sourceObservationEnvelope{SchemaVersion: 1, Observations: documents})
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxSourceObservationEnvelopeBytes {
		return nil, fmt.Errorf("%w: source observation envelope exceeds %d bytes", ErrInvalid, MaxSourceObservationEnvelopeBytes)
	}
	return encoded, nil
}

func validateSourceObservations(observations []materialize.SourceObservation) error {
	if len(observations) > MaxSourceObservationCount {
		return fmt.Errorf("%w: source observations exceed maximum count", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(observations))
	columns := 0
	textBytes := 0
	addText := func(value string) error {
		if len(value) > MaxSourceObservationEnvelopeBytes-textBytes {
			return fmt.Errorf("%w: source observation text exceeds aggregate bound", ErrInvalid)
		}
		textBytes += len(value)
		return nil
	}
	for _, observation := range observations {
		if err := observationText(observation.ID, "source observation id", maxSourceObservationIDBytes); err != nil {
			return err
		}
		if err := addText(observation.ID); err != nil {
			return err
		}
		if _, ok := seen[observation.ID]; ok {
			return fmt.Errorf("%w: duplicate source observation id %q", ErrConflict, observation.ID)
		}
		seen[observation.ID] = struct{}{}
		if observation.Revision != "" {
			if err := observationText(observation.Revision, "source observation revision", maxSourceObservationTextBytes); err != nil {
				return err
			}
			if err := addText(observation.Revision); err != nil {
				return err
			}
		}
		if observation.ObservationQueries < 0 || observation.ObservationQueries > maxSourceObservationQueries || observation.ObservationRows < 0 || observation.ObservationRows > maxSourceObservationRows || observation.ObservationMillis < 0 || observation.ObservationMillis > maxSourceObservationMillis {
			return fmt.Errorf("%w: source observation counters are outside bounds", ErrInvalid)
		}
		if len(observation.Schema) > maxSourceObservationSchema || len(observation.Schema) > maxSourceObservationTotalSchema-columns {
			return fmt.Errorf("%w: source observation schema exceeds bounds", ErrInvalid)
		}
		columns += len(observation.Schema)
		for _, column := range observation.Schema {
			if strings.TrimSpace(column.Name) == "" || strings.TrimSpace(column.PhysicalType) == "" {
				return fmt.Errorf("%w: source observation schema column is incomplete", ErrInvalid)
			}
			for label, value := range map[string]string{"column name": column.Name, "column physical type": column.PhysicalType, "column default": column.Default, "column comment": column.Comment} {
				if value == "" && (label == "column default" || label == "column comment") {
					continue
				}
				if err := observationText(value, label, maxSourceObservationTextBytes); err != nil {
					return err
				}
				if err := addText(value); err != nil {
					return err
				}
			}
			if column.Ordinal < 0 {
				return fmt.Errorf("%w: source observation column ordinal cannot be negative", ErrInvalid)
			}
		}
		for label, observedAt := range map[string]time.Time{"revision observed": observation.RevisionObserved, "freshness observed": observation.FreshnessObserved} {
			if !observedAt.IsZero() && (observedAt.Location() != time.UTC || !observedAt.Equal(observedAt.UTC())) {
				return fmt.Errorf("%w: source observation %s timestamp must be UTC", ErrInvalid, label)
			}
		}
		if err := validateObservationFailure(observation.SchemaFailure, "schema"); err != nil {
			return err
		}
		if err := validateObservationFailure(observation.FreshnessFailure, "freshness"); err != nil {
			return err
		}
	}
	return nil
}

func observationText(value, label string, max int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%w: %s is invalid", ErrInvalid, label)
	}
	return nil
}

func validateObservationFailure(value materialize.ObservationFailure, label string) error {
	if value == "" || value == materialize.ObservationUnavailable || value == materialize.ObservationTimeout || value == materialize.ObservationBounds {
		return nil
	}
	return fmt.Errorf("%w: source observation %s failure is unknown", ErrInvalid, label)
}

func normalizeSourceObservationCapture(in SourceObservationCapture) (SourceObservationCapture, error) {
	if !validCanonicalSourceObservationUUID(in.AttemptID) {
		return SourceObservationCapture{}, ErrInvalid
	}
	marker, err := catalogartifact.DecodeCommitMarker(in.CommitMarker)
	if err != nil || marker.AttemptID != in.AttemptID {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation marker identity differs", ErrConflict)
	}
	markerJSON, err := marker.CanonicalJSON()
	if err != nil || !bytes.Equal([]byte(markerJSON), in.CommitMarker) {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation marker is not canonical", ErrInvalid)
	}
	if len(in.ObservationEnvelope) == 0 || len(in.ObservationEnvelope) > MaxSourceObservationEnvelopeBytes {
		return SourceObservationCapture{}, ErrInvalid
	}
	canonicalEnvelope, err := canonicalizeStoredEnvelope(in.ObservationEnvelope)
	if err != nil {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation envelope is invalid", ErrInvalid)
	}
	// Callers writing a capture must provide the exact canonical envelope. A
	// PostgreSQL jsonb round-trip may reorder object keys; Load normalizes that
	// representation before returning it, while this boundary remains strict.
	if !bytes.Equal(canonicalEnvelope, in.ObservationEnvelope) {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation envelope is not canonical", ErrInvalid)
	}
	digest := sha256.Sum256(canonicalEnvelope)
	expectedDigest := "sha256:" + hex.EncodeToString(digest[:])
	if in.ContentDigest != expectedDigest {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation content digest differs", ErrConflict)
	}
	if in.CapturedAt.IsZero() || in.CapturedAt.Location() != time.UTC || !in.CapturedAt.Equal(in.CapturedAt.UTC()) {
		return SourceObservationCapture{}, ErrInvalid
	}
	if !in.CreatedAt.IsZero() {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation creation time is database-owned", ErrInvalid)
	}
	in.CommitMarker, in.ObservationEnvelope = append(json.RawMessage(nil), markerJSON...), append(json.RawMessage(nil), canonicalEnvelope...)
	in.CapturedAt = in.CapturedAt.Truncate(time.Microsecond)
	return in, nil
}

func canonicalizeStoredEnvelope(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxSourceObservationEnvelopeBytes {
		return nil, ErrInvalid
	}
	observations, err := decodeSourceObservationEnvelope(raw)
	if err != nil {
		return nil, err
	}
	return CanonicalSourceObservationEnvelope(observations)
}

// DecodeSourceObservationEnvelope strictly decodes one canonical envelope and
// returns defensive domain values. It is intended for recovery/qualification;
// callers must never re-open a source merely to reconstruct this evidence.
func DecodeSourceObservationEnvelope(raw []byte) ([]materialize.SourceObservation, error) {
	if len(raw) == 0 || len(raw) > MaxSourceObservationEnvelopeBytes {
		return nil, ErrInvalid
	}
	observations, err := decodeSourceObservationEnvelope(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := CanonicalSourceObservationEnvelope(observations)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, fmt.Errorf("%w: source observation envelope is not canonical", ErrInvalid)
	}
	return cloneSourceObservations(observations), nil
}

func decodeSourceObservationEnvelope(raw []byte) ([]materialize.SourceObservation, error) {
	var envelope sourceObservationEnvelope
	if err := strictjson.DecodeWithOptions(raw, &envelope, strictjson.Options{MaxBytes: MaxSourceObservationEnvelopeBytes, MaxDepth: 32}); err != nil || envelope.SchemaVersion != 1 {
		return nil, ErrInvalid
	}
	observations := make([]materialize.SourceObservation, 0, len(envelope.Observations))
	for _, document := range envelope.Observations {
		observation := materialize.SourceObservation{ID: document.ID, Schema: append([]semanticmodel.ColumnSchema(nil), document.Schema...), Revision: document.Revision, FreshnessEmpty: document.FreshnessEmpty, SchemaFailure: document.SchemaFailure, FreshnessFailure: document.FreshnessFailure, ObservationQueries: document.ObservationQueries, ObservationRows: document.ObservationRows, ObservationMillis: document.ObservationMillis}
		var err error
		if document.RevisionObserved != "" {
			observation.RevisionObserved, err = parseObservationTime(document.RevisionObserved)
			if err != nil {
				return nil, err
			}
		}
		if document.FreshnessObserved != "" {
			observation.FreshnessObserved, err = parseObservationTime(document.FreshnessObserved)
			if err != nil {
				return nil, err
			}
		}
		observations = append(observations, observation)
	}
	if err := validateSourceObservations(observations); err != nil {
		return nil, err
	}
	return observations, nil
}

func cloneSourceObservations(observations []materialize.SourceObservation) []materialize.SourceObservation {
	if observations == nil {
		return nil
	}
	result := make([]materialize.SourceObservation, len(observations))
	for i, observation := range observations {
		result[i] = observation
		result[i].Schema = append([]semanticmodel.ColumnSchema(nil), observation.Schema...)
		for column := range result[i].Schema {
			if result[i].Schema[column].Nullable != nil {
				nullable := *result[i].Schema[column].Nullable
				result[i].Schema[column].Nullable = &nullable
			}
		}
	}
	return result
}

func parseObservationTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || !parsed.Equal(parsed.UTC()) {
		return time.Time{}, fmt.Errorf("%w: source observation timestamp is not canonical UTC", ErrInvalid)
	}
	return parsed, nil
}

func (r *Repository) RecordSourceObservationCapture(ctx context.Context, in SourceObservationCapture) (SourceObservationCapture, error) {
	if r == nil || r.db == nil {
		return SourceObservationCapture{}, ErrInvalid
	}
	return RecordSourceObservationCapture(ctx, r.db, in)
}

func RecordSourceObservationCapture(ctx context.Context, db DBTX, in SourceObservationCapture) (SourceObservationCapture, error) {
	if db == nil {
		return SourceObservationCapture{}, ErrInvalid
	}
	normalized, err := normalizeSourceObservationCapture(in)
	if err != nil {
		return SourceObservationCapture{}, err
	}
	// A repository handle owns the short control-plane transaction. A caller-
	// owned transaction reaches recordSourceObservationCapture directly. In
	// both cases the attempt row remains locked from the state check through
	// the immutable insert, fencing concurrent termination/reconciliation.
	if b, ok := db.(beginner); ok {
		tx, err := b.Begin(ctx)
		if err != nil {
			return SourceObservationCapture{}, err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		got, err := recordSourceObservationCapture(ctx, tx, normalized)
		if err != nil {
			return SourceObservationCapture{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SourceObservationCapture{}, err
		}
		committed = true
		return got, nil
	}
	return recordSourceObservationCapture(ctx, db, normalized)
}

func recordSourceObservationCapture(ctx context.Context, db DBTX, normalized SourceObservationCapture) (SourceObservationCapture, error) {
	attempt, err := lockDeliveryAttemptForObservation(ctx, db, normalized.AttemptID)
	if err != nil {
		return SourceObservationCapture{}, err
	}
	marker, _ := catalogartifact.DecodeCommitMarker(normalized.CommitMarker)
	if marker.RequestDigest != attempt.RequestDigest || marker.PlanDigest != attempt.PlanDigest || marker.PhysicalPoolID != attempt.PhysicalPoolID || marker.LeaseEpoch != attempt.FencingEpoch {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation capture does not match attempt identity", ErrConflict)
	}
	if attempt.State != "running" && attempt.State != "committed" {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation capture cannot attach to attempt state %s", ErrConflict, attempt.State)
	}
	if err := querygen(db).InsertSourceObservationCapture(ctx, dbgen.InsertSourceObservationCaptureParams{AttemptID: pgUUID(normalized.AttemptID), CommitMarker: normalized.CommitMarker, ObservationEnvelope: normalized.ObservationEnvelope, ContentDigest: normalized.ContentDigest, CapturedAt: pgtype.Timestamptz{Time: normalized.CapturedAt, Valid: true}}); err != nil {
		return SourceObservationCapture{}, err
	}
	got, err := LoadSourceObservationCapture(ctx, db, normalized.AttemptID)
	if err != nil {
		return SourceObservationCapture{}, err
	}
	if !markersEqual(string(got.CommitMarker), string(normalized.CommitMarker)) || !bytes.Equal(got.ObservationEnvelope, normalized.ObservationEnvelope) || got.ContentDigest != normalized.ContentDigest || !got.CapturedAt.Equal(normalized.CapturedAt) {
		return SourceObservationCapture{}, fmt.Errorf("%w: source observation capture %q", ErrConflict, normalized.AttemptID)
	}
	return got, nil
}

func (r *Repository) LoadSourceObservationCapture(ctx context.Context, attemptID string) (SourceObservationCapture, error) {
	if r == nil || r.db == nil {
		return SourceObservationCapture{}, ErrInvalid
	}
	return LoadSourceObservationCapture(ctx, r.db, attemptID)
}

func LoadSourceObservationCapture(ctx context.Context, db DBTX, attemptID string) (SourceObservationCapture, error) {
	if db == nil || !validCanonicalSourceObservationUUID(attemptID) {
		return SourceObservationCapture{}, ErrInvalid
	}
	row, err := querygen(db).GetSourceObservationCapture(ctx, pgUUID(attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceObservationCapture{}, ErrNotFound
	}
	if err != nil {
		return SourceObservationCapture{}, err
	}
	marker, err := catalogartifact.DecodeCommitMarker(row.CommitMarker)
	if err != nil {
		return SourceObservationCapture{}, fmt.Errorf("%w: stored source observation marker is invalid", ErrConflict)
	}
	attempt, err := getDeliveryAttemptForObservation(ctx, db, attemptID)
	if err != nil {
		return SourceObservationCapture{}, err
	}
	if marker.AttemptID != attemptID || marker.RequestDigest != attempt.RequestDigest || marker.PlanDigest != attempt.PlanDigest || marker.PhysicalPoolID != attempt.PhysicalPoolID || marker.LeaseEpoch != attempt.FencingEpoch {
		return SourceObservationCapture{}, fmt.Errorf("%w: stored source observation capture does not match attempt identity", ErrConflict)
	}
	markerJSON, err := marker.CanonicalJSON()
	if err != nil {
		return SourceObservationCapture{}, fmt.Errorf("%w: stored source observation marker is invalid", ErrConflict)
	}
	envelope, err := canonicalizeStoredEnvelope(row.ObservationEnvelope)
	if err != nil {
		return SourceObservationCapture{}, fmt.Errorf("%w: stored source observation envelope is invalid", ErrConflict)
	}
	capturedAt, createdAt := tsTime(row.CapturedAt), tsTime(row.CreatedAt)
	if capturedAt.IsZero() || createdAt.IsZero() || !capturedAt.Equal(capturedAt.UTC()) || !createdAt.Equal(createdAt.UTC()) {
		return SourceObservationCapture{}, fmt.Errorf("%w: stored source observation timestamps are invalid", ErrConflict)
	}
	digest := sha256.Sum256(envelope)
	expectedDigest := "sha256:" + hex.EncodeToString(digest[:])
	if row.ContentDigest != expectedDigest {
		return SourceObservationCapture{}, fmt.Errorf("%w: stored source observation digest is invalid", ErrConflict)
	}
	return SourceObservationCapture{AttemptID: row.AttemptID, CommitMarker: json.RawMessage(markerJSON), ObservationEnvelope: envelope, ContentDigest: row.ContentDigest, CapturedAt: capturedAt, CreatedAt: createdAt}, nil
}

type deliveryAttemptObservationIdentity struct {
	AttemptID      string
	RequestDigest  string
	PlanDigest     string
	PhysicalPoolID string
	CatalogID      string
	FencingEpoch   int64
	State          string
}

func lockDeliveryAttemptForObservation(ctx context.Context, db DBTX, attemptID string) (deliveryAttemptObservationIdentity, error) {
	if db == nil || !validCanonicalSourceObservationUUID(attemptID) {
		return deliveryAttemptObservationIdentity{}, ErrInvalid
	}
	row, err := querygen(db).LockDeliveryAttemptForObservation(ctx, pgUUID(attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryAttemptObservationIdentity{}, ErrNotFound
	}
	if err != nil {
		return deliveryAttemptObservationIdentity{}, err
	}
	return deliveryAttemptObservationIdentity{AttemptID: row.AttemptID, RequestDigest: row.RequestDigest, PlanDigest: row.PlanDigest, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, FencingEpoch: row.FencingEpoch, State: row.State}, nil
}

func getDeliveryAttemptForObservation(ctx context.Context, db DBTX, attemptID string) (deliveryAttemptObservationIdentity, error) {
	if db == nil || !validCanonicalSourceObservationUUID(attemptID) {
		return deliveryAttemptObservationIdentity{}, ErrInvalid
	}
	row, err := querygen(db).GetDeliveryAttemptForObservation(ctx, pgUUID(attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryAttemptObservationIdentity{}, ErrNotFound
	}
	if err != nil {
		return deliveryAttemptObservationIdentity{}, err
	}
	return deliveryAttemptObservationIdentity{AttemptID: row.AttemptID, RequestDigest: row.RequestDigest, PlanDigest: row.PlanDigest, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, FencingEpoch: row.FencingEpoch, State: row.State}, nil
}

func validCanonicalSourceObservationUUID(value string) bool {
	return validUUID(value) && value == strings.ToLower(value)
}
