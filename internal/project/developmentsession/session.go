// Package developmentsession contains the durable, owner-scoped pointer used
// by the local development loop. It records orchestration evidence only: the
// candidate and graph authorities remain responsible for the objects named by
// this record.
package developmentsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrInvalid       = errors.New("invalid development session")
	ErrNotFound      = errors.New("development session not found")
	ErrConflict      = errors.New("development session revision conflict")
	ErrOwnerMismatch = errors.New("development session owner mismatch")
)

const (
	maxOwnerBytes       = 256
	maxCheckoutBytes    = 256
	maxWorktreeBytes    = 256
	maxTargetBytes      = 160
	maxEnvironmentBytes = 160
	maxCandidateBytes   = 256
	maxDiagnosticBytes  = 4096
	maxDiagnostics      = 64
)

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]*$`)

// Key is the immutable serving scope of a development session. OwnerID is
// always supplied by authentication, never by a browser payload.
type Key struct {
	OwnerID     string                  `json:"ownerId"`
	CheckoutID  string                  `json:"checkoutId"`
	WorktreeID  string                  `json:"worktreeId"`
	ProjectID   projectgraph.ResourceID `json:"projectId"`
	TargetID    string                  `json:"targetId"`
	Environment string                  `json:"environment"`
}

func (k Key) Validate() error {
	k.OwnerID = strings.TrimSpace(k.OwnerID)
	k.CheckoutID = strings.TrimSpace(k.CheckoutID)
	k.WorktreeID = strings.TrimSpace(k.WorktreeID)
	k.TargetID = strings.TrimSpace(k.TargetID)
	k.Environment = strings.TrimSpace(k.Environment)
	if k.OwnerID == "" || len(k.OwnerID) > maxOwnerBytes || !tokenPattern.MatchString(k.OwnerID) {
		return fmt.Errorf("%w: owner identity is invalid", ErrInvalid)
	}
	if k.CheckoutID == "" || len(k.CheckoutID) > maxCheckoutBytes || !tokenPattern.MatchString(k.CheckoutID) {
		return fmt.Errorf("%w: checkout identity is invalid", ErrInvalid)
	}
	if k.WorktreeID == "" || len(k.WorktreeID) > maxWorktreeBytes || !tokenPattern.MatchString(k.WorktreeID) {
		return fmt.Errorf("%w: worktree identity is invalid", ErrInvalid)
	}
	if err := k.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity is invalid: %v", ErrInvalid, err)
	}
	if k.TargetID == "" || len(k.TargetID) > maxTargetBytes || !tokenPattern.MatchString(k.TargetID) {
		return fmt.Errorf("%w: target identity is invalid", ErrInvalid)
	}
	if k.Environment == "" || len(k.Environment) > maxEnvironmentBytes || !tokenPattern.MatchString(k.Environment) {
		return fmt.Errorf("%w: environment is invalid", ErrInvalid)
	}
	return nil
}

// ID is a stable opaque pointer. Delimiters are included in the hash input so
// scope components cannot collide through concatenation.
func (k Key) ID() string {
	input := strings.Join([]string{strings.TrimSpace(k.OwnerID), strings.TrimSpace(k.CheckoutID), strings.TrimSpace(k.WorktreeID), k.ProjectID.String(), strings.TrimSpace(k.TargetID), strings.TrimSpace(k.Environment)}, "\x00")
	sum := sha256.Sum256([]byte(input))
	return "devsess_" + hex.EncodeToString(sum[:])
}

// PreviewURL is the stable session pointer. The final candidate URL is
// resolved from this endpoint and is never synthesized from a candidate ID.
func (k Key) PreviewURL(origin string) string {
	if err := k.Validate(); err != nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(origin), "/") + "/api/v1/projects/" + url.PathEscape(strings.TrimSpace(k.ProjectID.String())) + "/targets/" + url.PathEscape(strings.TrimSpace(k.TargetID)) + "/development-session/candidate/preview"
}

type Identity struct {
	CandidateID    string `json:"candidateId"`
	ArtifactDigest string `json:"artifactDigest"`
	GraphDigest    string `json:"graphDigest"`
	PreviewURL     string `json:"previewUrl,omitempty"`
}

func (i Identity) normalized(requireCandidate bool) (Identity, error) {
	i.CandidateID = strings.TrimSpace(i.CandidateID)
	i.ArtifactDigest = strings.TrimSpace(i.ArtifactDigest)
	i.GraphDigest = strings.TrimSpace(i.GraphDigest)
	i.PreviewURL = strings.TrimSpace(i.PreviewURL)
	if i.CandidateID != "" && (len(i.CandidateID) > maxCandidateBytes || strings.IndexFunc(i.CandidateID, func(r rune) bool { return r <= ' ' || r == 127 }) >= 0) {
		return Identity{}, fmt.Errorf("%w: candidate identity is invalid", ErrInvalid)
	}
	if i.ArtifactDigest != "" {
		if err := digest.ValidateSHA256Identity(i.ArtifactDigest); err != nil {
			return Identity{}, fmt.Errorf("%w: artifact digest is invalid: %v", ErrInvalid, err)
		}
	}
	if i.GraphDigest != "" {
		if err := digest.ValidateSHA256Identity(i.GraphDigest); err != nil {
			return Identity{}, fmt.Errorf("%w: graph digest is invalid: %v", ErrInvalid, err)
		}
	}
	if i.CandidateID != "" && i.ArtifactDigest == "" {
		return Identity{}, fmt.Errorf("%w: candidate identity requires artifact digest", ErrInvalid)
	}
	if requireCandidate && i.CandidateID == "" && (i.ArtifactDigest != "" || i.GraphDigest != "") {
		return Identity{}, fmt.Errorf("%w: identity digests require candidate identity", ErrInvalid)
	}
	if i.PreviewURL != "" {
		parsed, err := url.Parse(i.PreviewURL)
		if err != nil || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Identity{}, fmt.Errorf("%w: candidate preview URL is invalid", ErrInvalid)
		}
	}
	if i.CandidateID == "" && i.PreviewURL != "" {
		return Identity{}, fmt.Errorf("%w: preview URL requires candidate identity", ErrInvalid)
	}
	return i, nil
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

func (d Diagnostic) normalized() (Diagnostic, error) {
	d.Code = strings.TrimSpace(d.Code)
	d.Message = RedactText(d.Message)
	d.Path = RedactText(strings.TrimSpace(d.Path))
	if d.Code == "" || len(d.Code) > 128 || !tokenPattern.MatchString(d.Code) {
		return Diagnostic{}, fmt.Errorf("%w: diagnostic code is invalid", ErrInvalid)
	}
	if d.Message == "" || len(d.Message) > maxDiagnosticBytes {
		return Diagnostic{}, fmt.Errorf("%w: diagnostic message is invalid", ErrInvalid)
	}
	if len(d.Path) > 1024 || strings.IndexByte(d.Path, 0) >= 0 {
		return Diagnostic{}, fmt.Errorf("%w: diagnostic path is invalid", ErrInvalid)
	}
	if d.Line < 0 || d.Column < 0 {
		return Diagnostic{}, fmt.Errorf("%w: diagnostic location is invalid", ErrInvalid)
	}
	return d, nil
}

// RedactText removes common credential-bearing forms before diagnostics are
// persisted or returned. It intentionally has no access to secret stores.
func RedactText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= 0x20 {
			return r
		}
		return ' '
	}, value)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`),
		regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key|credential)\s*[=:]\s*)[^\s,;]+`),
		regexp.MustCompile(`(?i)(://)([^/@\s]+)@`),
	} {
		value = re.ReplaceAllString(value, `${1}[REDACTED]`)
	}
	if len(value) > maxDiagnosticBytes {
		value = value[:maxDiagnosticBytes]
	}
	return value
}

type Record struct {
	ID          string       `json:"id"`
	Key         Key          `json:"scope"`
	Attempted   Identity     `json:"attempted"`
	LastValid   Identity     `json:"lastValid"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Revision    int64        `json:"revision"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

func (r Record) Normalize() (Record, error) {
	r.Key.OwnerID = strings.TrimSpace(r.Key.OwnerID)
	r.Key.CheckoutID = strings.TrimSpace(r.Key.CheckoutID)
	r.Key.WorktreeID = strings.TrimSpace(r.Key.WorktreeID)
	r.Key.TargetID = strings.TrimSpace(r.Key.TargetID)
	r.Key.Environment = strings.TrimSpace(r.Key.Environment)
	if err := r.Key.Validate(); err != nil {
		return Record{}, err
	}
	r.ID = strings.TrimSpace(r.ID)
	if r.ID == "" {
		r.ID = r.Key.ID()
	}
	if r.ID != r.Key.ID() {
		return Record{}, fmt.Errorf("%w: session key does not match scope", ErrInvalid)
	}
	var err error
	if r.Attempted, err = r.Attempted.normalized(false); err != nil {
		return Record{}, err
	}
	if r.LastValid, err = r.LastValid.normalized(true); err != nil {
		return Record{}, err
	}
	if len(r.Diagnostics) > maxDiagnostics {
		return Record{}, fmt.Errorf("%w: too many diagnostics", ErrInvalid)
	}
	for n := range r.Diagnostics {
		if r.Diagnostics[n], err = r.Diagnostics[n].normalized(); err != nil {
			return Record{}, err
		}
	}
	if r.Revision < 0 {
		return Record{}, fmt.Errorf("%w: revision is invalid", ErrInvalid)
	}
	return r, nil
}

// Store is the PostgreSQL-backed authority contract. expectedRevision=0
// creates a scope; every successful update increments the durable revision.
type Store interface {
	Resolve(context.Context, Key) (Record, error)
	Save(context.Context, Record, int64) (Record, error)
}

// MemoryStore is intentionally small and useful for unit tests and local
// compositions. Production wiring uses postgres.Repository.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	clock   func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record), clock: time.Now}
}

func (s *MemoryStore) Resolve(_ context.Context, key Key) (Record, error) {
	if s == nil {
		return Record{}, ErrNotFound
	}
	if err := key.Validate(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key.ID()]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (s *MemoryStore) Save(_ context.Context, input Record, expected int64) (Record, error) {
	if s == nil {
		return Record{}, ErrNotFound
	}
	record, err := input.Normalize()
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.records[record.ID]
	if !exists {
		if expected != 0 {
			return Record{}, ErrConflict
		}
		now := s.clock().UTC()
		record.Revision, record.CreatedAt, record.UpdatedAt = 1, now, now
	} else {
		if current.Revision != expected {
			return Record{}, ErrConflict
		}
		record.Revision, record.CreatedAt, record.UpdatedAt = current.Revision+1, current.CreatedAt, s.clock().UTC()
	}
	s.records[record.ID] = cloneRecord(record)
	return cloneRecord(record), nil
}

func cloneRecord(in Record) Record {
	in.Diagnostics = append([]Diagnostic(nil), in.Diagnostics...)
	return in
}
