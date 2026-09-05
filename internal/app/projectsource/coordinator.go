// Package projectsource coordinates native project-source admission. It owns
// only source synchronization and immutable object references; publication,
// deployment, and compiler lifecycle authorities remain outside this package.
package projectsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/platform/objectstore"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxSourceFiles             = 10_000
	maxSourceBytes       int64 = 64 << 20
	maxBlobBytes         int64 = 16 << 20
	maxProjectIDBytes          = 255
	maxOwnerIDBytes            = 255
	maxCandidateKeyBytes       = 512
	maxObjectKeyBytes          = 2048
	defaultBlobBatch           = 32
)

var (
	ErrInvalid  = errors.New("invalid project source admission input")
	ErrConflict = errors.New("project source admission conflict")
)

// Tx is a caller-owned native PostgreSQL transaction. The coordinator never
// commits or rolls back a transaction passed to repository methods directly;
// it owns only the short transaction it opens around each phase.
type Tx = projectpostgres.SourceTx

// SourceRepository is the narrow project-owned persistence surface used by
// admission. Implementations must keep all mutation methods caller-transaction
// shaped, as the native project PostgreSQL repository does.
type SourceRepository interface {
	CreateSyncPlanTx(context.Context, projectpostgres.SourceTx, projectpostgres.SyncPlanInput) (projectpostgres.SyncPlan, error)
	SyncPlanForUpdateTx(context.Context, projectpostgres.SourceTx, uuid.UUID) (projectpostgres.SyncPlan, error)
	ListMissingPlanSourceBlobDigestsTx(context.Context, projectpostgres.SourceTx, uuid.UUID, string) ([]string, error)
	InsertSourceBlobTx(context.Context, projectpostgres.SourceTx, projectpostgres.SourceBlobInput) (projectpostgres.SourceBlob, error)
	CommitSnapshotTx(context.Context, projectpostgres.SourceTx, projectpostgres.CommitSnapshotInput) (projectpostgres.SourceSnapshot, error)
	Snapshot(context.Context, string, string, string) (projectpostgres.SourceSnapshot, error)
}

// BeginFunc adapts a native pgx pool/connection or a test transaction source.
type BeginFunc func(context.Context) (Tx, error)

// CompilerPort is an application-owned callback. It is called only after source
// object admission transactions have closed.
type CompilerPort interface {
	Compile(context.Context, CompileInput) (CompileOutput, error)
}

type CompileFunc func(context.Context, CompileInput) (CompileOutput, error)

func (f CompileFunc) Compile(ctx context.Context, input CompileInput) (CompileOutput, error) {
	return f(ctx, input)
}

type SourceFile struct {
	Path                  string
	Digest                string
	Bytes                 []byte
	ObjectKey             string
	ContentType           string
	MetadataDigest        string
	StorageSecurityDomain string
}

type SourceRevision struct {
	Revision   string
	Repository string
	Ref        string
	ChangeID   string
}

// AdmissionInput is the caller-owned source snapshot and plan identity. UUIDs
// are intentionally supplied by the caller; the coordinator does not derive
// durable identities from source content.
type AdmissionInput struct {
	PlanID                uuid.UUID
	OperationID           uuid.UUID
	SnapshotID            uuid.UUID
	ProjectID             string
	StorageSecurityDomain string
	OwnerID               string
	CandidateKey          string
	ProjectFile           string
	SourceDigest          string
	RequestDigest         string
	ExpiresAt             time.Time
	Files                 []SourceFile
	Attestation           projectpostgres.SourceAttestationInput
}

type CompileInput struct {
	ProjectID             string
	StorageSecurityDomain string
	ProjectFile           string
	SourceDigest          string
	Files                 []SourceFile
}

type CompileOutput struct {
	ProjectDigest                 string
	CompilerVersion               string
	SchemaVersion                 int64
	ProjectArtifact               []byte
	ProjectArtifactObjectKey      string
	ProjectArtifactMetadataDigest string
	Manifest                      []byte
	ManifestObjectKey             string
	ManifestMetadataDigest        string
}

type AdmissionResult struct {
	Plan     projectpostgres.SyncPlan
	Snapshot projectpostgres.SourceSnapshot
}

type Coordinator struct {
	begin     BeginFunc
	sources   SourceRepository
	objects   objectstore.ImmutableStore
	compiler  CompilerPort
	blobBatch int
	now       func() time.Time
}

// New constructs a source admission coordinator over native project
// persistence, immutable object storage, and a compiler callback.
func New(begin BeginFunc, sources SourceRepository, objects objectstore.ImmutableStore, compiler CompilerPort) (*Coordinator, error) {
	if begin == nil || sources == nil || objects == nil || compiler == nil {
		return nil, ErrInvalid
	}
	c := &Coordinator{begin: begin, sources: sources, objects: objects, compiler: compiler, blobBatch: defaultBlobBatch, now: func() time.Time { return time.Now().UTC() }}
	return c, nil
}

// NewWithDatabase adapts a *pgxpool.Pool, *pgx.Conn, or test object exposing
// Begin(context.Context) (pgx.Tx, error) to New.
func NewWithDatabase(db interface {
	Begin(context.Context) (pgx.Tx, error)
}, sources SourceRepository, objects objectstore.ImmutableStore, compiler CompilerPort) (*Coordinator, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	return New(func(ctx context.Context) (Tx, error) { return db.Begin(ctx) }, sources, objects, compiler)
}

// SetBlobBatchSize changes only the number of blob references admitted per
// short transaction. It is intended for bounded integration tests and
// deployment configuration, not for transaction lifetime extension.
func (c *Coordinator) SetBlobBatchSize(size int) error {
	if c == nil || size < 1 || size > 1000 {
		return ErrInvalid
	}
	c.blobBatch = size
	return nil
}

// Admit executes the complete source admission protocol. Object-store puts,
// compiler work, and artifact writes never occur while a PostgreSQL
// transaction is active.
func (c *Coordinator) Admit(ctx context.Context, input AdmissionInput) (AdmissionResult, error) {
	if c == nil || c.begin == nil || c.sources == nil || c.objects == nil || c.compiler == nil {
		return AdmissionResult{}, ErrInvalid
	}
	normalized, files, err := normalizeInput(input, c.now())
	if err != nil {
		return AdmissionResult{}, err
	}
	planInput := projectpostgres.SyncPlanInput{PlanID: normalized.PlanID, OperationID: normalized.OperationID, ProjectID: normalized.ProjectID, StorageSecurityDomain: normalized.StorageSecurityDomain, OwnerID: normalized.OwnerID, CandidateKey: normalized.CandidateKey, SourceDigest: normalized.SourceDigest, ProjectFile: normalized.ProjectFile, RequestDigest: normalized.RequestDigest, ExpiresAt: normalized.ExpiresAt, Entries: make([]projectpostgres.SourceSyncPlanEntryInput, len(files))}
	for i, file := range files {
		planInput.Entries[i] = projectpostgres.SourceSyncPlanEntryInput{Path: file.Path, Digest: file.Digest, SizeBytes: int64(len(file.Bytes)), Ordinal: i}
	}

	plan, err := c.createAndLockPlan(ctx, planInput)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("create source plan: %w", err)
	}
	if plan.State == "committed" {
		snapshot, snapErr := c.sources.Snapshot(ctx, normalized.ProjectID, normalized.StorageSecurityDomain, normalized.SourceDigest)
		if snapErr != nil {
			return AdmissionResult{}, snapErr
		}
		if snapshot.SnapshotID != normalized.SnapshotID || snapshot.ProjectID != normalized.ProjectID || snapshot.StorageSecurityDomain != normalized.StorageSecurityDomain || snapshot.SourceDigest != normalized.SourceDigest || snapshot.ProjectFile != normalized.ProjectFile {
			return AdmissionResult{}, fmt.Errorf("%w: replayed snapshot identity differs", ErrConflict)
		}
		return AdmissionResult{Plan: plan, Snapshot: snapshot}, nil
	}

	missing, err := c.missingDigests(ctx, plan)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("list missing source blobs: %w", err)
	}
	for _, digest := range missing {
		file, ok := filesByDigest(files)[digest]
		if !ok {
			return AdmissionResult{}, fmt.Errorf("%w: repository solicited unknown source digest %s", ErrConflict, digest)
		}
		if err := c.putSourceObject(ctx, file); err != nil {
			return AdmissionResult{}, fmt.Errorf("put source blob %s: %w", digest, err)
		}
	}
	if err := c.admitBlobReferences(ctx, plan, files); err != nil {
		return AdmissionResult{}, fmt.Errorf("admit source blobs: %w", err)
	}

	compiled, err := c.compiler.Compile(ctx, CompileInput{ProjectID: normalized.ProjectID, StorageSecurityDomain: normalized.StorageSecurityDomain, ProjectFile: normalized.ProjectFile, SourceDigest: normalized.SourceDigest, Files: cloneFiles(files)})
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("compile project: %w", err)
	}
	compiled, err = normalizeCompileOutput(compiled)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("validate compiled project: %w", err)
	}
	projectArtifactInfo, err := c.putArtifact(ctx, normalized.StorageSecurityDomain, compiled.ProjectArtifactObjectKey, compiled.ProjectArtifact, compiled.ProjectArtifactMetadataDigest)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("put project artifact: %w", err)
	}
	manifestInfo, err := c.putArtifact(ctx, normalized.StorageSecurityDomain, compiled.ManifestObjectKey, compiled.Manifest, compiled.ManifestMetadataDigest)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("put source manifest: %w", err)
	}

	commitInput := projectpostgres.CommitSnapshotInput{PlanID: plan.PlanID, OwnerID: plan.OwnerID, SnapshotID: normalized.SnapshotID, ProjectID: normalized.ProjectID, StorageSecurityDomain: normalized.StorageSecurityDomain, SourceDigest: normalized.SourceDigest, ProjectFile: normalized.ProjectFile, ProjectDigest: compiled.ProjectDigest, ProjectArtifactObjectKey: projectArtifactInfo.Key, ProjectArtifactDigest: projectArtifactInfo.Digest, ProjectArtifactSizeBytes: projectArtifactInfo.SizeBytes, ManifestObjectKey: manifestInfo.Key, ManifestObjectDigest: manifestInfo.Digest, ManifestObjectSizeBytes: manifestInfo.SizeBytes, CompilerVersion: compiled.CompilerVersion, SchemaVersion: compiled.SchemaVersion, Entries: planEntries(plan), Attestation: normalized.Attestation}
	snapshot, err := c.commitSnapshot(ctx, commitInput)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("commit source snapshot: %w", err)
	}
	plan.State = "committed"
	return AdmissionResult{Plan: plan, Snapshot: snapshot}, nil
}

func (c *Coordinator) createAndLockPlan(ctx context.Context, input projectpostgres.SyncPlanInput) (projectpostgres.SyncPlan, error) {
	tx, err := c.begin(ctx)
	if err != nil {
		return projectpostgres.SyncPlan{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	plan, err := c.sources.CreateSyncPlanTx(ctx, tx, input)
	if err != nil {
		return projectpostgres.SyncPlan{}, err
	}
	locked, err := c.sources.SyncPlanForUpdateTx(ctx, tx, plan.PlanID)
	if err != nil {
		return projectpostgres.SyncPlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return projectpostgres.SyncPlan{}, err
	}
	return locked, nil
}

func (c *Coordinator) missingDigests(ctx context.Context, plan projectpostgres.SyncPlan) ([]string, error) {
	tx, err := c.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	missing, err := c.sources.ListMissingPlanSourceBlobDigestsTx(ctx, tx, plan.PlanID, plan.OwnerID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return missing, nil
}

func (c *Coordinator) putSourceObject(ctx context.Context, file SourceFile) error {
	metadata := objectstore.ObjectMetadata{StorageSecurityDomain: file.StorageSecurityDomain, Digest: file.Digest, SizeBytes: int64(len(file.Bytes)), ContentType: file.ContentType, MetadataDigest: file.MetadataDigest}
	info, err := c.objects.PutImmutable(ctx, file.ObjectKey, bytes.NewReader(file.Bytes), metadata)
	if err == nil {
		return verifyInfo(info, metadata, file.ObjectKey)
	}
	if !errors.Is(err, objectstore.ErrAmbiguous) {
		return err
	}
	return c.verifyObject(ctx, file.ObjectKey, metadata)
}

func (c *Coordinator) putArtifact(ctx context.Context, domain, key string, body []byte, metadataDigest string) (objectstore.ObjectInfo, error) {
	digest := sha256Identity(body)
	metadata := objectstore.ObjectMetadata{StorageSecurityDomain: domain, Digest: digest, SizeBytes: int64(len(body)), ContentType: "application/octet-stream", MetadataDigest: metadataDigest}
	info, err := c.objects.PutImmutable(ctx, key, bytes.NewReader(body), metadata)
	if err == nil {
		if verifyErr := verifyInfo(info, metadata, key); verifyErr != nil {
			return objectstore.ObjectInfo{}, verifyErr
		}
		return info, nil
	}
	if !errors.Is(err, objectstore.ErrAmbiguous) {
		return objectstore.ObjectInfo{}, err
	}
	if err := c.verifyObject(ctx, key, metadata); err != nil {
		return objectstore.ObjectInfo{}, err
	}
	obj, err := c.objects.Open(ctx, key)
	if err != nil {
		return objectstore.ObjectInfo{}, err
	}
	defer obj.Body.Close()
	return obj.Info, nil
}

func (c *Coordinator) verifyObject(ctx context.Context, key string, expected objectstore.ObjectMetadata) error {
	obj, err := c.objects.Open(ctx, key)
	if err != nil {
		return err
	}
	defer obj.Body.Close()
	if err := verifyInfo(obj.Info, expected, key); err != nil {
		return err
	}
	return nil
}

func verifyInfo(info objectstore.ObjectInfo, expected objectstore.ObjectMetadata, key string) error {
	if info.Key != key {
		return fmt.Errorf("%w: immutable object key mismatch", ErrConflict)
	}
	if info.StorageSecurityDomain != expected.StorageSecurityDomain || info.Digest != expected.Digest || info.SizeBytes != expected.SizeBytes || info.ContentType != expected.ContentType || info.MetadataDigest != expected.MetadataDigest {
		return fmt.Errorf("%w: immutable object identity mismatch", ErrConflict)
	}
	return nil
}

func (c *Coordinator) admitBlobReferences(ctx context.Context, plan projectpostgres.SyncPlan, files []SourceFile) error {
	byDigest := filesByDigest(files)
	entries := append([]projectpostgres.SourceSyncPlanEntry(nil), plan.Entries...)
	for start := 0; start < len(entries); start += c.blobBatch {
		end := start + c.blobBatch
		if end > len(entries) {
			end = len(entries)
		}
		tx, err := c.begin(ctx)
		if err != nil {
			return err
		}
		for _, entry := range entries[start:end] {
			file, ok := byDigest[entry.Digest]
			if !ok {
				_ = tx.Rollback(context.Background())
				return ErrInvalid
			}
			if _, err := c.sources.InsertSourceBlobTx(ctx, tx, projectpostgres.SourceBlobInput{ProjectID: plan.ProjectID, StorageSecurityDomain: plan.StorageSecurityDomain, Digest: entry.Digest, SizeBytes: entry.SizeBytes, ObjectKey: file.ObjectKey, ContentType: file.ContentType, MetadataDigest: file.MetadataDigest, PlanID: plan.PlanID, OwnerID: plan.OwnerID}); err != nil {
				_ = tx.Rollback(context.Background())
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			_ = tx.Rollback(context.Background())
			return err
		}
	}
	return nil
}

func (c *Coordinator) commitSnapshot(ctx context.Context, input projectpostgres.CommitSnapshotInput) (projectpostgres.SourceSnapshot, error) {
	tx, err := c.begin(ctx)
	if err != nil {
		return projectpostgres.SourceSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot, err := c.sources.CommitSnapshotTx(ctx, tx, input)
	if err != nil {
		return projectpostgres.SourceSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return projectpostgres.SourceSnapshot{}, err
	}
	return snapshot, nil
}

func normalizeInput(input AdmissionInput, now time.Time) (AdmissionInput, []SourceFile, error) {
	n := input
	if n.PlanID == uuid.Nil {
		return AdmissionInput{}, nil, ErrInvalid
	}
	if n.OperationID == uuid.Nil {
		return AdmissionInput{}, nil, ErrInvalid
	}
	if n.SnapshotID == uuid.Nil {
		return AdmissionInput{}, nil, ErrInvalid
	}
	n.ProjectID = strings.TrimSpace(n.ProjectID)
	n.StorageSecurityDomain = strings.TrimSpace(n.StorageSecurityDomain)
	n.OwnerID = strings.TrimSpace(n.OwnerID)
	n.CandidateKey = strings.TrimSpace(n.CandidateKey)
	n.ProjectFile = strings.TrimSpace(n.ProjectFile)
	if n.ProjectID == "" || len(n.ProjectID) > maxProjectIDBytes || n.StorageSecurityDomain == "" || len(n.StorageSecurityDomain) > maxProjectIDBytes || n.OwnerID == "" || len(n.OwnerID) > maxOwnerIDBytes || n.CandidateKey == "" || len(n.CandidateKey) > maxCandidateKeyBytes || !validText(n.ProjectID) || !validText(n.StorageSecurityDomain) || !validText(n.OwnerID) || !validText(n.CandidateKey) || !canonicalPath(n.ProjectFile) {
		return AdmissionInput{}, nil, ErrInvalid
	}
	if len(n.Files) == 0 || len(n.Files) > maxSourceFiles {
		return AdmissionInput{}, nil, ErrInvalid
	}
	files := append([]SourceFile(nil), n.Files...)
	for i := range files {
		files[i].Path = strings.TrimSpace(files[i].Path)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	seen := map[string]struct{}{}
	var total int64
	for i := range files {
		f := &files[i]
		f.Digest = strings.TrimSpace(f.Digest)
		f.ObjectKey = strings.TrimSpace(f.ObjectKey)
		f.ContentType = strings.TrimSpace(f.ContentType)
		f.MetadataDigest = strings.TrimSpace(f.MetadataDigest)
		if !canonicalPath(f.Path) || f.Path == "" {
			return AdmissionInput{}, nil, ErrInvalid
		}
		if _, ok := seen[f.Path]; ok {
			return AdmissionInput{}, nil, ErrInvalid
		}
		seen[f.Path] = struct{}{}
		if int64(len(f.Bytes)) > maxBlobBytes {
			return AdmissionInput{}, nil, ErrInvalid
		}
		total += int64(len(f.Bytes))
		if total > maxSourceBytes {
			return AdmissionInput{}, nil, ErrInvalid
		}
		actual := sha256Identity(f.Bytes)
		if f.Digest == "" {
			f.Digest = actual
		}
		if f.Digest != actual {
			return AdmissionInput{}, nil, ErrInvalid
		}
		f.StorageSecurityDomain = n.StorageSecurityDomain
		if f.ObjectKey == "" {
			f.ObjectKey = "source-blobs/" + strings.TrimPrefix(f.Digest, "sha256:")
		}
		if f.ContentType == "" {
			f.ContentType = "application/octet-stream"
		}
		if f.MetadataDigest == "" {
			f.MetadataDigest = sha256Identity(nil)
		}
		if !validObjectKey(f.ObjectKey) || !validDigest(f.MetadataDigest) || f.ContentType == "" || len(f.ContentType) > objectstore.MaxContentTypeBytes || !validText(f.ContentType) {
			return AdmissionInput{}, nil, ErrInvalid
		}
	}
	digestEntries := make([]projectpostgres.SourceSnapshotEntryInput, len(files))
	for i, file := range files {
		digestEntries[i] = projectpostgres.SourceSnapshotEntryInput{Path: file.Path, Digest: file.Digest, SizeBytes: int64(len(file.Bytes)), Ordinal: i}
	}
	n.SourceDigest = projectpostgres.CanonicalSourceDigest(n.ProjectID, n.ProjectFile, digestEntries)
	if strings.TrimSpace(input.SourceDigest) != "" && strings.TrimSpace(input.SourceDigest) != n.SourceDigest {
		return AdmissionInput{}, nil, ErrInvalid
	}
	n.RequestDigest = strings.TrimSpace(n.RequestDigest)
	if n.RequestDigest != "" && !validDigest(n.RequestDigest) {
		return AdmissionInput{}, nil, ErrInvalid
	}
	if n.RequestDigest == "" {
		n.RequestDigest = sha256Identity([]byte(n.SourceDigest + "\x00" + n.CandidateKey))
	}
	n.ExpiresAt = n.ExpiresAt.UTC()
	if n.ExpiresAt.IsZero() {
		n.ExpiresAt = now.Add(2 * time.Minute)
	}
	if !n.ExpiresAt.After(now) || n.ExpiresAt.After(now.Add(5*time.Minute)) {
		return AdmissionInput{}, nil, ErrInvalid
	}
	attestation := n.Attestation
	attestation.AttestationDigest = strings.TrimSpace(attestation.AttestationDigest)
	attestation.Revision = strings.TrimSpace(attestation.Revision)
	attestation.Repository = strings.TrimSpace(attestation.Repository)
	attestation.Ref = strings.TrimSpace(attestation.Ref)
	attestation.ChangeID = strings.TrimSpace(attestation.ChangeID)
	if len(attestation.Revision) > 1024 || len(attestation.Repository) > 1024 || len(attestation.Ref) > 1024 || len(attestation.ChangeID) > 1024 || !validText(attestation.Revision) || !validText(attestation.Repository) || !validText(attestation.Ref) || !validText(attestation.ChangeID) {
		return AdmissionInput{}, nil, ErrInvalid
	}
	attestation.SourceDigest = n.SourceDigest
	if attestation.SnapshotID == uuid.Nil {
		attestation.SnapshotID = n.SnapshotID
	}
	if attestation.SnapshotID != n.SnapshotID || attestation.AttestationID == uuid.Nil {
		return AdmissionInput{}, nil, ErrInvalid
	}
	canonical, err := canonicalJSON(attestation.Payload)
	if err != nil {
		return AdmissionInput{}, nil, err
	}
	attestation.Payload = canonical
	if attestation.AttestationDigest == "" {
		attestation.AttestationDigest = sha256Identity(canonical)
	}
	if !validDigest(attestation.AttestationDigest) || attestation.AttestationDigest != sha256Identity(canonical) {
		return AdmissionInput{}, nil, ErrInvalid
	}
	n.Attestation = attestation
	return n, files, nil
}

func normalizeCompileOutput(out CompileOutput) (CompileOutput, error) {
	out.ProjectDigest = strings.TrimSpace(out.ProjectDigest)
	out.CompilerVersion = strings.TrimSpace(out.CompilerVersion)
	out.ProjectArtifactObjectKey = strings.TrimSpace(out.ProjectArtifactObjectKey)
	out.ManifestObjectKey = strings.TrimSpace(out.ManifestObjectKey)
	out.ProjectArtifactMetadataDigest = strings.TrimSpace(out.ProjectArtifactMetadataDigest)
	out.ManifestMetadataDigest = strings.TrimSpace(out.ManifestMetadataDigest)
	if out.ProjectDigest == "" || !validDigest(out.ProjectDigest) || out.CompilerVersion == "" || len(out.CompilerVersion) > 255 || !validText(out.CompilerVersion) || out.SchemaVersion <= 0 || len(out.ProjectArtifact) == 0 || int64(len(out.ProjectArtifact)) > maxSourceBytes || len(out.Manifest) == 0 || int64(len(out.Manifest)) > maxSourceBytes || !validObjectKey(out.ProjectArtifactObjectKey) || !validObjectKey(out.ManifestObjectKey) {
		return CompileOutput{}, ErrInvalid
	}
	if out.ProjectArtifactMetadataDigest == "" {
		out.ProjectArtifactMetadataDigest = sha256Identity(nil)
	}
	if out.ManifestMetadataDigest == "" {
		out.ManifestMetadataDigest = sha256Identity(nil)
	}
	if !validDigest(out.ProjectArtifactMetadataDigest) || !validDigest(out.ManifestMetadataDigest) {
		return CompileOutput{}, ErrInvalid
	}
	return out, nil
}
func canonicalJSON(raw []byte) ([]byte, error) {
	var validated json.RawMessage
	if err := strictjson.DecodeWithOptions(raw, &validated, strictjson.Options{MaxBytes: 16 << 10, MaxDepth: 100, DuplicateKeys: strictjson.CaseSensitiveKeys, AllowUnknownFields: true}); err != nil {
		return nil, ErrInvalid
	}
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil, ErrInvalid
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, ErrInvalid
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) > 16<<10 {
		return nil, ErrInvalid
	}
	return b, nil
}
func validDigest(v string) bool {
	return len(v) == 71 && strings.HasPrefix(v, "sha256:") && v == strings.ToLower(v) && strings.Trim(v[7:], "0123456789abcdef") == ""
}
func canonicalPath(v string) bool {
	return v != "" && len(v) <= 1024 && validText(v) && !path.IsAbs(v) && path.Clean(v) == v && v != ".." && !strings.HasPrefix(v, "../") && !strings.Contains(v, `\`)
}
func validObjectKey(v string) bool {
	if v == "" || len(v) > maxObjectKeyBytes || !validText(v) || strings.HasPrefix(v, "/") || strings.HasSuffix(v, "/") || strings.Contains(v, `\`) || path.Clean(v) != v || v == ".." || strings.HasPrefix(v, "../") {
		return false
	}
	for _, segment := range strings.Split(v, "/") {
		if segment == "" || segment == "." || segment == ".." || (len(segment) >= 2 && segment[1] == ':') {
			return false
		}
	}
	return true
}

func validText(v string) bool {
	if !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func sha256Identity(v []byte) string {
	sum := sha256.Sum256(v)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func filesByDigest(files []SourceFile) map[string]SourceFile {
	out := make(map[string]SourceFile, len(files))
	for _, f := range files {
		if _, ok := out[f.Digest]; !ok {
			out[f.Digest] = f
		}
	}
	return out
}
func cloneFiles(files []SourceFile) []SourceFile {
	out := make([]SourceFile, len(files))
	for i, f := range files {
		out[i] = f
		out[i].Bytes = append([]byte(nil), f.Bytes...)
	}
	return out
}
func planEntries(plan projectpostgres.SyncPlan) []projectpostgres.SourceSnapshotEntryInput {
	out := make([]projectpostgres.SourceSnapshotEntryInput, len(plan.Entries))
	for i, e := range plan.Entries {
		out[i] = projectpostgres.SourceSnapshotEntryInput{Path: e.Path, Digest: e.Digest, SizeBytes: e.SizeBytes, Ordinal: i}
	}
	return out
}
