// Package postgres stores immutable serving-generation evidence alongside the
// canonical delivery authority. Delivery owns lifecycle and active selection;
// this package owns only the admitted bundle, graph projection and reader
// leases rooted at exact DuckLake snapshot seals.
package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingdb "github.com/flidai/leapview/internal/servingstate/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is a strict caller-owned PostgreSQL transaction surface. Pools satisfy
// DBTX for reads but intentionally do not satisfy Tx, preventing multi-step
// admission/lease mutations from accidentally autocommitting per statement.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}
type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Retention batches are deliberately small enough to keep row locks and
// transaction latency bounded. Callers can invoke the maintenance methods
// repeatedly to drain a larger backlog.
const retentionBatchLimit = 1000

// Bundle is the immutable serving evidence admitted for one delivery
// generation. JSON strings are canonical object documents returned by the DB.
type Bundle struct {
	GenerationID                                                                                       string
	ProjectID                                                                                          projectgraph.ResourceID
	Environment                                                                                        servingstate.Environment
	ArtifactID, ArtifactDigest, CompiledGraphDigest, ArtifactFormat, ArtifactLocator                   string
	StorageSecurityDomain, ArtifactContentType, ArtifactMetadataDigest                                 string
	ManifestJSON, ProjectDigest, AccessPolicyJSON, DashboardPublicationsJSON, DashboardAppearancesJSON string
	SizeBytes                                                                                          int64
	DuckLakeSnapshotID                                                                                 int64
	CreatedBy, CreatedAt, ActivatedAt                                                                  string
}

type GenerationBundleInput struct {
	GenerationID string
	ProjectID    projectgraph.ResourceID
	Environment  servingstate.Environment
	Artifact     servingstate.Artifact
	// ArtifactLocator is the immutable object-storage key and is required for
	// native admission; legacy filesystem path fields are not consulted.
	ArtifactLocator           string
	StorageSecurityDomain     string
	ArtifactContentType       string
	ArtifactMetadataDigest    string
	ProjectDigest             string
	AccessPolicyJSON          string
	DashboardPublicationsJSON string
	DashboardAppearancesJSON  string
	CreatedBy                 string
}

var ErrConflict = errors.New("serving generation bundle conflict")

// AdmitGenerationBundleTx validates and inserts a generation bundle in the
// caller-owned delivery transaction. It never commits or rolls back tx.
func AdmitGenerationBundleTx(ctx context.Context, tx Tx, input GenerationBundleInput, graph projectgraph.ProjectGraph) (Bundle, error) {
	if tx == nil {
		return Bundle{}, errors.New("serving-state transaction is required")
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return Bundle{}, errors.New("caller-owned serving-state admission must be a PostgreSQL transaction")
	}
	ctx = contextOrBackground(ctx)
	gen, err := uuid.Parse(input.GenerationID)
	if err != nil {
		return Bundle{}, fmt.Errorf("generation id must be UUID: %w", err)
	}
	if err := input.ProjectID.Validate(); err != nil || input.ProjectID.String() != strings.TrimSpace(input.ProjectID.String()) {
		return Bundle{}, errors.New("project id must be canonical")
	}
	if err := servingstate.ValidateEnvironment(input.Environment); err != nil || string(input.Environment) != strings.TrimSpace(string(input.Environment)) {
		return Bundle{}, errors.New("environment must be canonical")
	}
	locator := strings.TrimSpace(input.ArtifactLocator)
	if err := validateArtifactIdentity(input.Artifact, input.GenerationID, locator); err != nil {
		return Bundle{}, err
	}
	if !isCanonicalDigest(input.ProjectDigest) {
		return Bundle{}, errors.New("project digest must be a canonical SHA-256 identity")
	}
	if !isCanonicalDigest(input.ArtifactMetadataDigest) {
		return Bundle{}, errors.New("artifact metadata digest must be a canonical SHA-256 identity")
	}
	if err := validateStorageSecurityDomain(input.StorageSecurityDomain); err != nil {
		return Bundle{}, err
	}
	if input.ArtifactContentType != servingstate.ArtifactBundleContentType {
		return Bundle{}, fmt.Errorf("artifact content type must be %q", servingstate.ArtifactBundleContentType)
	}
	if err := validateCreatedBy(input.CreatedBy); err != nil {
		return Bundle{}, err
	}
	manifest, err := canonicalObject(input.Artifact.ManifestJSON)
	if err != nil {
		return Bundle{}, err
	}
	access, err := canonicalObject(input.AccessPolicyJSON)
	if err != nil {
		return Bundle{}, err
	}
	pub, err := canonicalObject(input.DashboardPublicationsJSON)
	if err != nil {
		return Bundle{}, err
	}
	appearance, err := canonicalObject(input.DashboardAppearancesJSON)
	if err != nil {
		return Bundle{}, err
	}
	if err := graph.Validate(); err != nil || graph.ProjectID() != input.ProjectID {
		return Bundle{}, errors.New("serving graph is invalid or project-mismatched")
	}
	identity := projectgraph.ServingIdentity{ProjectID: input.ProjectID, Environment: string(input.Environment), GenerationID: gen.String()}
	if _, err := projectgraph.NewArtifactEnvelope(identity, graph); err != nil {
		return Bundle{}, err
	}
	genUUID, _ := pgUUID(input.GenerationID)
	evidence, err := querySet(tx).GenerationEvidence(ctx, genUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Bundle{}, servingstate.ErrNotFound
	} else if err != nil {
		return Bundle{}, err
	}
	if evidence.ProjectID != input.ProjectID.String() || evidence.Environment != string(input.Environment) || evidence.ServingArtifactDigest != input.Artifact.Digest || evidence.CompiledGraphDigest != graph.Digest() {
		return Bundle{}, fmt.Errorf("%w: serving bundle does not match delivery generation", ErrConflict)
	}
	tagRows, err := querySet(tx).InsertBundle(ctx, servingdb.InsertBundleParams{GenerationID: genUUID, ProjectID: input.ProjectID.String(), Environment: string(input.Environment), ArtifactID: input.Artifact.ID, ArtifactDigest: input.Artifact.Digest, CompiledGraphDigest: graph.Digest(), ArtifactFormat: input.Artifact.Format, ArtifactLocator: locator, StorageSecurityDomain: input.StorageSecurityDomain, ArtifactContentType: input.ArtifactContentType, ArtifactMetadataDigest: input.ArtifactMetadataDigest, ManifestJson: []byte(manifest), ProjectDigest: input.ProjectDigest, AccessPolicyJson: []byte(access), DashboardPublicationsJson: []byte(pub), DashboardAppearancesJson: []byte(appearance), SizeBytes: input.Artifact.SizeBytes, CreatedBy: input.CreatedBy})
	if err != nil {
		return Bundle{}, err
	}
	if tagRows == 0 {
		stored, readErr := readBundle(ctx, tx, input.GenerationID)
		if readErr != nil {
			return Bundle{}, readErr
		}
		if stored.ArtifactID != input.Artifact.ID || stored.ArtifactDigest != input.Artifact.Digest || stored.CompiledGraphDigest != graph.Digest() || stored.ArtifactFormat != input.Artifact.Format || stored.ArtifactLocator != locator || stored.StorageSecurityDomain != input.StorageSecurityDomain || stored.ArtifactContentType != input.ArtifactContentType || stored.ArtifactMetadataDigest != input.ArtifactMetadataDigest || stored.SizeBytes != input.Artifact.SizeBytes || !jsonEquivalent(stored.ManifestJSON, manifest) || stored.ProjectDigest != input.ProjectDigest || !jsonEquivalent(stored.AccessPolicyJSON, access) || !jsonEquivalent(stored.DashboardPublicationsJSON, pub) || !jsonEquivalent(stored.DashboardAppearancesJSON, appearance) || stored.CreatedBy != input.CreatedBy {
			return Bundle{}, fmt.Errorf("generation bundle replay differs: %w", ErrConflict)
		}
		if err := verifyGraphProjection(ctx, tx, input.GenerationID, graph); err != nil {
			return Bundle{}, err
		}
		return stored, nil
	}
	for _, resource := range graph.Resources() {
		payload, _ := json.Marshal(resource)
		_, err = querySet(tx).InsertAsset(ctx, servingdb.InsertAssetParams{Column1: genUUID, SnapshotID: "asset_" + shortDigest(input.GenerationID+"|"+resource.ID.String()), LogicalAssetID: resource.ID.String(), AssetType: string(resource.Kind), AssetKey: resource.Name, Title: resource.Metadata.DisplayName, Description: resource.Metadata.Description, SourceFile: resource.Provenance.Path, PayloadSchema: "project.graph.v1", Column10: payload, ContentHash: digestBytes(payload)})
		if err != nil {
			return Bundle{}, err
		}
	}
	for _, edge := range graph.Edges() {
		_, err = querySet(tx).InsertAssetEdge(ctx, servingdb.InsertAssetEdgeParams{Column1: genUUID, ID: "edge_" + shortDigest(input.GenerationID+"|"+edge.From.String()+"|"+edge.To.String()+"|"+edge.Relation), FromLogicalAssetID: edge.From.String(), ToLogicalAssetID: edge.To.String(), EdgeType: edge.Relation})
		if err != nil {
			return Bundle{}, err
		}
	}
	if err := verifyGraphProjection(ctx, tx, input.GenerationID, graph); err != nil {
		return Bundle{}, err
	}
	return readBundle(ctx, tx, input.GenerationID)
}

// verifyGraphProjection makes replay idempotency exact: ON CONFLICT DO
// NOTHING is only an insertion optimization; every persisted child row and
// payload must still equal the canonical graph, with no missing or extra rows.
func verifyGraphProjection(ctx context.Context, db DBTX, generation string, graph projectgraph.ProjectGraph) error {
	type expectedAsset struct {
		snapshot, id, typ, key, title, description, source, schema, payload, hash string
	}
	expected := make(map[string]expectedAsset, len(graph.Resources()))
	for _, resource := range graph.Resources() {
		payload, _ := json.Marshal(resource)
		id := resource.ID.String()
		expected[id] = expectedAsset{
			snapshot:    "asset_" + shortDigest(generation+"|"+id),
			id:          id,
			typ:         string(resource.Kind),
			key:         resource.Name,
			title:       resource.Metadata.DisplayName,
			description: resource.Metadata.Description,
			source:      resource.Provenance.Path,
			schema:      "project.graph.v1",
			payload:     string(payload),
			hash:        digestBytes(payload),
		}
	}
	id, err := pgUUID(generation)
	if err != nil {
		return err
	}
	rows, err := querySet(db).ListAssets(contextOrBackground(ctx), id)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(expected))
	for _, row := range rows {
		var got expectedAsset
		got.snapshot, got.id, got.typ, got.key, got.title, got.description, got.source, got.schema, got.payload, got.hash = row.SnapshotID, row.LogicalAssetID, row.AssetType, row.AssetKey, row.Title, row.Description, row.SourceFile, row.PayloadSchema, row.PayloadJson, row.ContentHash
		want, ok := expected[got.id]
		if !ok || want.snapshot != got.snapshot || want.typ != got.typ || want.key != got.key || want.title != got.title || want.description != got.description || want.source != got.source || want.schema != got.schema || !jsonEquivalent(want.payload, got.payload) || want.hash != got.hash {
			return fmt.Errorf("%w: persisted asset %q differs", ErrConflict, got.id)
		}
		seen[got.id] = struct{}{}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("%w: persisted asset set cardinality differs", ErrConflict)
	}
	type edgeKey struct{ id, from, to, typ string }
	expectedEdges := make(map[string]edgeKey, len(graph.Edges()))
	for _, edge := range graph.Edges() {
		id := "edge_" + shortDigest(generation+"|"+edge.From.String()+"|"+edge.To.String()+"|"+edge.Relation)
		expectedEdges[id] = edgeKey{id: id, from: edge.From.String(), to: edge.To.String(), typ: edge.Relation}
	}
	erows, err := querySet(db).ListAssetEdges(contextOrBackground(ctx), id)
	if err != nil {
		return err
	}
	edgesSeen := make(map[string]struct{}, len(expectedEdges))
	for _, row := range erows {
		var got edgeKey
		got.id, got.from, got.to, got.typ = row.ID, row.FromLogicalAssetID, row.ToLogicalAssetID, row.EdgeType
		want, ok := expectedEdges[got.id]
		if !ok || want.from != got.from || want.to != got.to || want.typ != got.typ {
			return fmt.Errorf("%w: persisted edge %q differs", ErrConflict, got.id)
		}
		edgesSeen[got.id] = struct{}{}
	}
	if len(edgesSeen) != len(expectedEdges) {
		return fmt.Errorf("%w: persisted edge set cardinality differs", ErrConflict)
	}
	return nil
}

type Repository struct{ db DBTX }

func New(db DBTX) *Repository                  { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository        { return New(db) }
func (r *Repository) WithTx(tx Tx) *Repository { return New(tx) }
func (*Repository) NativePersistence()         {}
func (r *Repository) Configured() bool         { return r != nil && r.db != nil }
func (r *Repository) AdmitGenerationBundleTx(ctx context.Context, tx Tx, input GenerationBundleInput, graph projectgraph.ProjectGraph) (Bundle, error) {
	return AdmitGenerationBundleTx(ctx, tx, input, graph)
}
func SchemaSQL() string { return schemaSQL }
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("serving-state transaction is required")
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return errors.New("retention guard requires a caller-owned PostgreSQL transaction")
	}
	_, err := tx.Exec(contextOrBackground(ctx), schemaSQL)
	return err
}

//go:embed schema.sql
var schemaSQL string

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
func querySet(db DBTX) *servingdb.Queries { return servingdb.New(db) }
func pgUUID(value string) (pgtype.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}
func (r *Repository) dbOrErr() (DBTX, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("serving-state PostgreSQL database is required")
	}
	return r.db, nil
}

func isCanonicalDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	hexPart := value[len("sha256:"):]
	if _, err := hex.DecodeString(hexPart); err != nil {
		return false
	}
	return strings.ToLower(hexPart) == hexPart
}

func validateArtifactIdentity(artifact servingstate.Artifact, generationID, locator string) error {
	if artifact.ServingStateID != servingstate.ID(generationID) {
		return errors.New("artifact serving-state identity does not match generation")
	}
	if artifact.Path != "" {
		return errors.New("artifact filesystem path must be empty")
	}
	if !isCanonicalDigest(artifact.Digest) {
		return errors.New("artifact digest must be a canonical SHA-256 identity")
	}
	wantID := "artifact-" + strings.TrimPrefix(artifact.Digest, "sha256:")
	if artifact.ID != wantID || artifact.ID != strings.TrimSpace(artifact.ID) {
		return errors.New("artifact id must be artifact-<digesthex>")
	}
	if artifact.Format != servingstate.ArtifactBundleFormat {
		return fmt.Errorf("artifact format must be %q", servingstate.ArtifactBundleFormat)
	}
	if artifact.SizeBytes < 1 || artifact.SizeBytes > servingstate.MaxArtifactBundleBytes {
		return fmt.Errorf("artifact size must be between 1 and %d bytes", servingstate.MaxArtifactBundleBytes)
	}
	wantLocator := "serving-artifacts/" + strings.TrimPrefix(artifact.Digest, "sha256:") + ".tar.gz"
	if locator != wantLocator || locator != strings.TrimSpace(locator) {
		return errors.New("artifact locator must be serving-artifacts/<digesthex>.tar.gz")
	}
	return nil
}

func validateStorageSecurityDomain(value string) error {
	if value == "" || !utf8.ValidString(value) || value != strings.TrimSpace(value) || len(value) > 512 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("storage security domain must be trimmed, 1..512 bytes, and contain no control characters")
	}
	return nil
}

func validateCreatedBy(value string) error {
	if value == "" || !utf8.ValidString(value) || value != strings.TrimSpace(value) || len(value) > 255 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("created by must be trimmed, non-empty, and at most 255 bytes")
	}
	return nil
}

func canonicalObject(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		raw = "{}"
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", err
	}
	if v == nil {
		v = map[string]json.RawMessage{}
	}
	b, _ := json.Marshal(v)
	if len(b) > 1<<20 {
		return "", errors.New("serving-state JSON exceeds 1 MiB")
	}
	return string(b), nil
}

func jsonEquivalent(left, right string) bool {
	var a, b any
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}
func digestBytes(b []byte) string { h := sha256.Sum256(b); return fmt.Sprintf("sha256:%x", h[:]) }
func shortDigest(v string) string { h := sha256.Sum256([]byte(v)); return fmt.Sprintf("%x", h[:])[:32] }
func readBundle(ctx context.Context, db DBTX, generation string) (Bundle, error) {
	id, err := pgUUID(generation)
	if err != nil {
		return Bundle{}, err
	}
	row, err := querySet(db).GetBundle(contextOrBackground(ctx), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Bundle{}, servingstate.ErrNotFound
	}
	if err != nil {
		return Bundle{}, err
	}
	return bundleFromGetRow(row)
}

func bundleFromGetRow(row servingdb.GetBundleRow) (Bundle, error) {
	pid, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return Bundle{}, err
	}
	bundle := Bundle{GenerationID: row.BGenerationID, ProjectID: pid, Environment: servingstate.Environment(row.Environment), ArtifactID: row.ArtifactID, ArtifactDigest: row.ArtifactDigest, CompiledGraphDigest: row.CompiledGraphDigest, ArtifactFormat: row.ArtifactFormat, ArtifactLocator: row.ArtifactLocator, StorageSecurityDomain: row.StorageSecurityDomain, ArtifactContentType: row.ArtifactContentType, ArtifactMetadataDigest: row.ArtifactMetadataDigest, ManifestJSON: row.BManifestJson, ProjectDigest: row.ProjectDigest, AccessPolicyJSON: row.BAccessPolicyJson, DashboardPublicationsJSON: row.BDashboardPublicationsJson, DashboardAppearancesJSON: row.BDashboardAppearancesJson, SizeBytes: row.SizeBytes, DuckLakeSnapshotID: row.DucklakeSnapshotID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano)}
	if row.CommittedAt.Valid {
		bundle.ActivatedAt = row.CommittedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return bundle, nil
}

func bundleFromActiveRow(row servingdb.GetActiveBundleRow) (Bundle, error) {
	pid, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{GenerationID: row.BGenerationID, ProjectID: pid, Environment: servingstate.Environment(row.Environment), ArtifactID: row.ArtifactID, ArtifactDigest: row.ArtifactDigest, CompiledGraphDigest: row.CompiledGraphDigest, ArtifactFormat: row.ArtifactFormat, ArtifactLocator: row.ArtifactLocator, StorageSecurityDomain: row.StorageSecurityDomain, ArtifactContentType: row.ArtifactContentType, ArtifactMetadataDigest: row.ArtifactMetadataDigest, ManifestJSON: row.BManifestJson, ProjectDigest: row.ProjectDigest, AccessPolicyJSON: row.BAccessPolicyJson, DashboardPublicationsJSON: row.BDashboardPublicationsJson, DashboardAppearancesJSON: row.BDashboardAppearancesJson, SizeBytes: row.SizeBytes, DuckLakeSnapshotID: row.DucklakeSnapshotID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano), ActivatedAt: row.CommittedAt.Time.UTC().Format(time.RFC3339Nano)}, nil
}
func bundleToState(b Bundle, status servingstate.Status) servingstate.State {
	return servingstate.State{ID: servingstate.ID(b.GenerationID), ProjectID: b.ProjectID, Environment: b.Environment, Status: status, Source: servingstate.SourcePublish, Digest: b.ArtifactDigest, ManifestJSON: b.ManifestJSON, ProjectDigest: b.ProjectDigest, AccessPolicyJSON: b.AccessPolicyJSON, DashboardPublicationsJSON: b.DashboardPublicationsJSON, DashboardAppearancesJSON: b.DashboardAppearancesJSON, CreatedBy: b.CreatedBy, CreatedAt: b.CreatedAt, ActivatedAt: b.ActivatedAt, DuckLakeSnapshotID: b.DuckLakeSnapshotID}
}
func bundleArtifact(b Bundle) servingstate.Artifact {
	return servingstate.Artifact{ID: b.ArtifactID, ServingStateID: servingstate.ID(b.GenerationID), Digest: b.ArtifactDigest, Format: b.ArtifactFormat, Locator: b.ArtifactLocator, StorageSecurityDomain: b.StorageSecurityDomain, ContentType: b.ArtifactContentType, MetadataDigest: b.ArtifactMetadataDigest, ManifestJSON: b.ManifestJSON, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt}
}

func (r *Repository) ByID(ctx context.Context, id servingstate.ID) (servingstate.State, error) {
	db, err := r.dbOrErr()
	if err != nil {
		return servingstate.State{}, err
	}
	b, err := readBundle(ctx, db, string(id))
	if err != nil {
		return servingstate.State{}, err
	}
	gen, err := pgUUID(string(id))
	if err != nil {
		return servingstate.State{}, err
	}
	active, err := querySet(db).ActiveFlag(contextOrBackground(ctx), gen)
	if err != nil {
		return servingstate.State{}, err
	}
	status := servingstate.StatusValidated
	if active {
		status = servingstate.StatusActive
	}
	return bundleToState(b, status), nil
}
func (r *Repository) ArtifactByServingState(ctx context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	db, err := r.dbOrErr()
	if err != nil {
		return servingstate.Artifact{}, err
	}
	b, err := readBundle(ctx, db, string(id))
	if err != nil {
		return servingstate.Artifact{}, err
	}
	return bundleArtifact(b), nil
}

// RecordDuckLakeSnapshot verifies the runtime-prepared snapshot against the
// immutable delivery seal. Native serving evidence already carries this value,
// so publication may confirm it but must never update the admitted bundle.
func (r *Repository) RecordDuckLakeSnapshot(ctx context.Context, id servingstate.ID, snapshot int64) error {
	if snapshot <= 0 {
		return fmt.Errorf("ducklake snapshot id must be positive")
	}
	db, err := r.dbOrErr()
	if err != nil {
		return err
	}
	bundle, err := readBundle(ctx, db, string(id))
	if err != nil {
		return err
	}
	if bundle.DuckLakeSnapshotID <= 0 {
		return fmt.Errorf("persisted ducklake snapshot id is missing for serving state %q", id)
	}
	if bundle.DuckLakeSnapshotID != snapshot {
		return fmt.Errorf("ducklake snapshot id mismatch for serving state %q: persisted=%d requested=%d", id, bundle.DuckLakeSnapshotID, snapshot)
	}
	return nil
}

func (r *Repository) ActiveArtifact(ctx context.Context, p projectgraph.ResourceID, e servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	if err := p.Validate(); err != nil {
		return servingstate.State{}, servingstate.Artifact{}, err
	}
	if err := servingstate.ValidateEnvironment(e); err != nil {
		return servingstate.State{}, servingstate.Artifact{}, err
	}
	db, err := r.dbOrErr()
	if err != nil {
		return servingstate.State{}, servingstate.Artifact{}, err
	}
	b, err := readActiveBundle(ctx, db, p, string(e))
	if err != nil {
		return servingstate.State{}, servingstate.Artifact{}, err
	}
	return bundleToState(b, servingstate.StatusActive), bundleArtifact(b), nil
}
func readActiveBundle(ctx context.Context, db DBTX, p projectgraph.ResourceID, e string) (Bundle, error) {
	row, err := querySet(db).GetActiveBundle(contextOrBackground(ctx), servingdb.GetActiveBundleParams{ProjectID: p.String(), Environment: e})
	if errors.Is(err, pgx.ErrNoRows) {
		return Bundle{}, servingstate.ErrNotFound
	}
	if err != nil {
		return Bundle{}, err
	}
	return bundleFromActiveRow(row)
}
func (r *Repository) ListActiveScopes(ctx context.Context) ([]servingstate.ActiveScope, error) {
	db, err := r.dbOrErr()
	if err != nil {
		return nil, err
	}
	rows, err := querySet(db).ListActiveScopes(contextOrBackground(ctx))
	if err != nil {
		return nil, err
	}
	out := []servingstate.ActiveScope{}
	for _, row := range rows {
		pid, err := projectgraph.NewResourceID(row.ProjectID)
		if err != nil {
			return nil, err
		}
		out = append(out, servingstate.ActiveScope{ProjectID: pid, Environment: servingstate.Environment(row.Environment)})
	}
	return out, nil
}

// ActiveScopeForTarget resolves the active serving scope selected for one
// exact delivery target. Unlike ListActiveScopes, this method never scans
// pointers belonging to other targets, so stale or unrelated deployment
// records cannot influence the process-bound instance identity.
//
// The boolean reports a clean absence of an active pointer. A present row is
// validated before returning; malformed project/environment evidence is an
// authority error and must fail closed at the caller.
func (r *Repository) ActiveScopeForTarget(ctx context.Context, targetID string) (servingstate.ActiveScope, bool, error) {
	if targetID == "" || targetID != strings.TrimSpace(targetID) {
		return servingstate.ActiveScope{}, false, errors.New("serving-state target id is required")
	}
	db, err := r.dbOrErr()
	if err != nil {
		return servingstate.ActiveScope{}, false, err
	}
	row, err := querySet(db).GetActiveScopeForTarget(contextOrBackground(ctx), targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return servingstate.ActiveScope{}, false, nil
	}
	if err != nil {
		return servingstate.ActiveScope{}, false, err
	}
	if row.TargetID != targetID {
		return servingstate.ActiveScope{}, false, fmt.Errorf("active serving scope target %q does not match requested target %q", row.TargetID, targetID)
	}
	projectID, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return servingstate.ActiveScope{}, false, fmt.Errorf("active serving scope project %q is invalid: %w", row.ProjectID, err)
	}
	environment := servingstate.Environment(row.Environment)
	if err := servingstate.ValidateEnvironment(environment); err != nil {
		return servingstate.ActiveScope{}, false, fmt.Errorf("active serving scope environment %q is invalid: %w", row.Environment, err)
	}
	return servingstate.ActiveScope{ProjectID: projectID, Environment: environment}, true, nil
}

func (r *Repository) CreateQuerySnapshotLease(ctx context.Context, in servingstate.SnapshotLeaseInput) (string, error) {
	db, err := r.dbOrErr()
	if err != nil {
		return "", err
	}
	b, ok := db.(beginner)
	if !ok {
		return "", errors.New("standalone reader lease requires a PostgreSQL transaction-capable database")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return "", err
	}
	lease, err := createQuerySnapshotLease(ctx, tx, in)
	if err != nil {
		_ = tx.Rollback(contextOrBackground(ctx))
		return "", err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return "", err
	}
	return lease, nil
}

// CreateQuerySnapshotLeaseTx composes lease admission with the caller's
// delivery transaction. The exact live generation retention root is checked
// in that transaction before the lease row is inserted; a seal row alone is
// never treated as a retention authority.
func (r *Repository) CreateQuerySnapshotLeaseTx(ctx context.Context, tx Tx, in servingstate.SnapshotLeaseInput) (string, error) {
	if tx == nil {
		return "", errors.New("serving-state transaction is required")
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return "", errors.New("caller-owned reader lease transaction must be a PostgreSQL transaction")
	}
	return createQuerySnapshotLease(ctx, tx, in)
}

// GuardReaderSnapshotRetentionTx locks and verifies the canonical live
// generation retention root for a lease. Delivery owns this root and may
// retire it concurrently; callers that already hold a delivery transaction
// should invoke this guard in that same transaction.
func GuardReaderSnapshotRetentionTx(ctx context.Context, tx Tx, generation servingstate.ID, snapshot int64) error {
	if tx == nil {
		return errors.New("serving-state transaction is required")
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return errors.New("retention guard requires a caller-owned PostgreSQL transaction")
	}
	if _, err := uuid.Parse(string(generation)); err != nil || snapshot <= 0 {
		return errors.New("invalid reader lease generation or snapshot")
	}
	id, _ := pgUUID(string(generation))
	covered, err := querySet(tx).GuardReaderSnapshotRetention(contextOrBackground(ctx), servingdb.GuardReaderSnapshotRetentionParams{Column1: id, PSnapshot: snapshot})
	if errors.Is(err, pgx.ErrNoRows) || (!covered && err == nil) {
		return errors.New("generation snapshot is not covered by a live delivery retention root")
	}
	return err
}

func createQuerySnapshotLease(ctx context.Context, db Tx, in servingstate.SnapshotLeaseInput) (string, error) {
	_, err := uuid.Parse(string(in.ServingStateID))
	if err != nil {
		return "", err
	}
	if in.DuckLakeSnapshotID <= 0 || in.OwnerID == "" || in.OwnerID != strings.TrimSpace(in.OwnerID) {
		return "", errors.New("invalid reader lease identity")
	}
	if err := GuardReaderSnapshotRetentionTx(ctx, db, in.ServingStateID, in.DuckLakeSnapshotID); err != nil {
		return "", err
	}
	id := uuid.NewString()
	var expiry pgtype.Timestamptz
	if !in.ExpiresAt.IsZero() {
		expiry = pgtype.Timestamptz{Time: in.ExpiresAt.UTC(), Valid: true}
	}
	genUUID, _ := pgUUID(string(in.ServingStateID))
	tag, err := querySet(db).CreateReaderLease(contextOrBackground(ctx), servingdb.CreateReaderLeaseParams{LeaseID: id, Column2: genUUID, DucklakeSnapshotID: in.DuckLakeSnapshotID, OwnerID: in.OwnerID, Column5: expiry})
	if err != nil {
		return "", err
	}
	if tag != 1 {
		return "", errors.New("generation snapshot retention root does not permit lease")
	}
	return id, nil
}
func (r *Repository) ReleaseQuerySnapshotLease(ctx context.Context, id string) error {
	if id == "" || id != strings.TrimSpace(id) {
		return nil
	}
	db, err := r.dbOrErr()
	if err != nil {
		return err
	}
	_, err = querySet(db).ReleaseReaderLease(contextOrBackground(ctx), id)
	return err
}
func (r *Repository) ExtendQuerySnapshotLease(ctx context.Context, id string, expires time.Time) error {
	if id == "" || id != strings.TrimSpace(id) || expires.IsZero() {
		return servingstate.ErrSnapshotLeaseLost
	}
	db, err := r.dbOrErr()
	if err != nil {
		return err
	}
	b, ok := db.(beginner)
	if !ok {
		return errors.New("standalone reader lease extension requires a PostgreSQL transaction-capable database")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return err
	}
	err = extendQuerySnapshotLeaseTx(ctx, tx, id, expires)
	if err != nil {
		_ = tx.Rollback(contextOrBackground(ctx))
		return err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return err
	}
	return nil
}

// ExtendQuerySnapshotLeaseTx rechecks the exact live delivery retention root
// in the caller's transaction before moving the lease expiry forward.
func (r *Repository) ExtendQuerySnapshotLeaseTx(ctx context.Context, tx Tx, id string, expires time.Time) error {
	if tx == nil {
		return servingstate.ErrSnapshotLeaseLost
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return errors.New("caller-owned reader lease transaction must be a PostgreSQL transaction")
	}
	return extendQuerySnapshotLeaseTx(ctx, tx, id, expires)
}

func extendQuerySnapshotLeaseTx(ctx context.Context, tx Tx, id string, expires time.Time) error {
	lease, err := querySet(tx).ReaderLease(contextOrBackground(ctx), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return servingstate.ErrSnapshotLeaseLost
	}
	if err != nil {
		return err
	}
	if err := GuardReaderSnapshotRetentionTx(ctx, tx, servingstate.ID(lease.GenerationID), lease.DucklakeSnapshotID); err != nil {
		return servingstate.ErrSnapshotLeaseLost
	}
	tag, err := querySet(tx).ExtendReaderLease(contextOrBackground(ctx), servingdb.ExtendReaderLeaseParams{LeaseID: id, ExpiresAt: pgtype.Timestamptz{Time: expires.UTC(), Valid: true}})
	if err != nil {
		return err
	}
	if tag != 1 {
		return servingstate.ErrSnapshotLeaseLost
	}
	return nil
}
func (r *Repository) ReleaseExpiredQuerySnapshotLeases(ctx context.Context, e string) error {
	if err := servingstate.ValidateEnvironment(servingstate.Environment(e)); err != nil {
		return err
	}
	db, err := r.dbOrErr()
	if err != nil {
		return err
	}
	b, ok := db.(beginner)
	if !ok {
		return errors.New("expired reader lease reconciliation requires a PostgreSQL transaction-capable database")
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return err
	}
	if err := r.ReleaseExpiredQuerySnapshotLeasesTx(ctx, tx, e); err != nil {
		_ = tx.Rollback(contextOrBackground(ctx))
		return err
	}
	return tx.Commit(contextOrBackground(ctx))
}

// ReleaseExpiredQuerySnapshotLeasesTx runs one bounded expired-lease batch on
// the caller-owned PostgreSQL transaction. It intentionally does not commit or
// roll back, so maintenance orchestration can compose it with other control
// mutations. Runtime roles have no EXECUTE privilege on this maintenance
// function; request paths should use ReleaseQuerySnapshotLease instead.
func (r *Repository) ReleaseExpiredQuerySnapshotLeasesTx(ctx context.Context, tx Tx, e string) error {
	if tx == nil {
		return errors.New("serving-state transaction is required")
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return errors.New("expired reader lease reconciliation requires a caller-owned PostgreSQL transaction")
	}
	if err := servingstate.ValidateEnvironment(servingstate.Environment(e)); err != nil {
		return err
	}
	_, err := querySet(tx).ReleaseExpiredLeases(contextOrBackground(ctx), servingdb.ReleaseExpiredLeasesParams{Environment: e, BatchLimit: retentionBatchLimit})
	return err
}

func (r *Repository) LeasedDuckLakeSnapshots(ctx context.Context, e string) ([]int64, error) {
	if err := servingstate.ValidateEnvironment(servingstate.Environment(e)); err != nil {
		return nil, err
	}
	db, err := r.dbOrErr()
	if err != nil {
		return nil, err
	}
	rows, err := querySet(db).LeasedSnapshots(contextOrBackground(ctx), e)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
func (r *Repository) ReferencedDuckLakeSnapshots(ctx context.Context, e string) ([]int64, error) {
	if err := servingstate.ValidateEnvironment(servingstate.Environment(e)); err != nil {
		return nil, err
	}
	db, err := r.dbOrErr()
	if err != nil {
		return nil, err
	}
	return querySet(db).ReferencedSnapshots(contextOrBackground(ctx), e)
}
func (r *Repository) ActiveDuckLakeSnapshots(ctx context.Context, e string) ([]int64, error) {
	return r.ReferencedDuckLakeSnapshots(ctx, e)
}
func (r *Repository) ForeignEnvironmentDuckLakeSnapshots(ctx context.Context, e string) ([]int64, error) {
	if err := servingstate.ValidateEnvironment(servingstate.Environment(e)); err != nil {
		return nil, err
	}
	db, err := r.dbOrErr()
	if err != nil {
		return nil, err
	}
	return querySet(db).ForeignSnapshots(contextOrBackground(ctx), e)
}
func (r *Repository) ActiveServingStateGraph(ctx context.Context, p projectgraph.ResourceID, e string) (servingstate.AssetGraph, bool, error) {
	s, _, err := r.ActiveArtifact(ctx, p, servingstate.Environment(e))
	if errors.Is(err, servingstate.ErrNotFound) {
		return servingstate.AssetGraph{}, false, nil
	}
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	return r.ServingStateGraph(ctx, p, e, s.ID)
}
func (r *Repository) ServingStateGraph(ctx context.Context, p projectgraph.ResourceID, e string, id servingstate.ID) (servingstate.AssetGraph, bool, error) {
	s, err := r.ByID(ctx, id)
	if errors.Is(err, servingstate.ErrNotFound) {
		return servingstate.AssetGraph{}, false, nil
	}
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	if s.ProjectID != p || s.Environment != servingstate.Environment(e) {
		return servingstate.AssetGraph{}, false, errors.New("serving generation scope mismatch")
	}
	db, err := r.dbOrErr()
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	genUUID, err := pgUUID(string(id))
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	rows, err := querySet(db).ListAssets(contextOrBackground(ctx), genUUID)
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	g := servingstate.AssetGraph{}
	for _, row := range rows {
		rid, err := projectgraph.NewResourceID(row.LogicalAssetID)
		if err != nil {
			return servingstate.AssetGraph{}, false, err
		}
		g.Assets = append(g.Assets, servingstate.Asset{ID: rid, SnapshotID: row.SnapshotID, ProjectID: p, ServingStateID: id, Type: row.AssetType, Key: row.AssetKey, ParentID: projectgraph.ResourceID(row.ParentLogicalAssetID), Title: row.Title, Description: row.Description, SourceFile: row.SourceFile, PayloadSchema: row.PayloadSchema, PayloadJSON: row.PayloadJson, ContentHash: row.ContentHash})
	}
	erows, err := querySet(db).ListAssetEdges(contextOrBackground(ctx), genUUID)
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	for _, row := range erows {
		f, err := projectgraph.NewResourceID(row.FromLogicalAssetID)
		if err != nil {
			return servingstate.AssetGraph{}, false, err
		}
		t, err := projectgraph.NewResourceID(row.ToLogicalAssetID)
		if err != nil {
			return servingstate.AssetGraph{}, false, err
		}
		g.Edges = append(g.Edges, servingstate.AssetEdge{ID: row.ID, ProjectID: p, ServingStateID: id, FromAssetID: f, ToAssetID: t, Type: row.EdgeType})
	}
	return g, true, nil
}
func (r *Repository) AssetVersions(ctx context.Context, p projectgraph.ResourceID, e string, a projectgraph.ResourceID) ([]servingstate.AssetVersion, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if err := servingstate.ValidateEnvironment(servingstate.Environment(e)); err != nil {
		return nil, err
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	db, err := r.dbOrErr()
	if err != nil {
		return nil, err
	}
	rows, err := querySet(db).AssetVersions(contextOrBackground(ctx), servingdb.AssetVersionsParams{ProjectID: p.String(), Environment: e, LogicalAssetID: a.String()})
	if err != nil {
		return nil, err
	}
	out := []servingstate.AssetVersion{}
	for _, row := range rows {
		out = append(out, servingstate.AssetVersion{ServingStateID: servingstate.ID(row.GGenerationID), ProjectID: p, Environment: servingstate.Environment(row.Environment), Status: row.Column4, Digest: row.ServingArtifactDigest, CreatedBy: row.ActorID, CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano), ActivatedAt: row.CommittedAt.Time.UTC().Format(time.RFC3339Nano), SnapshotID: row.SnapshotID, AssetID: a, SourceFile: row.SourceFile, PayloadJSON: row.APayloadJson, ContentHash: row.ContentHash})
	}
	return out, nil
}
