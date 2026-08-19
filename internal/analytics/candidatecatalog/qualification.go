package candidatecatalog

// This file contains the last, build-local part of the candidate protocol.
// A WorkingCatalog is normalized while it is still writable, then detached,
// hashed, and qualified as one immutable artifact.  In particular, this code
// never invokes DuckLake's physical maintenance functions: snapshot expiry is
// metadata-only and the physical pool remains the responsibility of global GC.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/extension"
)

var (
	ErrNormalizationFailed      = errors.New("candidate catalog normalization failed")
	ErrQualificationFailed      = errors.New("candidate catalog qualification failed")
	ErrQualificationPolicy      = errors.New("candidate qualification policy is required")
	ErrUnexpectedRelation       = errors.New("candidate catalog is missing an expected relation")
	ErrUnexpectedSchema         = errors.New("candidate catalog is missing an expected schema")
	ErrSnapshotState            = errors.New("candidate catalog does not contain exactly one current snapshot")
	ErrPhysicalReference        = errors.New("candidate file reference is outside the admitted physical pool")
	ErrObjectProbe              = errors.New("candidate file reference is not readable")
	ErrCatalogDigest            = errors.New("qualified catalog digest mismatch")
	ErrCatalogSize              = errors.New("qualified catalog size mismatch")
	ErrQualifiedCatalogRequired = errors.New("qualified catalog is required")
	ErrIncompleteQualification  = errors.New("qualification evidence is incomplete")
)

const qualificationRecordVersion = 1

// ObjectProbe is deliberately an object capability, rather than a filesystem
// walk or a storage client with credentials.  Target code owns the capability
// and decides how an admitted local/object-store reference is opened.
type ObjectProbe interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

// ObjectProbeFunc adapts a target-owned function to ObjectProbe.
type ObjectProbeFunc func(context.Context, string) (io.ReadCloser, error)

func (f ObjectProbeFunc) Open(ctx context.Context, reference string) (io.ReadCloser, error) {
	if f == nil {
		return nil, errors.New("nil object probe")
	}
	return f(ctx, reference)
}

// LocalObjectProbe is the safe default for a local physical pool.  It opens
// only the canonical path supplied by qualification; it never enumerates a
// directory and never removes anything.
type LocalObjectProbe struct{}

func (LocalObjectProbe) Open(_ context.Context, reference string) (io.ReadCloser, error) {
	return os.Open(reference)
}

// LogicalRelation identifies one expected base table in a candidate.
type LogicalRelation struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// QualificationExpectations are the portions of the compiled contract which
// can be checked without making this package depend on the project compiler.
// Digests are opaque control-plane identities; the policy callback decides
// their meaning and is given the exact catalog closure below.
type QualificationExpectations struct {
	Schemas                     []string          `json:"schemas,omitempty"`
	Relations                   []LogicalRelation `json:"relations,omitempty"`
	GraphDigest                 string            `json:"graphDigest,omitempty"`
	SchemaDigest                string            `json:"schemaDigest,omitempty"`
	ContractsDigest             string            `json:"contractsDigest,omitempty"`
	TestsDigest                 string            `json:"testsDigest,omitempty"`
	AuditsDigest                string            `json:"auditsDigest,omitempty"`
	DataDiffDigest              string            `json:"dataDiffDigest,omitempty"`
	ReviewerAuthorizationDigest string            `json:"reviewerAuthorizationDigest,omitempty"`
	AllowAdditional             bool              `json:"allowAdditional,omitempty"`
}

// QualificationCheck is a check over the exact normalized state.  Keeping
// the callback's input value-only prevents it from obtaining a writable
// Environment or a storage credential.
type QualificationCheck func(context.Context, QualificationInput) error

// QualificationChecks names the policy dimensions explicitly.  A caller may
// use the single Policy callback instead; these fields make it possible for a
// control-plane policy to retain separate evidence for graph, schema,
// contracts, tests, audits, data diffs, and reviewer authorization.
type QualificationChecks struct {
	LogicalGraph          QualificationCheck
	Schema                QualificationCheck
	Contracts             QualificationCheck
	Tests                 QualificationCheck
	Audits                QualificationCheck
	DataDiffs             QualificationCheck
	ReviewerAuthorization QualificationCheck
}

// QualificationPolicy is the final target-owned policy callback.  It runs
// only after the catalog has been detached and its exact bytes and closure
// have been captured.
type QualificationPolicy func(context.Context, QualificationInput) error

// QualificationRequest controls normalization, qualification, and the
// target-owned read-only preview.  Pool identity and lease are inherited from
// WorkingCatalog's construction request and cannot be overridden here.
type QualificationRequest struct {
	CatalogID string

	// These flattened fields are convenient for callers; Expected is merged
	// with them, with duplicates removed deterministically.
	ExpectedSchemas []string
	ExpectedTables  []LogicalRelation
	Expected        QualificationExpectations
	AllowAdditional bool

	GraphDigest                 string
	SchemaDigest                string
	ContractsDigest             string
	TestsDigest                 string
	AuditsDigest                string
	DataDiffDigest              string
	ReviewerAuthorizationDigest string
	PolicyDigest                string
	ReviewerPolicyDigest        string

	Probe               ObjectProbe
	CredentialBootstrap ducklake.CredentialBootstrap
	Checks              QualificationChecks
	Policy              QualificationPolicy
}

// CatalogTable is one visible DuckLake base table.
type CatalogTable struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// FileReference is the canonical identity of one current data or delete file.
// Reference is retained as DuckLake reported it; Canonical is the exact
// pool-bound value passed to ObjectProbe.
type FileReference struct {
	Kind      ducklake.FileKind `json:"kind"`
	Reference string            `json:"reference"`
	Canonical string            `json:"canonical"`
}

// CatalogClosure is the complete current physical closure obtained from
// DuckLake metadata, not from SQLite or a directory listing.
type CatalogClosure struct {
	Tables      []CatalogTable  `json:"tables"`
	Files       []FileReference `json:"files"`
	DataFiles   []string        `json:"dataFiles"`
	DeleteFiles []string        `json:"deleteFiles"`
	Digest      string          `json:"digest"`
}

func cloneQualificationClosure(closure CatalogClosure) CatalogClosure {
	clone := closure
	clone.Tables = append([]CatalogTable(nil), closure.Tables...)
	clone.Files = append([]FileReference(nil), closure.Files...)
	clone.DataFiles = append([]string(nil), closure.DataFiles...)
	clone.DeleteFiles = append([]string(nil), closure.DeleteFiles...)
	return clone
}

// NormalizationResult is evidence produced while the handle is writable.  It
// is useful to diagnostics and is included (in canonical form) in the
// qualification input, but it is not itself a candidate or ready state.
type NormalizationResult struct {
	CurrentSnapshot  int64                       `json:"currentSnapshot"`
	ExpiredSnapshots []int64                     `json:"expiredSnapshots"`
	Snapshots        []ducklake.Snapshot         `json:"snapshots"`
	InliningPolicy   ducklake.DataInliningPolicy `json:"inliningPolicy"`
	Tables           []CatalogTable              `json:"tables"`
	Closure          CatalogClosure              `json:"closure"`
}

// QualificationInput is the immutable value supplied to checks and policy.
// CatalogDigest and CatalogSize are filled after DetachForSeal, so policy
// decisions bind the exact bytes that will be uploaded.
type QualificationInput struct {
	Record               QualificationRecord       `json:"record"`
	Expectations         QualificationExpectations `json:"expectations"`
	GraphDigest          string                    `json:"graphDigest,omitempty"`
	SchemaDigest         string                    `json:"schemaDigest,omitempty"`
	ContractsDigest      string                    `json:"contractsDigest,omitempty"`
	TestsDigest          string                    `json:"testsDigest,omitempty"`
	AuditsDigest         string                    `json:"auditsDigest,omitempty"`
	DataDiffDigest       string                    `json:"dataDiffDigest,omitempty"`
	ReviewerPolicyDigest string                    `json:"reviewerPolicyDigest,omitempty"`
}

// QualificationRecord is the durable, content-addressed evidence for one
// exact normalized catalog.  It contains no credentials and no mutable
// pointers. Digest is over this record with Digest itself omitted.
type QualificationRecord struct {
	Version              int                        `json:"version"`
	CatalogID            string                     `json:"catalogId"`
	CatalogDigest        string                     `json:"catalogDigest"`
	CatalogSize          int64                      `json:"catalogSize"`
	CurrentSnapshot      int64                      `json:"currentSnapshot"`
	PoolID               string                     `json:"poolId"`
	DataPath             string                     `json:"dataPath"`
	Compatibility        physicalpool.Compatibility `json:"compatibility"`
	Closure              CatalogClosure             `json:"closure"`
	GraphDigest          string                     `json:"graphDigest,omitempty"`
	SchemaDigest         string                     `json:"schemaDigest,omitempty"`
	ContractsDigest      string                     `json:"contractsDigest,omitempty"`
	TestsDigest          string                     `json:"testsDigest,omitempty"`
	AuditsDigest         string                     `json:"auditsDigest,omitempty"`
	DataDiffDigest       string                     `json:"dataDiffDigest,omitempty"`
	PolicyDigest         string                     `json:"policyDigest"`
	ReviewerPolicyDigest string                     `json:"reviewerPolicyDigest"`
	Digest               string                     `json:"digest,omitempty"`
}

// QualifiedCatalog owns the detached private artifact and its immutable
// qualification evidence. Removing it only removes the private staging;
// shared physical-pool objects are never touched.
type QualifiedCatalog struct {
	DetachedCatalog
	Record QualificationRecord

	contract           *ducklake.PoolContract
	probe              ObjectProbe
	extensionAdmission extension.Admission
	bootstrap          ducklake.CredentialBootstrap
}

// Qualification returns a copy of the immutable qualification record.
func (q QualifiedCatalog) Qualification() QualificationRecord {
	return cloneQualificationRecord(q.Record)
}

// Remove abandons this qualified artifact. It is intentionally the only
// lifecycle mutation exposed by the result.
func (q *QualifiedCatalog) Remove() error {
	if q == nil {
		return nil
	}
	return q.DetachedCatalog.Remove()
}

// PreviewCatalog is a strictly read-only attachment. It intentionally has no
// Exec or Commit method.
type PreviewCatalog struct {
	env *ducklake.Environment
}

// OpenReadOnlyCatalog attaches an uploaded catalog without exposing any
// mutation capability. It is used by seal verification after remote upload.
// Object-backed pools require the target-owned per-connector bootstrap so
// DuckDB cannot resolve ambient process credentials.
func OpenReadOnlyCatalog(ctx context.Context, rootDir, catalogPath string, contract *ducklake.PoolContract, admission extension.Admission, bootstrap ducklake.CredentialBootstrap) (*PreviewCatalog, error) {
	if contract == nil || contract.Validate() != nil || strings.TrimSpace(catalogPath) == "" {
		return nil, ErrQualifiedCatalogRequired
	}
	if admission == nil {
		return nil, fmt.Errorf("exact DuckDB extension admission is required for read-only catalog verification")
	}
	if strings.EqualFold(strings.TrimSpace(contract.Tuple.StorageImplementation), "s3") && bootstrap == nil {
		return nil, fmt.Errorf("target-owned S3 credential bootstrap is required for read-only catalog verification")
	}
	dataPath, err := contract.Pool.DataPath()
	if err != nil {
		return nil, err
	}
	env, err := ducklake.Open(ctx, ducklake.Config{RootDir: rootDir, CatalogPath: catalogPath, DataPath: dataPath, PhysicalPoolID: contract.Pool.ID.String(), SharedPool: true, Compatibility: contract.Tuple, PoolContract: contract, ExtensionAdmission: admission, ReadOnly: true, CredentialBootstrap: bootstrap})
	if err != nil {
		return nil, err
	}
	return &PreviewCatalog{env: env}, nil
}

func (p *PreviewCatalog) Close() error {
	if p == nil || p.env == nil {
		return nil
	}
	return p.env.Close()
}

func (p *PreviewCatalog) Query(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	if p == nil || p.env == nil {
		return nil, ErrClosed
	}
	return p.env.Query(ctx, plan)
}

func (p *PreviewCatalog) Snapshots(ctx context.Context) ([]ducklake.Snapshot, error) {
	if p == nil || p.env == nil {
		return nil, ErrClosed
	}
	return p.env.Snapshots(ctx)
}

func (p *PreviewCatalog) CurrentFileSet(ctx context.Context, catalogID, schema, table string) (ducklake.CatalogFileSet, error) {
	if p == nil || p.env == nil {
		return ducklake.CatalogFileSet{}, ErrClosed
	}
	return p.env.CurrentFileSet(ctx, catalogID, schema, table)
}

func (p *PreviewCatalog) DataInliningPolicy(ctx context.Context) (ducklake.DataInliningPolicy, error) {
	if p == nil || p.env == nil {
		return ducklake.DataInliningPolicy{}, ErrClosed
	}
	return p.env.DataInliningPolicy(ctx)
}

func (p *PreviewCatalog) ValidateNoLiveInlineData(ctx context.Context) error {
	if p == nil || p.env == nil {
		return ErrClosed
	}
	return p.env.ValidateNoLiveInlineData(ctx)
}

func (p *PreviewCatalog) VisibleTables(ctx context.Context) ([]CatalogTable, error) {
	if p == nil || p.env == nil {
		return nil, ErrClosed
	}
	return visiblePreviewTables(ctx, p)
}

func (p *PreviewCatalog) CurrentClosure(ctx context.Context, catalogID string) (CatalogClosure, error) {
	tables, err := p.VisibleTables(ctx)
	if err != nil {
		return CatalogClosure{}, err
	}
	return enumeratePreviewClosure(ctx, p, catalogID, tables)
}

// DataInliningPolicy inspects all process, attach, global, schema, and table
// inlining settings without exposing the underlying Environment.
func (w *WorkingCatalog) DataInliningPolicy(ctx context.Context) (ducklake.DataInliningPolicy, error) {
	if err := w.checkOpen(); err != nil {
		return ducklake.DataInliningPolicy{}, err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return ducklake.DataInliningPolicy{}, err
	}
	return w.env.DataInliningPolicy(ctx)
}

// ExpireSnapshots expires only the supplied historical snapshot IDs. This
// wrapper intentionally has no dry-run or cleanup capability.
func (w *WorkingCatalog) ExpireSnapshots(ctx context.Context, versions []int64) error {
	if err := w.checkOpen(); err != nil {
		return err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return err
	}
	if err := w.env.ExpireSnapshots(ctx, versions, false); err != nil {
		return err
	}
	return verifyLease(ctx, w.request)
}

// VisibleTables enumerates DuckLake base tables through DuckDB's catalog
// metadata and filters out temporary/internal relations.
func (w *WorkingCatalog) VisibleTables(ctx context.Context) ([]CatalogTable, error) {
	if err := w.checkOpen(); err != nil {
		return nil, err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return nil, err
	}
	return visibleTables(ctx, w)
}

// Normalize validates immutable catalog safety and performs metadata-only
// snapshot normalization and complete current closure enumeration. It leaves
// the WorkingCatalog open so the caller can run policy checks or retry before
// detaching.
func (w *WorkingCatalog) Normalize(ctx context.Context) (NormalizationResult, error) {
	if err := w.checkOpen(); err != nil {
		return NormalizationResult{}, err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return NormalizationResult{}, err
	}
	policy, err := w.DataInliningPolicy(ctx)
	if err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: normalize data-inlining policy: %v", ErrNormalizationFailed, err)
	}
	if err := policy.ValidateZero(); err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: %w", ErrNormalizationFailed, err)
	}
	if err := w.env.ValidateNoLiveInlineData(ctx); err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: %w", ErrNormalizationFailed, err)
	}
	verifiedPolicy, policyErr := w.DataInliningPolicy(ctx)
	if policyErr != nil {
		return NormalizationResult{}, fmt.Errorf("%w: re-read data-inlining policy: %v", ErrNormalizationFailed, policyErr)
	}
	if err := verifiedPolicy.ValidateZero(); err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: verify data-inlining policy: %w", ErrNormalizationFailed, err)
	}
	policy = verifiedPolicy
	snapshots, err := w.env.Snapshots(ctx)
	if err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: enumerate snapshots: %v", ErrNormalizationFailed, err)
	}
	if len(snapshots) == 0 {
		return NormalizationResult{}, fmt.Errorf("%w: no snapshots remain", ErrSnapshotState)
	}
	current, err := currentSnapshot(ctx, w)
	if err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: read current snapshot: %v", ErrNormalizationFailed, err)
	}
	var expire []int64
	for _, snapshot := range snapshots {
		if snapshot.ID != current {
			expire = append(expire, snapshot.ID)
		}
	}
	if len(expire) > 0 {
		if err := verifyLease(ctx, w.request); err != nil {
			return NormalizationResult{}, err
		}
		// ExpireSnapshots only updates this private metadata catalog. No cleanup,
		// orphan deletion, or checkpoint operation is reachable from this path.
		if err := w.ExpireSnapshots(ctx, expire); err != nil {
			return NormalizationResult{}, fmt.Errorf("%w: expire historical snapshots: %v", ErrNormalizationFailed, err)
		}
	}
	after, err := w.env.Snapshots(ctx)
	if err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: enumerate normalized snapshots: %v", ErrNormalizationFailed, err)
	}
	if len(after) != 1 || after[0].ID != current {
		return NormalizationResult{}, fmt.Errorf("%w: retained=%#v current=%d", ErrSnapshotState, after, current)
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return NormalizationResult{}, err
	}
	tables, err := w.VisibleTables(ctx)
	if err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: enumerate visible base tables: %v", ErrNormalizationFailed, err)
	}
	closure, err := enumerateClosure(ctx, w, tables)
	if err != nil {
		return NormalizationResult{}, fmt.Errorf("%w: enumerate current file closure: %v", ErrNormalizationFailed, err)
	}
	return NormalizationResult{CurrentSnapshot: current, ExpiredSnapshots: append([]int64(nil), expire...), Snapshots: after, InliningPolicy: policy, Tables: tables, Closure: closure}, nil
}

// NormalizeAndQualify is the complete private-candidate protocol. Any error
// closes and removes the working handle (or detached artifact), so no failed
// attempt can be mistaken for ready or active state.
func NormalizeAndQualify(ctx context.Context, working *WorkingCatalog, request QualificationRequest) (QualifiedCatalog, error) {
	if working == nil {
		return QualifiedCatalog{}, fmt.Errorf("%w: working catalog is required", ErrQualificationFailed)
	}
	if err := validateQualificationRequest(request); err != nil {
		_ = working.Close()
		return QualifiedCatalog{}, err
	}
	var normalized NormalizationResult
	var err error
	if working.normalized != nil {
		normalized = *working.normalized
	} else {
		normalized, err = working.Normalize(ctx)
		if err != nil {
			_ = working.Close()
			return QualifiedCatalog{}, err
		}
	}
	if err := validateExpectations(normalized.Tables, request); err != nil {
		_ = working.Close()
		return QualifiedCatalog{}, fmt.Errorf("%w: %v", ErrQualificationFailed, err)
	}
	if err := probeClosure(ctx, &normalized.Closure, request.Probe, working.request.PoolContract); err != nil {
		_ = working.Close()
		return QualifiedCatalog{}, fmt.Errorf("%w: %v", ErrQualificationFailed, err)
	}
	detached, err := working.DetachForSeal()
	if err != nil {
		_ = working.Close()
		return QualifiedCatalog{}, fmt.Errorf("%w: detach normalized catalog: %v", ErrQualificationFailed, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = detached.Remove()
		}
	}()
	catalogDigest, catalogSize, err := hashCatalogFile(detached.CatalogPath())
	if err != nil {
		return QualifiedCatalog{}, fmt.Errorf("%w: hash normalized catalog: %v", ErrQualificationFailed, err)
	}
	expectations := mergeExpectations(request)
	input := QualificationInput{Expectations: expectations, GraphDigest: expectations.GraphDigest, SchemaDigest: expectations.SchemaDigest, ContractsDigest: expectations.ContractsDigest, TestsDigest: expectations.TestsDigest, AuditsDigest: expectations.AuditsDigest, DataDiffDigest: expectations.DataDiffDigest, ReviewerPolicyDigest: reviewerDigest(request, expectations)}
	record := QualificationRecord{Version: qualificationRecordVersion, CatalogID: firstNonEmpty(request.CatalogID, working.request.AttemptID), CatalogDigest: catalogDigest, CatalogSize: catalogSize, CurrentSnapshot: normalized.CurrentSnapshot, PoolID: working.request.PoolContract.Pool.ID.String(), DataPath: mustPoolDataPath(working.request.PoolContract), Compatibility: working.request.PoolContract.Tuple, Closure: normalized.Closure, GraphDigest: input.GraphDigest, SchemaDigest: input.SchemaDigest, ContractsDigest: input.ContractsDigest, TestsDigest: input.TestsDigest, AuditsDigest: input.AuditsDigest, DataDiffDigest: input.DataDiffDigest, PolicyDigest: policyDigest(request, expectations), ReviewerPolicyDigest: input.ReviewerPolicyDigest}
	record.Digest = digestRecord(record)
	input.Record = record
	for _, check := range []struct {
		name string
		fn   QualificationCheck
	}{{"logical graph", request.Checks.LogicalGraph}, {"schema", request.Checks.Schema}, {"contracts", request.Checks.Contracts}, {"tests", request.Checks.Tests}, {"audits", request.Checks.Audits}, {"data diffs", request.Checks.DataDiffs}, {"reviewer authorization", request.Checks.ReviewerAuthorization}} {
		if check.fn == nil {
			continue
		}
		if err := check.fn(ctx, input); err != nil {
			return QualifiedCatalog{}, fmt.Errorf("%w: %s check: %v", ErrQualificationFailed, check.name, err)
		}
	}
	if request.Policy != nil {
		if err := request.Policy(ctx, input); err != nil {
			return QualifiedCatalog{}, fmt.Errorf("%w: policy callback: %v", ErrQualificationFailed, err)
		}
	}
	qualified := QualifiedCatalog{DetachedCatalog: detached, Record: record, contract: working.request.PoolContract, probe: request.Probe, extensionAdmission: working.request.ExtensionAdmission, bootstrap: request.CredentialBootstrap}
	// Prove the exact artifact through the same read-only attach path used by
	// serving before returning a qualified result.
	preview, previewErr := OpenQualifiedPreview(ctx, qualified)
	if previewErr != nil {
		return QualifiedCatalog{}, fmt.Errorf("%w: read-only verification: %v", ErrQualificationFailed, previewErr)
	}
	if closeErr := preview.Close(); closeErr != nil {
		return QualifiedCatalog{}, fmt.Errorf("%w: close read-only verification: %v", ErrQualificationFailed, closeErr)
	}
	cleanup = false
	return qualified, nil
}

func (c QualificationChecks) empty() bool {
	return c.LogicalGraph == nil && c.Schema == nil && c.Contracts == nil && c.Tests == nil && c.Audits == nil && c.DataDiffs == nil && c.ReviewerAuthorization == nil
}

func (c QualificationChecks) complete() bool {
	return c.LogicalGraph != nil && c.Schema != nil && c.Contracts != nil && c.Tests != nil && c.Audits != nil && c.DataDiffs != nil && c.ReviewerAuthorization != nil
}

func validateQualificationRequest(request QualificationRequest) error {
	expected := mergeExpectations(request)
	if strings.TrimSpace(request.PolicyDigest) != "" && !validSHA256Digest(request.PolicyDigest) {
		return fmt.Errorf("%w: policy digest is not sha256:<64 hex characters>", ErrIncompleteQualification)
	}
	if strings.TrimSpace(request.ReviewerPolicyDigest) != "" && !validSHA256Digest(request.ReviewerPolicyDigest) {
		return fmt.Errorf("%w: reviewer policy digest is not sha256:<64 hex characters>", ErrIncompleteQualification)
	}
	for name, value := range map[string]string{
		"graph": expected.GraphDigest, "schema": expected.SchemaDigest,
		"contracts": expected.ContractsDigest, "tests": expected.TestsDigest,
		"audits": expected.AuditsDigest, "data diff": expected.DataDiffDigest,
		"reviewer authorization": expected.ReviewerAuthorizationDigest,
	} {
		if strings.TrimSpace(value) != "" && !validSHA256Digest(value) {
			return fmt.Errorf("%w: %s digest is not sha256:<64 hex characters>", ErrIncompleteQualification, name)
		}
	}
	if request.Policy != nil {
		if !validSHA256Digest(request.PolicyDigest) {
			return fmt.Errorf("%w: one policy requires an explicit policy digest", ErrIncompleteQualification)
		}
		reviewer := firstNonEmpty(request.ReviewerPolicyDigest, request.ReviewerAuthorizationDigest, expected.ReviewerAuthorizationDigest)
		if !validSHA256Digest(reviewer) {
			return fmt.Errorf("%w: reviewer authorization digest is required", ErrIncompleteQualification)
		}
		return nil
	}
	if !request.Checks.complete() {
		return fmt.Errorf("%w: provide one complete policy callback or all named checks", ErrQualificationPolicy)
	}
	if !validSHA256Digest(firstNonEmpty(request.ReviewerPolicyDigest, request.ReviewerAuthorizationDigest, expected.ReviewerAuthorizationDigest)) {
		return fmt.Errorf("%w: reviewer authorization digest is required", ErrIncompleteQualification)
	}
	return nil
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func cloneQualificationRecord(record QualificationRecord) QualificationRecord {
	clone := record
	clone.Closure.Tables = append([]CatalogTable(nil), record.Closure.Tables...)
	clone.Closure.Files = append([]FileReference(nil), record.Closure.Files...)
	clone.Closure.DataFiles = append([]string(nil), record.Closure.DataFiles...)
	clone.Closure.DeleteFiles = append([]string(nil), record.Closure.DeleteFiles...)
	return clone
}

// OpenQualifiedPreview opens the exact qualified artifact under a target-owned
// credential bootstrap. ReadOnly is forced true and the returned wrapper has
// no mutation capability.
func OpenQualifiedPreview(ctx context.Context, qualified QualifiedCatalog) (*PreviewCatalog, error) {
	if qualified.state == nil || qualified.contract == nil {
		return nil, ErrQualifiedCatalogRequired
	}
	if digestRecord(qualified.Record) != qualified.Record.Digest || digestClosure(qualified.Record.Closure) != qualified.Record.Closure.Digest {
		return nil, ErrCatalogDigest
	}
	dataPath, err := qualified.contract.Pool.DataPath()
	if err != nil {
		return nil, err
	}
	digest, size, err := hashCatalogFile(qualified.CatalogPath())
	if err != nil {
		return nil, err
	}
	if digest != qualified.Record.CatalogDigest {
		return nil, ErrCatalogDigest
	}
	if size != qualified.Record.CatalogSize {
		return nil, ErrCatalogSize
	}
	env, err := ducklake.Open(ctx, ducklake.Config{RootDir: qualified.StagingPath(), CatalogPath: qualified.CatalogPath(), DataPath: dataPath, PhysicalPoolID: qualified.contract.Pool.ID.String(), SharedPool: true, Compatibility: qualified.contract.Tuple, PoolContract: qualified.contract, ExtensionAdmission: qualified.extensionAdmission, ReadOnly: true, CredentialBootstrap: qualified.bootstrap})
	if err != nil {
		return nil, err
	}
	preview := &PreviewCatalog{env: env}
	if err := verifyPreviewState(ctx, preview, qualified); err != nil {
		_ = preview.Close()
		return nil, err
	}
	return preview, nil
}

func currentSnapshot(ctx context.Context, w *WorkingCatalog) (int64, error) {
	rows, err := w.Query(ctx, semanticquery.Plan{SQL: "SELECT id FROM ducklake_current_snapshot(?)", Args: []any{"lake"}, Columns: []string{"id"}})
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, fmt.Errorf("current snapshot query returned %d rows", len(rows))
	}
	switch value := rows[0]["id"].(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case uint64:
		return int64(value), nil
	case uint32:
		return int64(value), nil
	default:
		return 0, fmt.Errorf("current snapshot has unexpected type %T", rows[0]["id"])
	}
}

func visibleTables(ctx context.Context, w *WorkingCatalog) ([]CatalogTable, error) {
	rows, err := w.Query(ctx, semanticquery.Plan{SQL: "SELECT schema_name, table_name FROM duckdb_tables() WHERE database_name = 'lake' AND NOT internal AND NOT temporary ORDER BY schema_name, table_name", Columns: []string{"schema_name", "table_name"}})
	if err != nil {
		return nil, err
	}
	tables := make([]CatalogTable, 0, len(rows))
	for _, row := range rows {
		schema, okSchema := row["schema_name"].(string)
		table, okTable := row["table_name"].(string)
		if !okSchema || !okTable || strings.TrimSpace(schema) == "" || strings.TrimSpace(table) == "" {
			return nil, fmt.Errorf("DuckDB table metadata has invalid schema/table")
		}
		tables = append(tables, CatalogTable{Schema: schema, Table: table})
	}
	return tables, nil
}

func enumerateClosure(ctx context.Context, w *WorkingCatalog, tables []CatalogTable) (CatalogClosure, error) {
	closure := CatalogClosure{Tables: append([]CatalogTable(nil), tables...)}
	for _, table := range tables {
		set, err := w.CurrentFileSet(ctx, w.request.PoolContract.Pool.ID.String(), table.Schema, table.Table)
		if err != nil {
			return CatalogClosure{}, err
		}
		for _, reference := range set.DataFiles {
			closure.Files = append(closure.Files, FileReference{Kind: ducklake.DataFile, Reference: reference})
		}
		for _, reference := range set.DeleteFiles {
			closure.Files = append(closure.Files, FileReference{Kind: ducklake.DeleteFile, Reference: reference})
		}
	}
	sort.Slice(closure.Files, func(i, j int) bool {
		if closure.Files[i].Kind == closure.Files[j].Kind {
			return closure.Files[i].Reference < closure.Files[j].Reference
		}
		return closure.Files[i].Kind < closure.Files[j].Kind
	})
	return closure, nil
}

func probeClosure(ctx context.Context, closure *CatalogClosure, probe ObjectProbe, contract *ducklake.PoolContract) error {
	if closure == nil {
		return ErrPhysicalReference
	}
	dataPath, err := contract.Pool.DataPath()
	if err != nil {
		return err
	}
	if probe == nil {
		if strings.Contains(dataPath, "://") {
			return fmt.Errorf("%w: remote physical pools require a target-owned probe", ErrObjectProbe)
		}
		probe = LocalObjectProbe{}
	}
	seen := map[string]struct{}{}
	canonicalFiles := make([]FileReference, 0, len(closure.Files))
	for i := range closure.Files {
		file := &closure.Files[i]
		canonical, err := canonicalPoolReference(dataPath, file.Reference)
		if err != nil {
			return fmt.Errorf("%s %q: %w", file.Kind, file.Reference, err)
		}
		if !strings.Contains(dataPath, "://") {
			if resolved, evalErr := filepath.EvalSymlinks(canonical); evalErr == nil {
				if !filepathWithin(dataPath, resolved) {
					return fmt.Errorf("%s %q: %w", file.Kind, canonical, ErrPhysicalReference)
				}
				canonical = filepath.Clean(resolved)
			}
		}
		file.Canonical = canonical
		file.Reference = canonical
		key := string(file.Kind) + "\x00" + canonical
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		canonicalFiles = append(canonicalFiles, *file)
		reader, err := probe.Open(ctx, canonical)
		if err != nil {
			return fmt.Errorf("%s %q: %w: %v", file.Kind, canonical, ErrObjectProbe, err)
		}
		if reader == nil {
			return fmt.Errorf("%s %q: %w: probe returned nil reader", file.Kind, canonical, ErrObjectProbe)
		}
		var one [1]byte
		_, readErr := reader.Read(one[:])
		closeErr := reader.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("%s %q: %w: %v", file.Kind, canonical, ErrObjectProbe, readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("%s %q: %w: %v", file.Kind, canonical, ErrObjectProbe, closeErr)
		}
	}
	sort.Slice(canonicalFiles, func(i, j int) bool {
		if canonicalFiles[i].Kind == canonicalFiles[j].Kind {
			return canonicalFiles[i].Canonical < canonicalFiles[j].Canonical
		}
		return canonicalFiles[i].Kind < canonicalFiles[j].Kind
	})
	closure.Files = canonicalFiles
	closure.DataFiles = nil
	closure.DeleteFiles = nil
	for _, file := range canonicalFiles {
		if file.Kind == ducklake.DataFile {
			closure.DataFiles = append(closure.DataFiles, file.Canonical)
		} else if file.Kind == ducklake.DeleteFile {
			closure.DeleteFiles = append(closure.DeleteFiles, file.Canonical)
		}
	}
	sort.Strings(closure.DataFiles)
	sort.Strings(closure.DeleteFiles)
	closure.Digest = digestClosure(*closure)
	return nil
}

func verifyPreviewState(ctx context.Context, preview *PreviewCatalog, qualified QualifiedCatalog) error {
	snapshots, err := preview.Snapshots(ctx)
	if err != nil {
		return err
	}
	if len(snapshots) != 1 || snapshots[0].ID != qualified.Record.CurrentSnapshot {
		return ErrSnapshotState
	}
	tables, err := visiblePreviewTables(ctx, preview)
	if err != nil {
		return err
	}
	closure, err := enumeratePreviewClosure(ctx, preview, qualified.contract.Pool.ID.String(), tables)
	if err != nil {
		return err
	}
	if err := probeClosure(ctx, &closure, qualified.probe, qualified.contract); err != nil {
		return err
	}
	if closure.Digest != qualified.Record.Closure.Digest {
		return fmt.Errorf("%w: physical closure changed", ErrCatalogDigest)
	}
	return nil
}

func visiblePreviewTables(ctx context.Context, p *PreviewCatalog) ([]CatalogTable, error) {
	rows, err := p.Query(ctx, semanticquery.Plan{SQL: "SELECT schema_name, table_name FROM duckdb_tables() WHERE database_name = 'lake' AND NOT internal AND NOT temporary ORDER BY schema_name, table_name", Columns: []string{"schema_name", "table_name"}})
	if err != nil {
		return nil, err
	}
	result := make([]CatalogTable, 0, len(rows))
	for _, row := range rows {
		schema, okSchema := row["schema_name"].(string)
		table, okTable := row["table_name"].(string)
		if okSchema && okTable {
			result = append(result, CatalogTable{Schema: schema, Table: table})
		}
	}
	return result, nil
}

func enumeratePreviewClosure(ctx context.Context, p *PreviewCatalog, catalogID string, tables []CatalogTable) (CatalogClosure, error) {
	closure := CatalogClosure{Tables: append([]CatalogTable(nil), tables...)}
	for _, table := range tables {
		set, err := p.CurrentFileSet(ctx, catalogID, table.Schema, table.Table)
		if err != nil {
			return CatalogClosure{}, err
		}
		for _, ref := range set.DataFiles {
			closure.Files = append(closure.Files, FileReference{Kind: ducklake.DataFile, Reference: ref, Canonical: ref})
		}
		for _, ref := range set.DeleteFiles {
			closure.Files = append(closure.Files, FileReference{Kind: ducklake.DeleteFile, Reference: ref, Canonical: ref})
		}
	}
	sort.Slice(closure.Files, func(i, j int) bool {
		if closure.Files[i].Kind == closure.Files[j].Kind {
			return closure.Files[i].Canonical < closure.Files[j].Canonical
		}
		return closure.Files[i].Kind < closure.Files[j].Kind
	})
	for _, file := range closure.Files {
		if file.Kind == ducklake.DataFile {
			closure.DataFiles = append(closure.DataFiles, file.Canonical)
		} else {
			closure.DeleteFiles = append(closure.DeleteFiles, file.Canonical)
		}
	}
	closure.Digest = digestClosure(closure)
	return closure, nil
}

func validateExpectations(tables []CatalogTable, request QualificationRequest) error {
	expected := mergeExpectations(request)
	seenTables := map[string]struct{}{}
	seenSchemas := map[string]struct{}{}
	for _, table := range tables {
		seenTables[strings.ToLower(table.Schema+"\x00"+table.Table)] = struct{}{}
		seenSchemas[strings.ToLower(table.Schema)] = struct{}{}
	}
	for _, schema := range expected.Schemas {
		if _, ok := seenSchemas[strings.ToLower(strings.TrimSpace(schema))]; !ok {
			return fmt.Errorf("%w: %s", ErrUnexpectedSchema, schema)
		}
	}
	for _, relation := range expected.Relations {
		key := strings.ToLower(strings.TrimSpace(relation.Schema) + "\x00" + strings.TrimSpace(relation.Table))
		if _, ok := seenTables[key]; !ok {
			return fmt.Errorf("%w: %s.%s", ErrUnexpectedRelation, relation.Schema, relation.Table)
		}
	}
	if !expected.AllowAdditional {
		if len(expected.Relations) > 0 && len(seenTables) != len(expected.Relations) {
			return fmt.Errorf("%w: expected %d relations, found %d", ErrUnexpectedRelation, len(expected.Relations), len(seenTables))
		}
		if len(expected.Schemas) > 0 && len(seenSchemas) != len(expected.Schemas) {
			return fmt.Errorf("%w: expected %d schemas, found %d", ErrUnexpectedSchema, len(expected.Schemas), len(seenSchemas))
		}
	}
	return nil
}

func mergeExpectations(request QualificationRequest) QualificationExpectations {
	expected := request.Expected
	expected.Schemas = append(expected.Schemas, request.ExpectedSchemas...)
	expected.Relations = append(expected.Relations, request.ExpectedTables...)
	expected.GraphDigest = firstNonEmpty(request.GraphDigest, expected.GraphDigest)
	expected.SchemaDigest = firstNonEmpty(request.SchemaDigest, expected.SchemaDigest)
	expected.ContractsDigest = firstNonEmpty(request.ContractsDigest, expected.ContractsDigest)
	expected.TestsDigest = firstNonEmpty(request.TestsDigest, expected.TestsDigest)
	expected.AuditsDigest = firstNonEmpty(request.AuditsDigest, expected.AuditsDigest)
	expected.DataDiffDigest = firstNonEmpty(request.DataDiffDigest, expected.DataDiffDigest)
	expected.ReviewerAuthorizationDigest = firstNonEmpty(request.ReviewerAuthorizationDigest, expected.ReviewerAuthorizationDigest)
	expected.AllowAdditional = expected.AllowAdditional || request.AllowAdditional
	sort.Strings(expected.Schemas)
	expected.Schemas = uniqueStrings(expected.Schemas)
	sort.Slice(expected.Relations, func(i, j int) bool {
		if expected.Relations[i].Schema == expected.Relations[j].Schema {
			return expected.Relations[i].Table < expected.Relations[j].Table
		}
		return expected.Relations[i].Schema < expected.Relations[j].Schema
	})
	uniqueRelations := expected.Relations[:0]
	seen := map[string]struct{}{}
	for _, relation := range expected.Relations {
		key := strings.ToLower(strings.TrimSpace(relation.Schema) + "\x00" + strings.TrimSpace(relation.Table))
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			uniqueRelations = append(uniqueRelations, relation)
		}
	}
	expected.Relations = uniqueRelations
	return expected
}

func canonicalPoolReference(dataPath, reference string) (string, error) {
	dataPath = strings.TrimSpace(dataPath)
	reference = strings.TrimSpace(strings.ReplaceAll(reference, "\\", "/"))
	if dataPath == "" || reference == "" {
		return "", ErrPhysicalReference
	}
	if strings.Contains(dataPath, "://") {
		base, err := url.Parse(dataPath)
		if err != nil || base.Host == "" {
			return "", ErrPhysicalReference
		}
		if strings.Contains(reference, "://") {
			ref, parseErr := url.Parse(reference)
			if parseErr != nil || strings.ToLower(ref.Scheme) != strings.ToLower(base.Scheme) || strings.ToLower(ref.Host) != strings.ToLower(base.Host) || ref.RawQuery != "" || ref.Fragment != "" {
				return "", ErrPhysicalReference
			}
			reference = ref.Path
		}
		if strings.HasPrefix(reference, "/") {
			// Absolute object paths are allowed only beneath the pool prefix.
			if !pathWithin(base.Path, reference) {
				return "", ErrPhysicalReference
			}
			joined := path.Clean(reference)
			base.Path = joined
		} else {
			clean := path.Clean(path.Join(base.Path, reference))
			if !pathWithin(base.Path, clean) {
				return "", ErrPhysicalReference
			}
			base.Path = clean
		}
		base.RawPath = ""
		return base.String(), nil
	}
	base, err := filepath.Abs(dataPath)
	if err != nil {
		return "", ErrPhysicalReference
	}
	var resolved string
	if filepath.IsAbs(filepath.FromSlash(reference)) {
		resolved = filepath.Clean(filepath.FromSlash(reference))
	} else {
		resolved = filepath.Clean(filepath.Join(base, filepath.FromSlash(reference)))
	}
	if !filepathWithin(base, resolved) {
		return "", ErrPhysicalReference
	}
	return resolved, nil
}

// CanonicalPoolReference binds a DuckLake data/delete reference to the
// admitted pool namespace before an object capability opens it.
func CanonicalPoolReference(dataPath, reference string) (string, error) {
	return canonicalPoolReference(dataPath, reference)
}

func pathWithin(base, candidate string) bool {
	base = path.Clean("/" + strings.TrimPrefix(base, "/"))
	candidate = path.Clean("/" + strings.TrimPrefix(candidate, "/"))
	return candidate == base || strings.HasPrefix(candidate, strings.TrimSuffix(base, "/")+"/")
}

func filepathWithin(base, candidate string) bool {
	base = filepath.Clean(base)
	candidate = filepath.Clean(candidate)
	return candidate == base || strings.HasPrefix(candidate, base+string(filepath.Separator))
}

func hashCatalogFile(name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func digestClosure(closure CatalogClosure) string {
	copyClosure := closure
	copyClosure.Digest = ""
	encoded, _ := json.Marshal(copyClosure)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestRecord(record QualificationRecord) string {
	record.Digest = ""
	encoded, _ := json.Marshal(record)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func policyDigest(request QualificationRequest, expectations QualificationExpectations) string {
	if request.PolicyDigest != "" {
		return request.PolicyDigest
	}
	encoded, _ := json.Marshal(struct {
		Expectations QualificationExpectations `json:"expectations"`
		Checks       QualificationExpectations `json:"checks"`
	}{expectations, expectations})
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func reviewerDigest(request QualificationRequest, expectations QualificationExpectations) string {
	if request.ReviewerPolicyDigest != "" {
		return request.ReviewerPolicyDigest
	}
	if request.ReviewerAuthorizationDigest != "" {
		return request.ReviewerAuthorizationDigest
	}
	return expectations.ReviewerAuthorizationDigest
}

func mustPoolDataPath(contract *ducklake.PoolContract) string {
	if contract == nil {
		return ""
	}
	value, _ := contract.Pool.DataPath()
	return value
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	seen := map[string]struct{}{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, strings.TrimSpace(value))
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
