package http

import (
	"errors"
	stdhttp "net/http"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

// Definition mutations must use this boundary so the expected version is
// checked in the same transaction as the write. Falling back to a read-then-
// write adapter would make If-Match vulnerable to a race.
func (h Handler) ListSemanticAttributeDefinitions(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("ownerKind"))
	if owner != "" && !access.SemanticAttributeOwnerKind(owner).Valid() {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(errors.New("ownerKind is invalid")))
		return
	}
	limit, err := parseAPILimitQuery(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	afterName, afterDefinitionID, err := semanticAttributePageCursor(r.URL.Query().Get("pageToken"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, ok := repo.(access.SemanticAttributeRegistry)
	if !ok {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	rows, err := registry.SearchSemanticAttributes(r.Context(), access.SemanticAttributeSearch{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), OwnerKind: access.SemanticAttributeOwnerKind(owner),
		AfterName: afterName, AfterDefinitionID: afterDefinitionID, Limit: limit + 1,
	})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	nextCursor := ""
	if len(rows) > limit {
		last := rows[limit-1]
		nextCursor = encodeKeyCursor(last.Name + "\x00" + last.ID)
		rows = rows[:limit]
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, semanticAttributeDefinitionDTO(row))
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"items": items, "page": map[string]any{"nextCursor": nextCursor}})
}

func semanticAttributePageCursor(token string) (string, string, error) {
	if strings.TrimSpace(token) == "" {
		return "", "", nil
	}
	cursor, err := decodeKeyCursor(token)
	if err != nil {
		return "", "", err
	}
	name, definitionID, ok := strings.Cut(cursor, "\x00")
	if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(definitionID) == "" {
		return "", "", errors.New("pageToken is invalid")
	}
	return name, definitionID, nil
}

func (h Handler) GetSemanticAttributeDefinition(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, ok := repo.(access.SemanticAttributeRegistry)
	if !ok {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	row, err := registry.SemanticAttributeDefinition(r.Context(), chi.URLParam(r, "attribute"))
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	w.Header().Set("ETag", semanticAttributeDefinitionETag(row))
	writeJSON(w, stdhttp.StatusOK, semanticAttributeDefinitionDTO(row))
}

func (h Handler) RegisterSemanticAttribute(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input semanticAttributeRegisterWire
	if err := decodeStrictJSON(r, &input); err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	typeName, err := semanticAttributeType(input.Type)
	if err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	shape, err := semanticAttributeShape(input.Shape)
	if err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	metadata, err := semanticAttributeMetadata(input.Metadata)
	if err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, ok := repo.(access.SemanticAttributeRegistry)
	if !ok {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	var row access.SemanticAttributeDefinition
	err = executeSemanticAttributeMutation(r, repo, accessgen.GenCommandOperationRegisterSemanticAttribute(), func() error {
		var callErr error
		row, callErr = registry.RegisterSemanticAttribute(r.Context(), access.RegisterSemanticAttributeInput{Name: input.Name, Type: typeName, Shape: shape, Metadata: metadata, Mutation: h.semanticAttributeMutationContext(r)})
		return callErr
	})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/semantic-attributes/"+url.PathEscape(row.Name))
	w.Header().Set("ETag", semanticAttributeDefinitionETag(row))
	writeJSON(w, stdhttp.StatusCreated, semanticAttributeDefinitionDTO(row))
}

func (h Handler) UpdateSemanticAttributeMetadata(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input semanticAttributeMetadataWire
	if err := decodeStrictJSON(r, &input); err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	metadata, err := semanticAttributeMetadata(input)
	if err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, ok := repo.(access.SemanticAttributeRegistry)
	if !ok {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	versioned, versionedOK := repo.(access.VersionedSemanticAttributeRegistry)
	if !versionedOK {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	name := chi.URLParam(r, "attribute")
	current, err := registry.SemanticAttributeDefinition(r.Context(), name)
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	if err := checkIfMatch(r.Header.Get("If-Match"), semanticAttributeDefinitionETag(current)); err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	var row access.SemanticAttributeDefinition
	err = executeSemanticAttributeMutation(r, repo, accessgen.GenCommandOperationUpdateSemanticAttributeMetadata(), func() error {
		var callErr error
		input := access.UpdateSemanticAttributeMetadataInput{Name: name, Metadata: metadata, ExpectedVersion: current.DefinitionVersion, Mutation: h.semanticAttributeMutationContext(r)}
		row, callErr = versioned.UpdateSemanticAttributeMetadataExpected(r.Context(), input)
		return callErr
	})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	w.Header().Set("ETag", semanticAttributeDefinitionETag(row))
	writeJSON(w, stdhttp.StatusOK, semanticAttributeDefinitionDTO(row))
}

func (h Handler) DisableSemanticAttribute(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.setSemanticAttributeEnabled(w, r, false, accessgen.GenCommandOperationDisableSemanticAttribute())
}
func (h Handler) RestoreSemanticAttribute(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.setSemanticAttributeEnabled(w, r, true, accessgen.GenCommandOperationRestoreSemanticAttribute())
}

func (h Handler) setSemanticAttributeEnabled(w stdhttp.ResponseWriter, r *stdhttp.Request, enabled bool, operation accessgen.GenCommandOperationID) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, ok := repo.(access.SemanticAttributeRegistry)
	if !ok {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	versioned, versionedOK := repo.(access.VersionedSemanticAttributeRegistry)
	if !versionedOK {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	name := chi.URLParam(r, "attribute")
	current, err := registry.SemanticAttributeDefinition(r.Context(), name)
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	if err := checkIfMatch(r.Header.Get("If-Match"), semanticAttributeDefinitionETag(current)); err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	var row access.SemanticAttributeDefinition
	err = executeSemanticAttributeMutation(r, repo, operation, func() error {
		var callErr error
		row, callErr = versioned.SetSemanticAttributeEnabledExpected(r.Context(), name, enabled, current.DefinitionVersion, h.semanticAttributeMutationContext(r))
		return callErr
	})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	w.Header().Set("ETag", semanticAttributeDefinitionETag(row))
	writeJSON(w, stdhttp.StatusOK, semanticAttributeDefinitionDTO(row))
}
