// Package migrationcompatibility resolves one authoritative, cross-domain
// compatibility projection for an exact release artifact pair.
//
// The resolver is read-only. It does not inspect caller-created projections,
// execute migrations, or infer compatibility from configuration. Each domain
// projection must be returned by its owning subsystem and bound to the same
// artifact pair and deployment target.
package migrationcompatibility

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

const (
	ProjectionVersion        = "migration-compatibility/v1"
	GooseOwnerVersion        = "goose-compatibility/v1"
	RiverOwnerVersion        = "river-compatibility/v1"
	DuckLakeOwnerVersion     = "ducklake-compatibility/v1"
	PhysicalPoolOwnerVersion = "physical-pool-compatibility/v1"
	DigestDomain             = "leapview/migration-compatibility/v1\n"
	MaxCanonicalBytes        = 64 << 10
)

var (
	ErrInvalidRequest       = errors.New("migration compatibility request is invalid")
	ErrOwnerUnavailable     = errors.New("migration compatibility owner is unavailable")
	ErrOwnerEvidenceMissing = errors.New("migration compatibility owner evidence is missing")
	ErrOwnerConflict        = errors.New("migration compatibility owner evidence conflicts")
	ErrUnsupportedVersion   = errors.New("migration compatibility owner version is unsupported")
	ErrInvalidProjection    = errors.New("migration compatibility projection is invalid")
)

type Binding struct {
	PredecessorArtifactDigest string `json:"predecessorArtifactDigest"`
	CandidateArtifactDigest   string `json:"candidateArtifactDigest"`
	TargetIdentityDigest      string `json:"targetIdentityDigest"`
}

type ControlEvidence struct {
	Version    string
	Binding    Binding
	Owner      string
	Projection transitionpreflight.PostgreSQLControlProjection
}

type RiverEvidence struct {
	Version                string
	Binding                Binding
	OperationalSchemaOwner string
	JobHistoryOwner        string
	Projection             transitionpreflight.RiverJobProjection
}

type DuckLakeEvidence struct {
	Version    string
	Binding    Binding
	Owner      string
	Projection transitionpreflight.DuckLakeProjection
}

type PhysicalPoolProjection struct {
	Compatibility        transitionpreflight.CompatibilityState `json:"compatibility"`
	Predecessor          physicalpool.Compatibility             `json:"predecessor"`
	Candidate            physicalpool.Compatibility             `json:"candidate"`
	TargetIdentityDigest string                                 `json:"targetIdentityDigest"`
}

type PhysicalPoolEvidence struct {
	Version    string
	Binding    Binding
	Owner      string
	Projection PhysicalPoolProjection
}

type Query struct {
	Binding Binding
}

type GooseOwner interface {
	ResolveGooseCompatibility(context.Context, Query) (ControlEvidence, error)
}

type RiverOwner interface {
	ResolveRiverCompatibility(context.Context, Query) (RiverEvidence, error)
}

type DuckLakeOwner interface {
	ResolveDuckLakeCompatibility(context.Context, Query) (DuckLakeEvidence, error)
}

type PhysicalPoolOwner interface {
	ResolvePhysicalPoolCompatibility(context.Context, Query) (PhysicalPoolEvidence, error)
}

type Projection struct {
	Version                   string                                          `json:"version"`
	PredecessorArtifactDigest string                                          `json:"predecessorArtifactDigest"`
	CandidateArtifactDigest   string                                          `json:"candidateArtifactDigest"`
	TargetIdentityDigest      string                                          `json:"targetIdentityDigest"`
	OverallCompatibility      transitionpreflight.CompatibilityState          `json:"overallCompatibility"`
	MigrationOwnership        transitionpreflight.MigrationOwnership          `json:"migrationOwnership"`
	DuckLakeOwner             string                                          `json:"duckLakeOwner"`
	PhysicalPoolOwner         string                                          `json:"physicalPoolOwner"`
	Control                   transitionpreflight.PostgreSQLControlProjection `json:"control"`
	River                     transitionpreflight.RiverJobProjection          `json:"river"`
	DuckLake                  transitionpreflight.DuckLakeProjection          `json:"duckLake"`
	PhysicalPool              PhysicalPoolProjection                          `json:"physicalPool"`
}

type Resolution struct {
	Projection     Projection
	CanonicalBytes []byte
	Digest         string
}

type Resolver struct {
	goose        GooseOwner
	river        RiverOwner
	duckLake     DuckLakeOwner
	physicalPool PhysicalPoolOwner
}

func NewResolver(goose GooseOwner, river RiverOwner, duckLake DuckLakeOwner, physicalPool PhysicalPoolOwner) *Resolver {
	return &Resolver{goose: goose, river: river, duckLake: duckLake, physicalPool: physicalPool}
}

func (r *Resolver) Resolve(ctx context.Context, targetIdentityDigest string, predecessor, candidate transitionpreflight.ArtifactIdentity) (Resolution, error) {
	if r == nil || nilOwner(r.goose) || nilOwner(r.river) || nilOwner(r.duckLake) || nilOwner(r.physicalPool) {
		return Resolution{}, ErrOwnerUnavailable
	}
	if platformdigest.ValidateSHA256Identity(targetIdentityDigest) != nil {
		return Resolution{}, fmt.Errorf("%w: target identity", ErrInvalidRequest)
	}
	predecessorDigest, err := predecessor.Digest()
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: predecessor artifact", ErrInvalidRequest)
	}
	candidateDigest, err := candidate.Digest()
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: candidate artifact", ErrInvalidRequest)
	}
	if predecessorDigest == candidateDigest || predecessor.Release.Image == candidate.Release.Image {
		return Resolution{}, fmt.Errorf("%w: artifact pair", ErrInvalidRequest)
	}
	binding := Binding{PredecessorArtifactDigest: predecessorDigest, CandidateArtifactDigest: candidateDigest, TargetIdentityDigest: targetIdentityDigest}
	query := Query{Binding: binding}

	control, err := r.goose.ResolveGooseCompatibility(ctx, query)
	if err != nil {
		return Resolution{}, ownerError("goose", err)
	}
	river, err := r.river.ResolveRiverCompatibility(ctx, query)
	if err != nil {
		return Resolution{}, ownerError("river", err)
	}
	duckLake, err := r.duckLake.ResolveDuckLakeCompatibility(ctx, query)
	if err != nil {
		return Resolution{}, ownerError("ducklake", err)
	}
	pool, err := r.physicalPool.ResolvePhysicalPoolCompatibility(ctx, query)
	if err != nil {
		return Resolution{}, ownerError("physical pool", err)
	}

	if err := validateControlEvidence(control, binding); err != nil {
		return Resolution{}, err
	}
	if err := validateRiverEvidence(river, binding); err != nil {
		return Resolution{}, err
	}
	if err := validateDuckLakeEvidence(duckLake, binding); err != nil {
		return Resolution{}, err
	}
	if err := validatePhysicalPoolEvidence(pool, binding); err != nil {
		return Resolution{}, err
	}
	if duckLake.Projection.Predecessor != pool.Projection.Predecessor || duckLake.Projection.Candidate != pool.Projection.Candidate {
		return Resolution{}, fmt.Errorf("%w: DuckLake and physical-pool tuples", ErrOwnerConflict)
	}

	overall := transitionpreflight.CompatibilityBackwardCompatible
	for _, state := range []transitionpreflight.CompatibilityState{
		control.Projection.Compatibility,
		river.Projection.SchemaCompatibility,
		river.Projection.JobHistoryCompatibility,
		duckLake.Projection.Compatibility,
		pool.Projection.Compatibility,
	} {
		if state == transitionpreflight.CompatibilityIncompatible {
			overall = transitionpreflight.CompatibilityIncompatible
		}
	}
	projection := Projection{
		Version:                   ProjectionVersion,
		PredecessorArtifactDigest: predecessorDigest,
		CandidateArtifactDigest:   candidateDigest,
		TargetIdentityDigest:      targetIdentityDigest,
		OverallCompatibility:      overall,
		MigrationOwnership: transitionpreflight.MigrationOwnership{
			GooseControlSchemaOwner:     control.Owner,
			RiverOperationalSchemaOwner: river.OperationalSchemaOwner,
			RiverJobHistoryOwner:        river.JobHistoryOwner,
		},
		DuckLakeOwner:     duckLake.Owner,
		PhysicalPoolOwner: pool.Owner,
		Control:           control.Projection,
		River:             river.Projection,
		DuckLake:          duckLake.Projection,
		PhysicalPool:      pool.Projection,
	}
	canonical, err := projection.CanonicalJSON()
	if err != nil {
		return Resolution{}, err
	}
	digest, err := projection.Digest()
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{Projection: projection, CanonicalBytes: canonical, Digest: digest}, nil
}

func (p Projection) CanonicalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	document, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode migration compatibility projection: %w", err)
	}
	if len(document) > MaxCanonicalBytes {
		return nil, fmt.Errorf("%w: canonical document exceeds %d bytes", ErrInvalidProjection, MaxCanonicalBytes)
	}
	return document, nil
}

func (p Projection) Digest() (string, error) {
	document, err := p.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(DigestDomain), document...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ParseCanonical(document []byte) (Projection, error) {
	if len(document) == 0 || len(document) > MaxCanonicalBytes {
		return Projection{}, ErrInvalidProjection
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var projection Projection
	if err := decoder.Decode(&projection); err != nil {
		return Projection{}, fmt.Errorf("%w: decode: %v", ErrInvalidProjection, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Projection{}, fmt.Errorf("%w: trailing data", ErrInvalidProjection)
	}
	canonical, err := projection.CanonicalJSON()
	if err != nil {
		return Projection{}, err
	}
	if !bytes.Equal(document, canonical) {
		return Projection{}, fmt.Errorf("%w: non-canonical bytes", ErrInvalidProjection)
	}
	return projection, nil
}

func (p Projection) Validate() error {
	if p.Version != ProjectionVersion {
		return ErrUnsupportedVersion
	}
	binding := Binding{p.PredecessorArtifactDigest, p.CandidateArtifactDigest, p.TargetIdentityDigest}
	if err := validateBinding(binding); err != nil {
		return err
	}
	if p.MigrationOwnership.GooseControlSchemaOwner != transitionpreflight.GooseControlSchemaOwnerLeapView ||
		p.MigrationOwnership.RiverOperationalSchemaOwner != transitionpreflight.RiverOperationalSchemaOwnerRiver ||
		p.MigrationOwnership.RiverJobHistoryOwner != transitionpreflight.RiverJobHistoryOwnerLeapView ||
		p.DuckLakeOwner != transitionpreflight.OwnerLeapView || p.PhysicalPoolOwner != transitionpreflight.OwnerLeapView {
		return fmt.Errorf("%w: subsystem ownership", ErrInvalidProjection)
	}
	if err := validateControlProjection(p.Control, p.TargetIdentityDigest); err != nil {
		return err
	}
	if err := validateRiverProjection(p.River, p.TargetIdentityDigest); err != nil {
		return err
	}
	if err := validateDuckLakeProjection(p.DuckLake, p.TargetIdentityDigest); err != nil {
		return err
	}
	if err := validatePoolProjection(p.PhysicalPool, p.TargetIdentityDigest); err != nil {
		return err
	}
	if p.DuckLake.Predecessor != p.PhysicalPool.Predecessor || p.DuckLake.Candidate != p.PhysicalPool.Candidate {
		return fmt.Errorf("%w: cross-domain tuples", ErrInvalidProjection)
	}
	expected := transitionpreflight.CompatibilityBackwardCompatible
	for _, state := range []transitionpreflight.CompatibilityState{p.Control.Compatibility, p.River.SchemaCompatibility, p.River.JobHistoryCompatibility, p.DuckLake.Compatibility, p.PhysicalPool.Compatibility} {
		if state == transitionpreflight.CompatibilityIncompatible {
			expected = transitionpreflight.CompatibilityIncompatible
		}
	}
	if p.OverallCompatibility != expected {
		return fmt.Errorf("%w: overall compatibility", ErrInvalidProjection)
	}
	return nil
}

func validateControlEvidence(e ControlEvidence, binding Binding) error {
	if e.Version != GooseOwnerVersion {
		return ErrUnsupportedVersion
	}
	if e.Binding != binding {
		return fmt.Errorf("%w: Goose binding", ErrOwnerConflict)
	}
	if e.Owner != transitionpreflight.GooseControlSchemaOwnerLeapView {
		return fmt.Errorf("%w: Goose owner", ErrOwnerConflict)
	}
	return validateControlProjection(e.Projection, binding.TargetIdentityDigest)
}

func validateRiverEvidence(e RiverEvidence, binding Binding) error {
	if e.Version != RiverOwnerVersion {
		return ErrUnsupportedVersion
	}
	if e.Binding != binding {
		return fmt.Errorf("%w: River binding", ErrOwnerConflict)
	}
	if e.OperationalSchemaOwner != transitionpreflight.RiverOperationalSchemaOwnerRiver || e.JobHistoryOwner != transitionpreflight.RiverJobHistoryOwnerLeapView {
		return fmt.Errorf("%w: River owner", ErrOwnerConflict)
	}
	return validateRiverProjection(e.Projection, binding.TargetIdentityDigest)
}

func validateDuckLakeEvidence(e DuckLakeEvidence, binding Binding) error {
	if e.Version != DuckLakeOwnerVersion {
		return ErrUnsupportedVersion
	}
	if e.Binding != binding {
		return fmt.Errorf("%w: DuckLake binding", ErrOwnerConflict)
	}
	if e.Owner != transitionpreflight.OwnerLeapView {
		return fmt.Errorf("%w: DuckLake owner", ErrOwnerConflict)
	}
	return validateDuckLakeProjection(e.Projection, binding.TargetIdentityDigest)
}

func validatePhysicalPoolEvidence(e PhysicalPoolEvidence, binding Binding) error {
	if e.Version != PhysicalPoolOwnerVersion {
		return ErrUnsupportedVersion
	}
	if e.Binding != binding {
		return fmt.Errorf("%w: physical-pool binding", ErrOwnerConflict)
	}
	if e.Owner != transitionpreflight.OwnerLeapView {
		return fmt.Errorf("%w: physical-pool owner", ErrOwnerConflict)
	}
	return validatePoolProjection(e.Projection, binding.TargetIdentityDigest)
}

func validateBinding(binding Binding) error {
	if platformdigest.ValidateSHA256Identity(binding.PredecessorArtifactDigest) != nil || platformdigest.ValidateSHA256Identity(binding.CandidateArtifactDigest) != nil || platformdigest.ValidateSHA256Identity(binding.TargetIdentityDigest) != nil || binding.PredecessorArtifactDigest == binding.CandidateArtifactDigest {
		return fmt.Errorf("%w: identity binding", ErrInvalidProjection)
	}
	return nil
}

func validateControlProjection(p transitionpreflight.PostgreSQLControlProjection, target string) error {
	if err := validateState(p.Compatibility); err != nil {
		return err
	}
	if !canonicalVersion(p.PredecessorSchemaVersion) || !canonicalVersion(p.CandidateSchemaVersion) || p.TargetIdentityDigest != target {
		return fmt.Errorf("%w: Goose projection", ErrOwnerConflict)
	}
	return nil
}

func validateRiverProjection(p transitionpreflight.RiverJobProjection, target string) error {
	if err := validateState(p.SchemaCompatibility); err != nil {
		return err
	}
	if err := validateState(p.JobHistoryCompatibility); err != nil {
		return err
	}
	if !canonicalVersion(p.ExistingSchemaVersion) || !canonicalVersion(p.RequiredSchemaVersion) || !canonicalVersion(p.ExistingJobHistoryVersion) || !canonicalVersion(p.RequiredJobHistoryVersion) || p.TargetIdentityDigest != target {
		return fmt.Errorf("%w: River projection", ErrOwnerConflict)
	}
	return nil
}

func validateDuckLakeProjection(p transitionpreflight.DuckLakeProjection, target string) error {
	if err := validateState(p.Compatibility); err != nil {
		return err
	}
	if p.Predecessor.Validate() != nil || p.Candidate.Validate() != nil || p.TargetIdentityDigest != target {
		return fmt.Errorf("%w: DuckLake projection", ErrOwnerConflict)
	}
	return nil
}

func validatePoolProjection(p PhysicalPoolProjection, target string) error {
	if err := validateState(p.Compatibility); err != nil {
		return err
	}
	if p.Predecessor.Validate() != nil || p.Candidate.Validate() != nil || p.TargetIdentityDigest != target {
		return fmt.Errorf("%w: physical-pool projection", ErrOwnerConflict)
	}
	return nil
}

func validateState(state transitionpreflight.CompatibilityState) error {
	switch state {
	case transitionpreflight.CompatibilityBackwardCompatible, transitionpreflight.CompatibilityIncompatible:
		return nil
	default:
		return fmt.Errorf("%w: ambiguous compatibility", ErrOwnerConflict)
	}
}

func canonicalVersion(value string) bool {
	return value != "" && len(value) <= transitionpreflight.MaxSchemaVersionBytes && strings.TrimSpace(value) == value && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func ownerError(owner string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", ErrOwnerEvidenceMissing, owner)
	}
	return fmt.Errorf("%w: %s: %w", ErrOwnerEvidenceMissing, owner, err)
}

func nilOwner(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
