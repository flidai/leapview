// Package sqlite persists public API idempotency records and execution leases.
package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/flidai/leapview/internal/platform/http/idempotency"
	platformdb "github.com/flidai/leapview/internal/platform/http/idempotency/sqlite/idempotencydb"
)

// Record aliases the engine-neutral contract. Keeping this alias preserves
// the SQLite fixture API while preventing consumers from depending on it.
type Record = idempotency.Record

type Store struct {
	q       *platformdb.Queries
	session string
}

var ErrLeaseLost = idempotency.ErrLeaseLost

func NewStore(db platformdb.DBTX) *Store {
	return NewStoreWithSession(db, newSessionID())
}

func NewStoreWithSession(db platformdb.DBTX, session string) *Store {
	return &Store{q: platformdb.New(db), session: session}
}

func newSessionID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("generate idempotency session: %v", err))
	}
	return hex.EncodeToString(raw[:])
}

func (s *Store) Claim(ctx context.Context, scope, digest, owner string, lease, lifetime time.Duration) (Record, bool, error) {
	now := time.Now().UTC()
	if err := s.q.DeleteExpiredAPIIdempotencyRecord(ctx, platformdb.DeleteExpiredAPIIdempotencyRecordParams{Scope: scope, ExpiresAt: now.Format(time.RFC3339Nano)}); err != nil {
		return Record{}, false, err
	}
	rows, err := s.q.CreateAPIIdempotencyRecord(ctx, platformdb.CreateAPIIdempotencyRecordParams{Scope: scope, RequestDigest: digest, OwnerID: owner,
		OwnerSession: s.session, LeaseExpiresAt: now.Add(lease).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(lifetime).Format(time.RFC3339Nano)})
	if err != nil {
		return Record{}, false, err
	}
	execute := rows == 1
	if !execute {
		_, err = s.q.QuarantineAbandonedAPIIdempotencyRecord(ctx, platformdb.QuarantineAbandonedAPIIdempotencyRecordParams{
			UpdatedAt: now.Format(time.RFC3339Nano), Scope: scope, RequestDigest: digest, OwnerSession: s.session,
		})
		if err != nil {
			return Record{}, false, err
		}
	}
	record, err := s.Load(ctx, scope)
	return record, execute, err
}

func (s *Store) Load(ctx context.Context, scope string) (Record, error) {
	row, err := s.q.GetAPIIdempotencyRecord(ctx, scope)
	if err != nil {
		return Record{}, err
	}
	parsedLease, _ := time.Parse(time.RFC3339Nano, row.LeaseExpiresAt)
	record := Record{State: row.State, Digest: row.RequestDigest, Owner: row.OwnerID, OwnerSession: row.OwnerSession, LeaseGeneration: row.LeaseGeneration, LeaseExpires: parsedLease}
	if row.State != "completed" {
		return record, nil
	}
	record.Status = int(row.ResponseStatus.Int64)
	record.Body = append([]byte(nil), row.ResponseBody...)
	record.Header = http.Header{}
	if row.ResponseHeadersJson.Valid && row.ResponseHeadersJson.String != "" {
		if err := json.Unmarshal([]byte(row.ResponseHeadersJson.String), &record.Header); err != nil {
			return Record{}, err
		}
	}
	return record, nil
}

func (s *Store) Renew(ctx context.Context, scope, digest, owner string, generation int64, lease time.Duration) (time.Time, error) {
	now := time.Now().UTC()
	expires := now.Add(lease)
	changed, err := s.q.RenewAPIIdempotencyRecord(ctx, platformdb.RenewAPIIdempotencyRecordParams{NewLeaseExpiresAt: expires.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano), Scope: scope, RequestDigest: digest, OwnerID: owner, LeaseGeneration: generation})
	return expires, requireLease(changed, err)
}

func (s *Store) Complete(ctx context.Context, scope, digest, owner string, generation int64, status int, header http.Header, body []byte) error {
	headersJSON, err := json.Marshal(header)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	changed, err := s.q.CompleteAPIIdempotencyRecord(ctx, platformdb.CompleteAPIIdempotencyRecordParams{ResponseStatus: sql.NullInt64{Int64: int64(status), Valid: true}, ResponseHeadersJson: sql.NullString{String: string(headersJSON), Valid: true}, ResponseBody: body, UpdatedAt: now.Format(time.RFC3339Nano), Scope: scope, RequestDigest: digest, OwnerID: owner, LeaseGeneration: generation})
	return requireLease(changed, err)
}

func (s *Store) Abandon(ctx context.Context, scope, digest, owner string, generation int64) error {
	changed, err := s.q.AbandonAPIIdempotencyRecord(ctx, platformdb.AbandonAPIIdempotencyRecordParams{Scope: scope, RequestDigest: digest, OwnerID: owner, LeaseGeneration: generation})
	return requireLease(changed, err)
}

func (s *Store) MarkIndeterminate(ctx context.Context, scope, digest, owner string, generation int64) error {
	now := time.Now().UTC()
	changed, err := s.q.MarkAPIIdempotencyRecordIndeterminate(ctx, platformdb.MarkAPIIdempotencyRecordIndeterminateParams{
		UpdatedAt: now.Format(time.RFC3339Nano), Scope: scope, RequestDigest: digest, OwnerID: owner, LeaseGeneration: generation,
	})
	return requireLease(changed, err)
}

func requireLease(rows int64, err error) error {
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}
