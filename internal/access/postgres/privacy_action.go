package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// PrivacyExecutor is an access-owned technical executor. The caller, not this
// service, owns case intake, reviewed identifiers and legal decisions. There
// is deliberately no HTTP route or customer-facing request workflow here.
type PrivacyExecutor struct {
	repo     *Repository
	boundary PrivacyBoundary
}

type PrivacyBoundary struct {
	CustomerID   string
	DeploymentID string // durable platform.instance_identity, not a route value
}

type PrivacyIdentifier struct {
	Kind  string
	Value string
}

// PrivacySubjectGraph is supplied only after external case/identity review.
// In this first slice principal_id is the only supported search input. Email
// equality, names and provider subjects never confer authority or widen scope.
type PrivacySubjectGraph struct {
	Boundary            PrivacyBoundary
	PrincipalID         string
	PrincipalType       string // user or service
	ApprovedIdentifiers []PrivacyIdentifier
	CaseID              string
	CorrelationID       string
	ActorID             string
	Action              string // currently only restrict_access
	DryRun              bool
}

type PrivacyRecord struct {
	Store         string
	RecordID      string
	Boundary      PrivacyBoundary
	MatchReason   string
	Actionability string // restrict or excluded
}

type PrivacyManifest struct {
	Digest     string
	Records    []PrivacyRecord
	NextCursor string
	Total      int64
}

var (
	ErrPrivacyUnauthorized = errors.New("privacy action is not authorized")
	ErrPrivacyBoundary     = errors.New("privacy action boundary mismatch")
	ErrPrivacyUnsupported  = errors.New("privacy action or identifier is unsupported")
	ErrPrivacyConflict     = errors.New("privacy action conflicts with reviewed scope")
	ErrPrivacyBusy         = errors.New("privacy action has an active mutation")
	ErrPrivacyTooLarge     = errors.New("privacy action manifest exceeds the supported bound")
)

const (
	PrivacyRestrictAccess = "restrict_access"
	privacyPageSize       = 64
	privacyMaxPageSize    = 100
	privacyMaxItems       = 10000
)

func NewPrivacyExecutor(repo *Repository, boundary PrivacyBoundary) (*PrivacyExecutor, error) {
	if repo == nil || !repo.Configured() {
		return nil, errors.New("configured access repository is required")
	}
	if _, err := bounded(boundary.CustomerID, "customer id", 255); err != nil {
		return nil, err
	}
	if _, err := bounded(boundary.DeploymentID, "deployment id", 255); err != nil {
		return nil, err
	}
	return &PrivacyExecutor{repo: repo, boundary: boundary}, nil
}

func (e *PrivacyExecutor) validateGraph(g PrivacySubjectGraph) (PrivacySubjectGraph, error) {
	if e == nil || e.repo == nil || g.Boundary != e.boundary {
		return g, ErrPrivacyBoundary
	}
	var err error
	if g.PrincipalID, err = uuidID("principal id", g.PrincipalID); err != nil {
		return g, err
	}
	if g.ActorID, err = uuidID("actor id", g.ActorID); err != nil {
		return g, err
	}
	if g.ActorID == g.PrincipalID {
		return g, ErrPrivacyUnauthorized
	}
	if g.CaseID, err = bounded(g.CaseID, "case id", 256); err != nil {
		return g, err
	}
	if g.CorrelationID, err = bounded(g.CorrelationID, "correlation id", 256); err != nil {
		return g, err
	}
	if g.PrincipalType != "user" && g.PrincipalType != "service" {
		return g, ErrPrivacyUnsupported
	}
	if g.Action != PrivacyRestrictAccess {
		return g, ErrPrivacyUnsupported
	}
	if len(g.ApprovedIdentifiers) != 1 || g.ApprovedIdentifiers[0].Kind != "principal_id" || g.ApprovedIdentifiers[0].Value != g.PrincipalID {
		return g, ErrPrivacyUnsupported
	}
	return g, nil
}

// authorize checks the database's durable deployment identity and the actor's
// current instance-wide role on the same database used for discovery/action.
func (e *PrivacyExecutor) authorize(ctx context.Context, db DBTX, g PrivacySubjectGraph) (pgtype.UUID, error) {
	queries := accessdb.New(db)
	instanceID, err := queries.GetPrivacyInstanceIdentity(ctx)
	if err != nil || instanceID != g.Boundary.DeploymentID {
		return pgtype.UUID{}, ErrPrivacyBoundary
	}
	actor := &Repository{db: db}
	allowed, err := actor.IsPlatformAdmin(ctx, g.ActorID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("check privacy actor authorization: %w", err)
	}
	if !allowed {
		return pgtype.UUID{}, ErrPrivacyUnauthorized
	}
	principalID, err := pgUUID(g.PrincipalID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	principal, err := actor.PrincipalByID(ctx, g.PrincipalID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("read reviewed principal: %w", err)
	}
	principalType := string(principal.Kind)
	if principalType == "service_principal" {
		principalType = "service"
	}
	if principalType != g.PrincipalType {
		return pgtype.UUID{}, ErrPrivacyConflict
	}
	return principalID, nil
}

func privacyGraphDigest(g PrivacySubjectGraph) string {
	// The dry-run flag and pagination are intentionally absent: an approved
	// plan must bind identically to its later executing call.
	b, _ := json.Marshal(struct {
		Boundary            PrivacyBoundary
		PrincipalID         string
		PrincipalType       string
		ApprovedIdentifiers []PrivacyIdentifier
		CaseID              string
		CorrelationID       string
		ActorID             string
		Action              string
	}{g.Boundary, g.PrincipalID, g.PrincipalType, g.ApprovedIdentifiers, g.CaseID, g.CorrelationID, g.ActorID, g.Action})
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type privacyCursor struct {
	Store    string
	RecordID string
}

func decodePrivacyCursor(encoded string) (privacyCursor, error) {
	if encoded == "" {
		return privacyCursor{}, nil
	}
	if len(encoded) > 1024 {
		return privacyCursor{}, ErrPrivacyConflict
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return privacyCursor{}, ErrPrivacyConflict
	}
	var cursor privacyCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Store == "" || cursor.RecordID == "" {
		return privacyCursor{}, ErrPrivacyConflict
	}
	return cursor, nil
}

func encodePrivacyCursor(record PrivacyRecord) string {
	raw, _ := json.Marshal(privacyCursor{Store: record.Store, RecordID: record.RecordID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func privacyActionability(sqlActionable bool) string {
	if sqlActionable {
		return "restrict"
	}
	return "excluded"
}

// scanPrivacyManifest uses indexed principal relationships and keyset pages.
// It never materializes an unbounded subject graph in Go memory. The hard cap
// fails closed rather than launching an unbounded action in one review cycle.
func scanPrivacyManifest(ctx context.Context, db DBTX, g PrivacySubjectGraph, principalID pgtype.UUID, visit func(int64, PrivacyRecord) error) (string, int64, error) {
	queries := accessdb.New(db)
	h := sha256.New()
	_, _ = h.Write([]byte(privacyGraphDigest(g)))
	var after privacyCursor
	var count int64
	for {
		rows, err := queries.ListPrivacySubjectRecords(ctx, accessdb.ListPrivacySubjectRecordsParams{
			AfterStore: after.Store, AfterRecordID: after.RecordID, PageSize: privacyPageSize, PrincipalID: principalID,
		})
		if err != nil {
			return "", count, fmt.Errorf("discover privacy records: %w", err)
		}
		for _, row := range rows {
			count++
			if count > privacyMaxItems {
				return "", count, ErrPrivacyTooLarge
			}
			record := PrivacyRecord{Store: row.Store, RecordID: row.RecordID, Boundary: g.Boundary,
				MatchReason: "approved_principal_id", Actionability: privacyActionability(row.Actionable)}
			encoded, _ := json.Marshal(record)
			_, _ = h.Write(encoded)
			if visit != nil {
				if err := visit(count, record); err != nil {
					return "", count, err
				}
			}
			after = privacyCursor{Store: row.Store, RecordID: row.RecordID}
		}
		if len(rows) < privacyPageSize {
			break
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), count, nil
}

// DryRun returns a deterministic, bounded page and a digest of the complete
// reviewed access-owned manifest. It performs no writes. The cursor contains
// only record IDs; OAuth signatures are hashed inside the discovery query.
func (e *PrivacyExecutor) DryRun(ctx context.Context, graph PrivacySubjectGraph, cursor string, limit int) (PrivacyManifest, error) {
	if !graph.DryRun {
		return PrivacyManifest{}, errors.New("dry-run flag is required")
	}
	g, err := e.validateGraph(graph)
	if err != nil {
		return PrivacyManifest{}, err
	}
	if limit <= 0 || limit > privacyMaxPageSize {
		return PrivacyManifest{}, errors.New("privacy page size is out of bounds")
	}
	after, err := decodePrivacyCursor(cursor)
	if err != nil {
		return PrivacyManifest{}, err
	}
	tx, err := e.beginPrivacyTx(ctx, true)
	if err != nil {
		return PrivacyManifest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	principalID, err := e.authorize(ctx, tx, g)
	if err != nil {
		return PrivacyManifest{}, err
	}
	result := PrivacyManifest{Records: make([]PrivacyRecord, 0, limit)}
	var more bool
	digest, total, err := scanPrivacyManifest(ctx, tx, g, principalID, func(_ int64, record PrivacyRecord) error {
		if record.Store < after.Store || (record.Store == after.Store && record.RecordID <= after.RecordID) {
			return nil
		}
		if len(result.Records) < limit {
			result.Records = append(result.Records, record)
		} else {
			more = true
		}
		return nil
	})
	if err != nil {
		return PrivacyManifest{}, err
	}
	result.Digest, result.Total = digest, total
	if more {
		result.NextCursor = encodePrivacyCursor(result.Records[len(result.Records)-1])
	}
	return result, nil
}

func privacyShortError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "40001" || pgErr.Code == "40P01") {
		return ErrPrivacyBusy
	}
	return err
}
