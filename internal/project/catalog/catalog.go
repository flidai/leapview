// Package catalog exposes the project-wide resource catalog used by agent and
// browser consumers. The catalog is deliberately backed by the immutable
// graph carried by the active serving lease: a caller cannot select a project,
// domain, or generation and cannot resolve an ID outside that lease.
package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrUnavailable     = errors.New("project catalog unavailable")
	ErrNotFound        = errors.New("catalog resource not found")
	ErrInvalidRequest  = errors.New("invalid catalog request")
	ErrInvalidCursor   = errors.New("invalid catalog cursor")
	ErrSnapshotChanged = errors.New("catalog snapshot changed")
)

const (
	DefaultLimit    = 25
	MaxLimit        = 200
	MaxQueryLength  = 200
	MaxCursorLength = 4096
)

// Lease is the minimal runtime-host port needed by catalog.  Keeping this
// interface local avoids coupling catalog to app composition helpers while
// still requiring the exact active generation snapshot for every request.
type Lease interface {
	Release()
	Identity() projectgraph.ServingIdentity
	AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
}

type LeaseProvider interface {
	Acquire(context.Context) (Lease, error)
}

// SubjectResolver expands the authenticated principal into the principal plus
// all group subjects.  Implementations must fail closed when group lookup is
// unavailable; principal-only fallback would change authorization semantics.
type SubjectResolver interface {
	AuthorizationSubjects(context.Context, string) ([]access.SubjectRef, error)
}

// Ref is an exact graph identity. Kind is descriptive validation metadata;
// resource ID remains the stable identity and does not include domain/path.
type Ref struct {
	ID   projectgraph.ResourceID `json:"id"`
	Kind projectgraph.Kind       `json:"kind"`
}

func (r Ref) valid() bool {
	if _, err := projectgraph.NewResourceID(r.ID.String()); err != nil {
		return false
	}
	return r.Kind.Valid()
}

type Result struct {
	Ref         Ref                     `json:"ref"`
	Name        string                  `json:"name"`
	DisplayName string                  `json:"displayName,omitempty"`
	Description string                  `json:"description,omitempty"`
	Domain      string                  `json:"domain,omitempty"`
	Owner       string                  `json:"owner,omitempty"`
	Tags        []string                `json:"tags,omitempty"`
	Provenance  projectgraph.Provenance `json:"provenance,omitempty"`
}

type SearchRequest struct {
	PrincipalID string
	// DevAuthBypass admits the local development principal to the exact
	// active-generation graph without consulting grants. The lease, graph,
	// snapshot identity, and cursor checks still apply.
	DevAuthBypass bool
	Query         string
	Kinds         []projectgraph.Kind
	Domain        string
	Limit         int
	Cursor        string
}

type Page struct {
	Items      []Result `json:"items"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

type ListRequest struct {
	PrincipalID string
	// DevAuthBypass admits the local development principal to the exact
	// active-generation graph without consulting grants. The lease, graph,
	// snapshot identity, and cursor checks still apply.
	DevAuthBypass bool
	Parent        *Ref
	Kinds         []projectgraph.Kind
	Domain        string
	Limit         int
	Cursor        string
}

// Service is an authorization-filtered catalog/search port. It does not keep
// a mutable graph cache: every operation leases the active generation and
// reads its bound graph and immutable AuthorizationSnapshot.
type Service struct {
	leases   LeaseProvider
	subjects SubjectResolver
}

func NewService(leasing LeaseProvider, subjects SubjectResolver) (*Service, error) {
	if leasing == nil || subjects == nil {
		return nil, ErrUnavailable
	}
	return &Service{leases: leasing, subjects: subjects}, nil
}

func (s *Service) Search(ctx context.Context, request SearchRequest) (Page, error) {
	if s == nil || s.leases == nil || s.subjects == nil {
		return Page{}, ErrUnavailable
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		return Page{}, fmt.Errorf("%w: query is required", ErrInvalidRequest)
	}
	if len(request.Query) > MaxQueryLength {
		return Page{}, fmt.Errorf("%w: query must not exceed %d characters", ErrInvalidRequest, MaxQueryLength)
	}
	lease, snapshot, graph, subjects, err := s.authorized(ctx, request.PrincipalID, request.DevAuthBypass)
	if err != nil {
		return Page{}, err
	}
	defer lease.Release()
	kinds, err := normalizeKinds(request.Kinds)
	if err != nil {
		return Page{}, err
	}
	limit, err := normalizeLimit(request.Limit)
	if err != nil {
		return Page{}, err
	}
	domain := strings.ToLower(strings.TrimSpace(request.Domain))
	items := make([]Result, 0)
	for _, resource := range graph.Resources() {
		if len(kinds) != 0 && !containsKind(kinds, resource.Kind) {
			continue
		}
		if domain != "" && strings.ToLower(resource.Metadata.Domain) != domain {
			continue
		}
		if !matches(resource, request.Query) {
			continue
		}
		allowed := request.DevAuthBypass
		if !request.DevAuthBypass {
			allowed, err = allowsAny(snapshot, subjects, resource)
			if err != nil {
				return Page{}, err
			}
		}
		if allowed {
			items = append(items, resultFor(resource))
		}
	}
	sortResults(items)
	digest, err := snapshotDigest(snapshot)
	if err != nil {
		return Page{}, err
	}
	return paginate(items, request.Cursor, cursorInput{Snapshot: digest, Query: request.Query, Kinds: kinds, Domain: domain, Limit: limit})
}

func (s *Service) List(ctx context.Context, request ListRequest) (Page, error) {
	if s == nil || s.leases == nil || s.subjects == nil {
		return Page{}, ErrUnavailable
	}
	lease, snapshot, graph, subjects, err := s.authorized(ctx, request.PrincipalID, request.DevAuthBypass)
	if err != nil {
		return Page{}, err
	}
	defer lease.Release()
	kinds, err := normalizeKinds(request.Kinds)
	if err != nil {
		return Page{}, err
	}
	limit, err := normalizeLimit(request.Limit)
	if err != nil {
		return Page{}, err
	}
	domain := strings.ToLower(strings.TrimSpace(request.Domain))
	var parent projectgraph.ResourceID
	if request.Parent != nil {
		if !request.Parent.valid() {
			return Page{}, fmt.Errorf("%w: parent ref is invalid", ErrInvalidRequest)
		}
		resource, ok := graph.Resource(request.Parent.ID)
		if !ok || resource.Kind != request.Parent.Kind {
			return Page{}, ErrNotFound
		}
		allowed := request.DevAuthBypass
		if !request.DevAuthBypass {
			allowed, err = allowsAny(snapshot, subjects, resource)
			if err != nil {
				return Page{}, err
			}
		}
		if !allowed {
			return Page{}, ErrNotFound
		}
		parent = request.Parent.ID
	}
	items := make([]Result, 0)
	seen := map[projectgraph.ResourceID]struct{}{}
	if parent == "" {
		// Project graphs do not require edges from the root. Root browsing is a
		// project-wide catalog operation and therefore scans every graph node,
		// filtering each one against the exact generation snapshot.
		for _, resource := range graph.Resources() {
			if len(kinds) != 0 && !containsKind(kinds, resource.Kind) {
				continue
			}
			if domain != "" && strings.ToLower(resource.Metadata.Domain) != domain {
				continue
			}
			allowed := request.DevAuthBypass
			if !request.DevAuthBypass {
				allowed, err = allowsAny(snapshot, subjects, resource)
				if err != nil {
					return Page{}, err
				}
			}
			if allowed {
				seen[resource.ID] = struct{}{}
				items = append(items, resultFor(resource))
			}
		}
	} else {
		// Child browsing follows authored graph edges in the dependency
		// direction (From -> To). Shared dependencies are returned once.
		for _, edge := range graph.Edges() {
			if edge.From != parent {
				continue
			}
			resource, ok := graph.Resource(edge.To)
			if !ok || (len(kinds) != 0 && !containsKind(kinds, resource.Kind)) {
				continue
			}
			if domain != "" && strings.ToLower(resource.Metadata.Domain) != domain {
				continue
			}
			if _, ok := seen[resource.ID]; ok {
				continue
			}
			allowed := request.DevAuthBypass
			if !request.DevAuthBypass {
				allowed, err = allowsAny(snapshot, subjects, resource)
				if err != nil {
					return Page{}, err
				}
			}
			if allowed {
				seen[resource.ID] = struct{}{}
				items = append(items, resultFor(resource))
			}
		}
	}
	sortResults(items)
	parentKey := ""
	if request.Parent != nil {
		parentKey = string(request.Parent.Kind) + ":" + request.Parent.ID.String()
	}
	digest, err := snapshotDigest(snapshot)
	if err != nil {
		return Page{}, err
	}
	return paginate(items, request.Cursor, cursorInput{Snapshot: digest, Kinds: kinds, Domain: domain, Limit: limit, Parent: parentKey})
}

// Resolve validates the exact ID/kind against the leased graph and then checks
// the required capability. Every failure is deliberately ErrNotFound so an
// unauthorized ID cannot be distinguished from an unknown ID by callers.
func (s *Service) Resolve(ctx context.Context, principalID string, ref Ref, capability access.Capability, devAuthBypass bool) (Result, error) {
	if s == nil || s.leases == nil || s.subjects == nil {
		return Result{}, ErrUnavailable
	}
	if !ref.valid() {
		return Result{}, ErrNotFound
	}
	lease, snapshot, graph, subjects, err := s.authorized(ctx, principalID, devAuthBypass)
	if err != nil {
		return Result{}, err
	}
	defer lease.Release()
	resource, ok := graph.Resource(ref.ID)
	if !ok || resource.Kind != ref.Kind {
		return Result{}, ErrNotFound
	}
	if err := access.ValidateCapabilityForKind(resource.Kind, capability); err != nil {
		return Result{}, ErrNotFound
	}
	if devAuthBypass {
		return resultFor(resource), nil
	}
	for _, subject := range subjects {
		allowed, err := snapshot.Allows(subject, mustResourceRef(ref), capability)
		if err != nil {
			return Result{}, err
		}
		if allowed {
			return resultFor(resource), nil
		}
	}
	return Result{}, ErrNotFound
}

func (s *Service) authorized(ctx context.Context, principalID string, devAuthBypass bool) (Lease, accesssnapshot.AuthorizationSnapshot, projectgraph.ProjectGraph, []access.SubjectRef, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return nil, accesssnapshot.AuthorizationSnapshot{}, projectgraph.ProjectGraph{}, nil, ErrNotFound
	}
	lease, err := s.leases.Acquire(ctx)
	if err != nil || lease == nil {
		if err != nil {
			return nil, accesssnapshot.AuthorizationSnapshot{}, projectgraph.ProjectGraph{}, nil, err
		}
		return nil, accesssnapshot.AuthorizationSnapshot{}, projectgraph.ProjectGraph{}, nil, ErrUnavailable
	}
	snapshot := lease.AuthorizationSnapshot()
	if err := snapshot.ValidateBound(); err != nil {
		lease.Release()
		return nil, accesssnapshot.AuthorizationSnapshot{}, projectgraph.ProjectGraph{}, nil, err
	}
	if lease.Identity() != snapshot.Identity() {
		lease.Release()
		return nil, accesssnapshot.AuthorizationSnapshot{}, projectgraph.ProjectGraph{}, nil, ErrSnapshotChanged
	}
	if devAuthBypass {
		return lease, snapshot, snapshot.Project(), nil, nil
	}
	subjects, err := s.subjects.AuthorizationSubjects(ctx, principalID)
	if err != nil {
		lease.Release()
		return nil, accesssnapshot.AuthorizationSnapshot{}, projectgraph.ProjectGraph{}, nil, err
	}
	if len(subjects) == 0 {
		lease.Release()
		return nil, accesssnapshot.AuthorizationSnapshot{}, projectgraph.ProjectGraph{}, nil, ErrNotFound
	}
	for _, subject := range subjects {
		if err := subject.Validate(); err != nil {
			lease.Release()
			return nil, accesssnapshot.AuthorizationSnapshot{}, projectgraph.ProjectGraph{}, nil, err
		}
	}
	return lease, snapshot, snapshot.Project(), subjects, nil
}

func mustResourceRef(ref Ref) access.ResourceRef {
	value, _ := access.NewResourceRef(ref.ID, ref.Kind)
	return value
}

func allowsAny(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, resource projectgraph.Resource) (bool, error) {
	ref, err := access.NewResourceRef(resource.ID, resource.Kind)
	if err != nil {
		return false, err
	}
	capability := access.CapabilityResourceRead
	if resource.Kind == projectgraph.KindProject {
		capability = access.CapabilityProjectAdmin
	}
	for _, subject := range subjects {
		allowed, err := snapshot.Allows(subject, ref, capability)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

func resultFor(resource projectgraph.Resource) Result {
	return Result{Ref: Ref{ID: resource.ID, Kind: resource.Kind}, Name: resource.Name, DisplayName: resource.Metadata.DisplayName, Description: resource.Metadata.Description, Domain: resource.Metadata.Domain, Owner: resource.Metadata.Owner, Tags: append([]string(nil), resource.Metadata.Tags...), Provenance: resource.Provenance}
}

func matches(resource projectgraph.Resource, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	words := strings.Fields(query)
	values := []string{resource.ID.String(), string(resource.Kind), resource.Name, resource.Metadata.DisplayName, resource.Metadata.Description, resource.Metadata.Domain, resource.Metadata.Owner, strings.Join(resource.Metadata.Tags, " ")}
	joined := strings.ToLower(strings.Join(values, " "))
	for _, word := range words {
		if !strings.Contains(joined, word) {
			return false
		}
	}
	return true
}

func sortResults(items []Result) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		if left != right {
			return left < right
		}
		if items[i].Ref.Kind != items[j].Ref.Kind {
			return items[i].Ref.Kind < items[j].Ref.Kind
		}
		return items[i].Ref.ID < items[j].Ref.ID
	})
}

func normalizeKinds(kinds []projectgraph.Kind) ([]projectgraph.Kind, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	seen := map[projectgraph.Kind]struct{}{}
	out := make([]projectgraph.Kind, 0, len(kinds))
	for _, kind := range kinds {
		if !kind.Valid() {
			return nil, fmt.Errorf("%w: invalid kind %q", ErrInvalidRequest, kind)
		}
		if _, ok := seen[kind]; !ok {
			seen[kind] = struct{}{}
			out = append(out, kind)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func normalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultLimit, nil
	}
	if limit < 0 {
		return 0, fmt.Errorf("%w: limit must be positive", ErrInvalidRequest)
	}
	if limit > MaxLimit {
		return 0, fmt.Errorf("%w: limit must not exceed %d", ErrInvalidRequest, MaxLimit)
	}
	return limit, nil
}

func containsKind(kinds []projectgraph.Kind, wanted projectgraph.Kind) bool {
	for _, kind := range kinds {
		if kind == wanted {
			return true
		}
	}
	return false
}

type cursorInput struct {
	Snapshot string
	Query    string
	Kinds    []projectgraph.Kind
	Domain   string
	Limit    int
	Parent   string
}
type cursorWire struct {
	Snapshot string              `json:"snapshot"`
	Query    string              `json:"query,omitempty"`
	Kinds    []projectgraph.Kind `json:"kinds,omitempty"`
	Domain   string              `json:"domain,omitempty"`
	Limit    int                 `json:"limit"`
	Parent   string              `json:"parent,omitempty"`
	Offset   int                 `json:"offset"`
}

func paginate(items []Result, cursor string, input cursorInput) (Page, error) {
	offset := 0
	if strings.TrimSpace(cursor) != "" {
		if len(cursor) > MaxCursorLength {
			return Page{}, ErrInvalidCursor
		}
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return Page{}, ErrInvalidCursor
		}
		var wire cursorWire
		if err := json.Unmarshal(decoded, &wire); err != nil || wire.Offset < 0 {
			return Page{}, ErrInvalidCursor
		}
		if wire.Snapshot != input.Snapshot {
			return Page{}, ErrSnapshotChanged
		}
		if wire.Query != input.Query || wire.Domain != input.Domain || wire.Limit != input.Limit || wire.Parent != input.Parent || !sameKinds(wire.Kinds, input.Kinds) {
			return Page{}, ErrInvalidCursor
		}
		offset = wire.Offset
	}
	if offset > len(items) {
		return Page{}, ErrInvalidCursor
	}
	end := offset + input.Limit
	if end > len(items) {
		end = len(items)
	}
	page := Page{Items: append([]Result(nil), items[offset:end]...)}
	if end < len(items) {
		encoded, err := json.Marshal(cursorWire{Snapshot: input.Snapshot, Query: input.Query, Kinds: input.Kinds, Domain: input.Domain, Limit: input.Limit, Parent: input.Parent, Offset: end})
		if err != nil {
			return Page{}, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page, nil
}

func sameKinds(left, right []projectgraph.Kind) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func snapshotDigest(snapshot accesssnapshot.AuthorizationSnapshot) (string, error) {
	digest, err := snapshot.Digest()
	if err != nil || digest == "" {
		if err == nil {
			err = errors.New("authorization snapshot digest is empty")
		}
		return "", fmt.Errorf("%w: %v", ErrSnapshotChanged, err)
	}
	return digest, nil
}
