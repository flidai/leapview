// Package search owns LeapView's credential-scoped global catalog search.
package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

const (
	defaultLimit = 25
	maxLimit     = 200
	cursorPrefix = "s1"
)

var (
	ErrInvalidCursor   = errors.New("invalid search cursor")
	ErrSnapshotChanged = errors.New("search snapshot changed")
)

type Type string

const (
	TypeWorkspace       Type = "workspace"
	TypeConnection      Type = "connection"
	TypeSource          Type = "source"
	TypeModelTable      Type = "model_table"
	TypeSemanticModel   Type = "semantic_model"
	TypeSemanticTable   Type = "semantic_table"
	TypeField           Type = "field"
	TypeMeasure         Type = "measure"
	TypeDashboard       Type = "dashboard"
	TypePage            Type = "page"
	TypeVisual          Type = "visual"
	TypeFilter          Type = "filter"
	TypeRefreshPipeline Type = "refresh_pipeline"
)

var validTypes = map[Type]struct{}{
	TypeWorkspace: {}, TypeConnection: {}, TypeSource: {}, TypeModelTable: {},
	TypeSemanticModel: {}, TypeSemanticTable: {}, TypeField: {}, TypeMeasure: {},
	TypeDashboard: {}, TypePage: {}, TypeVisual: {}, TypeFilter: {}, TypeRefreshPipeline: {},
}

type typeSearchTerm struct {
	typeValue Type
	aliases   [][]string
}

var typeSearchWordPattern = regexp.MustCompile(`[\p{L}\p{N}]+`)

// Type words are query operators when no explicit type filter was supplied, so
// "executive dashboar" searches dashboard titles for "executive" instead of
// requiring documents to repeat their own type in their metadata. Both
// singular and plural aliases participate in the same prefix semantics as FTS.
var typeSearchTerms = []typeSearchTerm{
	{TypeRefreshPipeline, typeSearchAliases("refresh pipeline", "refresh pipelines")},
	{TypeSemanticModel, typeSearchAliases("semantic model", "semantic models")},
	{TypeSemanticTable, typeSearchAliases("semantic table", "semantic tables")},
	{TypeModelTable, typeSearchAliases("model table", "model tables")},
	{TypeWorkspace, typeSearchAliases("workspace", "workspaces")},
	{TypeConnection, typeSearchAliases("connection", "connections")},
	{TypeSource, typeSearchAliases("source", "sources")},
	{TypeField, typeSearchAliases("field", "fields")},
	{TypeMeasure, typeSearchAliases("measure", "measures")},
	{TypeDashboard, typeSearchAliases("dashboard", "dashboards")},
	{TypePage, typeSearchAliases("page", "pages")},
	{TypeVisual, typeSearchAliases("visual", "visuals")},
	{TypeFilter, typeSearchAliases("filter", "filters")},
}

func typeSearchAliases(values ...string) [][]string {
	aliases := make([][]string, 0, len(values))
	for _, value := range values {
		aliases = append(aliases, strings.Fields(strings.ToLower(value)))
	}
	return aliases
}

type Reference struct {
	WorkspaceID string `json:"workspaceId"`
	Type        Type   `json:"type"`
	ID          string `json:"id"`
}

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Location struct {
	DashboardID   string `json:"dashboardId,omitempty"`
	DashboardName string `json:"dashboardName,omitempty"`
	PageID        string `json:"pageId,omitempty"`
	PageName      string `json:"pageName,omitempty"`
	Href          string `json:"href"`
}

type ContextTag string

const (
	ContextCurrentPage      ContextTag = "current_page"
	ContextCurrentDashboard ContextTag = "current_dashboard"
	ContextCurrentWorkspace ContextTag = "current_workspace"
)

type Result struct {
	Reference   Reference       `json:"reference"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	VisualType  string          `json:"visualType,omitempty"`
	Workspace   Workspace       `json:"workspace"`
	Hierarchy   []HierarchyItem `json:"-"`
	Href        string          `json:"href"`
	Locations   []Location      `json:"locations"`
	Context     []ContextTag    `json:"context"`
}

// HierarchyItem is an internal projection of a result's navigable ancestors.
// Public search transports remain free to expose their own stable result shape.
type HierarchyItem struct {
	Type Type
	ID   string
	Name string
}

type Page struct {
	Items      []Result `json:"items"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

type SearchContext struct {
	WorkspaceID string
	DashboardID string
	PageID      string
}

type Query struct {
	Text        string
	Environment string
	Workspaces  []string
	Types       []Type
	// AllowedTypes constrains product-owned search surfaces without changing the
	// meaning of a caller-supplied Types filter. It is intentionally not exposed
	// by the public API.
	AllowedTypes []Type
	Context      SearchContext
	Limit        int
	Cursor       string
}

// Subject is the authenticated search caller. Restricted workspace IDs are
// intersected with request filters before the repository sees the query.
type Subject struct {
	ID                   string
	CredentialID         string
	CredentialRestricted bool
	Privileges           []string
	DevBypass            bool
	Restricted           bool
	WorkspaceIDs         []string
}

type RepositoryQuery struct {
	Text         string
	Environment  string
	Workspaces   []string
	Types        []Type
	Context      SearchContext
	NoWorkspaces bool
	NoTypes      bool
}

type Candidate struct {
	Result          Result
	Object          access.ObjectRef
	LocationObjects []access.ObjectRef
	RequireLocation bool
}

type Repository interface {
	Snapshot(context.Context, RepositoryQuery) (string, error)
	Candidates(context.Context, RepositoryQuery, int, int) ([]Candidate, bool, error)
	Resolve(context.Context, string, []Reference) ([]Candidate, error)
}

type Authorizer interface {
	CanView(context.Context, Subject, access.ObjectRef) (bool, error)
}

type CursorSigner interface {
	Sign(prefix string, payload []byte) string
	Verify(prefix, token string) ([]byte, error)
}

type Service struct {
	repository Repository
	authorizer Authorizer
	cursors    CursorSigner
}

func NewService(repository Repository, authorizer Authorizer, cursors CursorSigner) *Service {
	return &Service{repository: repository, authorizer: authorizer, cursors: cursors}
}

func (s *Service) Search(ctx context.Context, subject Subject, query Query) (Page, error) {
	if s == nil || s.repository == nil || s.authorizer == nil || s.cursors == nil {
		return Page{}, errors.New("search service is not configured")
	}
	normalized, err := normalizeQuery(subject, query)
	if err != nil {
		return Page{}, err
	}
	snapshot, err := s.repository.Snapshot(ctx, normalized)
	if err != nil {
		return Page{}, err
	}
	offset := 0
	if strings.TrimSpace(query.Cursor) != "" {
		offset, err = s.decodeCursor(query.Cursor, subject, normalized, snapshot)
		if err != nil {
			return Page{}, err
		}
	}
	limit := query.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		return Page{}, fmt.Errorf("search limit must not exceed %d", maxLimit)
	}
	items := make([]Result, 0, limit+1)
	nextOffset := 0
	chunk := limit * 4
	if chunk < 32 {
		chunk = 32
	}
searchLoop:
	for {
		rows, repositoryMore, err := s.repository.Candidates(ctx, normalized, offset, chunk)
		if err != nil {
			return Page{}, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			candidateOffset := offset
			offset++
			result, allowed, err := s.authorizedResult(ctx, subject, row, normalized.Context)
			if err != nil {
				return Page{}, err
			}
			if allowed {
				items = append(items, result)
				if len(items) == limit+1 {
					nextOffset = candidateOffset
					break searchLoop
				}
			}
		}
		if !repositoryMore {
			break
		}
	}
	page := Page{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = s.encodeCursor(nextOffset, subject, normalized, snapshot)
	}
	return page, nil
}

func (s *Service) Resolve(ctx context.Context, subject Subject, environment string, references []Reference) ([]Result, error) {
	if s == nil || s.repository == nil || s.authorizer == nil {
		return nil, errors.New("search service is not configured")
	}
	if len(references) == 0 {
		return []Result{}, nil
	}
	normalized := make([]Reference, 0, len(references))
	seen := map[Reference]struct{}{}
	for _, reference := range references {
		reference.WorkspaceID = strings.TrimSpace(reference.WorkspaceID)
		reference.ID = strings.TrimSpace(reference.ID)
		reference.Type = Type(strings.ToLower(strings.TrimSpace(string(reference.Type))))
		if reference.WorkspaceID == "" || reference.ID == "" {
			continue
		}
		if _, ok := validTypes[reference.Type]; !ok || !subjectAllowsWorkspace(subject, reference.WorkspaceID) {
			continue
		}
		if _, duplicate := seen[reference]; duplicate {
			continue
		}
		seen[reference] = struct{}{}
		normalized = append(normalized, reference)
	}
	candidates, err := s.repository.Resolve(ctx, strings.TrimSpace(environment), normalized)
	if err != nil {
		return nil, err
	}
	byReference := make(map[Reference]Candidate, len(candidates))
	for _, candidate := range candidates {
		byReference[candidate.Result.Reference] = candidate
	}
	results := make([]Result, 0, len(candidates))
	for _, reference := range normalized {
		candidate, ok := byReference[reference]
		if !ok {
			continue
		}
		result, allowed, err := s.authorizedResult(ctx, subject, candidate, SearchContext{})
		if err != nil {
			return nil, err
		}
		if allowed {
			results = append(results, result)
		}
	}
	return results, nil
}

func (s *Service) authorizedResult(ctx context.Context, subject Subject, candidate Candidate, searchContext SearchContext) (Result, bool, error) {
	allowed, err := s.authorizer.CanView(ctx, subject, candidate.Object)
	if err != nil || !allowed {
		return Result{}, false, err
	}
	result := candidate.Result
	result.Locations = append([]Location(nil), candidate.Result.Locations...)
	if len(candidate.LocationObjects) == 0 {
		return result, !candidate.RequireLocation, nil
	}
	locations := make([]Location, 0, len(result.Locations))
	for index, location := range result.Locations {
		if index >= len(candidate.LocationObjects) {
			break
		}
		locationAllowed, err := s.authorizer.CanView(ctx, subject, candidate.LocationObjects[index])
		if err != nil {
			return Result{}, false, err
		}
		if locationAllowed {
			locations = append(locations, location)
		}
	}
	result.Locations = locations
	result.Context = resultContext(result.Reference.WorkspaceID, locations, searchContext)
	if candidate.RequireLocation && len(locations) == 0 {
		return Result{}, false, nil
	}
	if len(locations) > 0 {
		result.Href = locations[0].Href
	}
	return result, true, nil
}

func resultContext(workspaceID string, locations []Location, searchContext SearchContext) []ContextTag {
	if searchContext.WorkspaceID == "" || workspaceID != searchContext.WorkspaceID {
		return []ContextTag{}
	}
	for _, location := range locations {
		if searchContext.PageID != "" && location.DashboardID == searchContext.DashboardID && location.PageID == searchContext.PageID {
			return []ContextTag{ContextCurrentPage, ContextCurrentDashboard, ContextCurrentWorkspace}
		}
	}
	for _, location := range locations {
		if searchContext.DashboardID != "" && location.DashboardID == searchContext.DashboardID {
			return []ContextTag{ContextCurrentDashboard, ContextCurrentWorkspace}
		}
	}
	return []ContextTag{ContextCurrentWorkspace}
}

func normalizeQuery(subject Subject, query Query) (RepositoryQuery, error) {
	workspaces := normalizedStrings(query.Workspaces)
	if subject.Restricted {
		allowed := make(map[string]struct{}, len(subject.WorkspaceIDs))
		for _, workspaceID := range normalizedStrings(subject.WorkspaceIDs) {
			allowed[workspaceID] = struct{}{}
		}
		if len(workspaces) == 0 {
			workspaces = normalizedStrings(subject.WorkspaceIDs)
		} else {
			filtered := workspaces[:0]
			for _, workspaceID := range workspaces {
				if _, ok := allowed[workspaceID]; ok {
					filtered = append(filtered, workspaceID)
				}
			}
			workspaces = filtered
		}
	}
	normalizedTypes, err := normalizeTypes(query.Types)
	if err != nil {
		return RepositoryQuery{}, err
	}
	allowedTypes, err := normalizeTypes(query.AllowedTypes)
	if err != nil {
		return RepositoryQuery{}, err
	}
	text := strings.TrimSpace(query.Text)
	typeOperator := false
	if len(normalizedTypes) == 0 {
		text, normalizedTypes = implicitTypeFilters(text)
		typeOperator = len(normalizedTypes) > 0
	} else if filteredText, inferredTypes := implicitTypeFilters(text); len(inferredTypes) > 0 && catalogTypesInclude(normalizedTypes, inferredTypes) {
		text = filteredText
	}
	noTypes := false
	if len(allowedTypes) > 0 {
		if len(normalizedTypes) == 0 && !typeOperator {
			normalizedTypes = allowedTypes
		} else {
			normalizedTypes = intersectTypes(normalizedTypes, allowedTypes)
			noTypes = len(normalizedTypes) == 0
		}
	}
	context := query.Context
	context.WorkspaceID = strings.TrimSpace(context.WorkspaceID)
	context.DashboardID = strings.TrimSpace(context.DashboardID)
	context.PageID = strings.TrimSpace(context.PageID)
	return RepositoryQuery{
		Text: text, Environment: strings.TrimSpace(query.Environment),
		Workspaces: workspaces, Types: normalizedTypes, Context: context,
		NoWorkspaces: subject.Restricted && len(workspaces) == 0,
		NoTypes:      noTypes,
	}, nil
}

func normalizeTypes(values []Type) ([]Type, error) {
	seen := map[Type]struct{}{}
	out := make([]Type, 0, len(values))
	for _, typ := range values {
		typ = Type(strings.ToLower(strings.TrimSpace(string(typ))))
		if _, ok := validTypes[typ]; !ok {
			return nil, fmt.Errorf("unknown search type %q", typ)
		}
		if _, duplicate := seen[typ]; duplicate {
			continue
		}
		seen[typ] = struct{}{}
		out = append(out, typ)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func intersectTypes(left, right []Type) []Type {
	allowed := make(map[Type]struct{}, len(right))
	for _, typ := range right {
		allowed[typ] = struct{}{}
	}
	out := make([]Type, 0, len(left))
	for _, typ := range left {
		if _, ok := allowed[typ]; ok {
			out = append(out, typ)
		}
	}
	return out
}

func catalogTypesInclude(types, wanted []Type) bool {
	available := make(map[Type]struct{}, len(types))
	for _, typ := range types {
		available[typ] = struct{}{}
	}
	for _, typ := range wanted {
		if _, ok := available[typ]; !ok {
			return false
		}
	}
	return true
}

func implicitTypeFilters(text string) (string, []Type) {
	type word struct {
		start int
		end   int
		value string
	}
	type candidate struct {
		typeValue Type
		alias     []string
	}

	indices := typeSearchWordPattern.FindAllStringIndex(text, -1)
	words := make([]word, 0, len(indices))
	for _, index := range indices {
		words = append(words, word{start: index[0], end: index[1], value: strings.ToLower(text[index[0]:index[1]])})
	}
	removed := make([]bool, len(words))
	seen := map[Type]struct{}{}
	types := make([]Type, 0)
	for index := 0; index < len(words); {
		candidates := make([]candidate, 0)
		for _, term := range typeSearchTerms {
			for _, alias := range term.aliases {
				if len(alias) > 0 && strings.HasPrefix(alias[0], words[index].value) {
					candidates = append(candidates, candidate{typeValue: term.typeValue, alias: alias})
				}
			}
		}
		if len(candidates) == 0 {
			index++
			continue
		}

		consumed := 1
		for index+consumed < len(words) {
			narrowed := make([]candidate, 0, len(candidates))
			for _, current := range candidates {
				if len(current.alias) > consumed && strings.HasPrefix(current.alias[consumed], words[index+consumed].value) {
					narrowed = append(narrowed, current)
				}
			}
			if len(narrowed) == 0 {
				break
			}
			candidates = narrowed
			consumed++
		}
		for _, current := range candidates {
			if _, duplicate := seen[current.typeValue]; duplicate {
				continue
			}
			seen[current.typeValue] = struct{}{}
			types = append(types, current.typeValue)
		}
		for consumedIndex := 0; consumedIndex < consumed; consumedIndex++ {
			removed[index+consumedIndex] = true
		}
		index += consumed
	}

	remaining := []byte(text)
	for index, remove := range removed {
		if !remove {
			continue
		}
		for byteIndex := words[index].start; byteIndex < words[index].end; byteIndex++ {
			remaining[byteIndex] = ' '
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return strings.Join(strings.Fields(string(remaining)), " "), types
}

func normalizedStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func subjectAllowsWorkspace(subject Subject, workspaceID string) bool {
	if !subject.Restricted {
		return true
	}
	for _, allowed := range subject.WorkspaceIDs {
		if strings.TrimSpace(allowed) == workspaceID {
			return true
		}
	}
	return false
}

type cursor struct {
	Offset   int    `json:"offset"`
	Subject  string `json:"subject"`
	Query    string `json:"query"`
	Snapshot string `json:"snapshot"`
}

func (s *Service) encodeCursor(offset int, subject Subject, query RepositoryQuery, snapshot string) string {
	payload, _ := json.Marshal(cursor{Offset: offset, Subject: subjectDigest(subject), Query: queryDigest(query), Snapshot: snapshot})
	return s.cursors.Sign(cursorPrefix, payload)
}

func (s *Service) decodeCursor(token string, subject Subject, query RepositoryQuery, snapshot string) (int, error) {
	payload, err := s.cursors.Verify(cursorPrefix, token)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	var value cursor
	if json.Unmarshal(payload, &value) != nil || value.Offset < 0 || value.Subject != subjectDigest(subject) || value.Query != queryDigest(query) {
		return 0, ErrInvalidCursor
	}
	if value.Snapshot != snapshot {
		return 0, ErrSnapshotChanged
	}
	return value.Offset, nil
}

func subjectDigest(subject Subject) string {
	values := normalizedStrings(subject.WorkspaceIDs)
	raw, _ := json.Marshal(struct {
		ID                   string
		CredentialID         string
		CredentialRestricted bool
		Privileges           []string
		DevBypass            bool
		Restricted           bool
		Workspaces           []string
	}{strings.TrimSpace(subject.ID), strings.TrimSpace(subject.CredentialID), subject.CredentialRestricted, normalizedStrings(subject.Privileges), subject.DevBypass, subject.Restricted, values})
	return digest(raw)
}

func queryDigest(query RepositoryQuery) string {
	raw, _ := json.Marshal(query)
	return digest(raw)
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
