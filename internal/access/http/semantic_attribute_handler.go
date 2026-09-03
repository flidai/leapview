package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (h Handler) ListPrincipalSemanticAttributeAssignments(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.listSemanticAttributeAssignments(w, r, access.SubjectKindPrincipal)
}

func (h Handler) ListGroupSemanticAttributeAssignments(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.listSemanticAttributeAssignments(w, r, access.SubjectKindGroup)
}

func (h Handler) listSemanticAttributeAssignments(w stdhttp.ResponseWriter, r *stdhttp.Request, kind access.SubjectKind) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	reader, ok := repo.(access.SemanticAttributeAssignmentReader)
	if !ok {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	subject, err := access.NewSubjectRef(kind, firstNonEmpty(chi.URLParam(r, string(kind))))
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	rows, err := reader.SemanticAttributeAssignments(r.Context(), access.SemanticAttributeAssignmentFilter{Subject: subject})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if row.Tombstoned {
			continue
		}
		items = append(items, semanticAttributeAssignmentDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}

func (h Handler) UpsertPrincipalSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.upsertSemanticAttributeAssignment(w, r, access.SubjectKindPrincipal, accessgen.GenCommandOperationUpsertPrincipalSemanticAttributeAssignment())
}

func (h Handler) UpsertGroupSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.upsertSemanticAttributeAssignment(w, r, access.SubjectKindGroup, accessgen.GenCommandOperationUpsertGroupSemanticAttributeAssignment())
}

func (h Handler) upsertSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request, kind access.SubjectKind, operation accessgen.GenCommandOperationID) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input semanticAttributeAssignmentWire
	if err := decodeStrictJSON(r, &input); err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, registryOK := repo.(access.SemanticAttributeRegistry)
	reader, readerOK := repo.(access.SemanticAttributeAssignmentReader)
	writer, writerOK := repo.(access.SemanticAttributeAssignmentWriter)
	if !registryOK || !readerOK || !writerOK {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	name := chi.URLParam(r, "attribute")
	definition, err := registry.SemanticAttributeDefinition(r.Context(), name)
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	values, err := semanticAttributeValues(definition, input.Values)
	if err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	subject, err := access.NewSubjectRef(kind, chi.URLParam(r, string(kind)))
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	current, err := findSemanticAttributeAssignment(r, reader, definition.ID, subject)
	if err != nil && !errors.Is(err, errSemanticAttributeAssignmentMissing) {
		writeSemanticAttributeError(w, err)
		return
	}
	if err := checkIfMatch(r.Header.Get("If-Match"), semanticAttributeAssignmentETag(current)); err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	var row access.SemanticAttributeAssignment
	err = executeSemanticAttributeMutation(r, repo, operation, func() error {
		var callErr error
		row, callErr = writer.SetSemanticAttributeAssignment(r.Context(), access.SemanticAttributeAssignmentInput{
			AssignmentID: current.ID, DefinitionID: definition.ID, DefinitionName: definition.Name,
			Subject: subject, Values: values, ExpectedVersion: current.AssignmentVersion,
			Mutation: h.semanticAttributeMutationContext(r),
		})
		return callErr
	})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	w.Header().Set("ETag", semanticAttributeAssignmentETag(row))
	writeJSON(w, stdhttp.StatusOK, semanticAttributeAssignmentDTO(row))
}

func (h Handler) RemovePrincipalSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.removeSemanticAttributeAssignment(w, r, access.SubjectKindPrincipal, accessgen.GenCommandOperationRemovePrincipalSemanticAttributeAssignment())
}

func (h Handler) RemoveGroupSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.removeSemanticAttributeAssignment(w, r, access.SubjectKindGroup, accessgen.GenCommandOperationRemoveGroupSemanticAttributeAssignment())
}

func (h Handler) removeSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request, kind access.SubjectKind, operation accessgen.GenCommandOperationID) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, registryOK := repo.(access.SemanticAttributeRegistry)
	reader, readerOK := repo.(access.SemanticAttributeAssignmentReader)
	writer, writerOK := repo.(access.SemanticAttributeAssignmentWriter)
	if !registryOK || !readerOK || !writerOK {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	definition, err := registry.SemanticAttributeDefinition(r.Context(), chi.URLParam(r, "attribute"))
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	subject, err := access.NewSubjectRef(kind, chi.URLParam(r, string(kind)))
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	current, err := findSemanticAttributeAssignment(r, reader, definition.ID, subject)
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	if err := checkIfMatch(r.Header.Get("If-Match"), semanticAttributeAssignmentETag(current)); err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	err = executeSemanticAttributeMutation(r, repo, operation, func() error {
		_, callErr := writer.TombstoneSemanticAttributeAssignment(r.Context(), current.ID, current.AssignmentVersion, h.semanticAttributeMutationContext(r))
		return callErr
	})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListSemanticAttributeClaimMappings(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, registryOK := repo.(access.SemanticAttributeRegistry)
	reader, readerOK := repo.(access.TrustedClaimMappingReader)
	if !registryOK || !readerOK {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	definition, err := registry.SemanticAttributeDefinition(r.Context(), chi.URLParam(r, "attribute"))
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	rows, err := reader.TrustedClaimMappings(r.Context(), access.TrustedClaimMappingFilter{})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if row.DefinitionID == definition.ID && !row.Tombstoned {
			items = append(items, semanticAttributeClaimMappingDTO(row))
		}
	}
	_ = writePagedJSON(w, r, items)
}

func (h Handler) UpsertSemanticAttributeClaimMapping(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input semanticAttributeClaimMappingWire
	if err := decodeStrictJSON(r, &input); err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	sourceKind, err := semanticAttributeClaimSourceKind(input.SourceKind)
	if err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	if strings.TrimSpace(input.Provider) == "" || strings.TrimSpace(input.Issuer) == "" || strings.TrimSpace(input.Audience) == "" || strings.TrimSpace(input.Claim) == "" {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(errors.New("provider, issuer, audience, and claim are required")))
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, registryOK := repo.(access.SemanticAttributeRegistry)
	reader, readerOK := repo.(access.TrustedClaimMappingReader)
	writer, writerOK := repo.(access.TrustedClaimMappingWriter)
	if !registryOK || !readerOK || !writerOK {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	definition, err := registry.SemanticAttributeDefinition(r.Context(), chi.URLParam(r, "attribute"))
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	source := access.TrustedClaimMappingFilter{SourceKind: sourceKind, Provider: input.Provider, Issuer: input.Issuer, Audience: input.Audience, Claim: input.Claim}
	rows, err := reader.TrustedClaimMappings(r.Context(), source)
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	var current access.TrustedClaimMapping
	for _, candidate := range rows {
		if candidate.DefinitionID == definition.ID && !candidate.Tombstoned {
			current = candidate
			break
		}
	}
	if err := checkIfMatch(r.Header.Get("If-Match"), semanticAttributeMappingETag(current)); err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	var row access.TrustedClaimMapping
	err = executeSemanticAttributeMutation(r, repo, accessgen.GenCommandOperationUpsertSemanticAttributeClaimMapping(), func() error {
		var callErr error
		row, callErr = writer.SetTrustedClaimMapping(r.Context(), access.TrustedClaimMappingInput{
			MappingID: current.ID, SourceKind: sourceKind, Provider: input.Provider,
			Issuer: source.Issuer, Audience: source.Audience, Claim: input.Claim,
			DefinitionID: definition.ID, DefinitionName: definition.Name, ExpectedVersion: current.MappingVersion,
			Mutation: h.semanticAttributeMutationContext(r),
		})
		return callErr
	})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	w.Header().Set("ETag", semanticAttributeMappingETag(row))
	writeJSON(w, stdhttp.StatusOK, semanticAttributeClaimMappingDTO(row))
}

func (h Handler) RemoveSemanticAttributeClaimMapping(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, registryOK := repo.(access.SemanticAttributeRegistry)
	reader, readerOK := repo.(access.TrustedClaimMappingReader)
	writer, writerOK := repo.(access.TrustedClaimMappingWriter)
	if !registryOK || !readerOK || !writerOK {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	definition, err := registry.SemanticAttributeDefinition(r.Context(), chi.URLParam(r, "attribute"))
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	rows, err := reader.TrustedClaimMappings(r.Context(), access.TrustedClaimMappingFilter{IncludeTombstones: true})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	mappingID := chi.URLParam(r, "mapping")
	var current access.TrustedClaimMapping
	for _, candidate := range rows {
		if candidate.ID == mappingID && candidate.DefinitionID == definition.ID && !candidate.Tombstoned {
			current = candidate
			break
		}
	}
	if current.ID == "" {
		writeSemanticAttributeError(w, errSemanticAttributeMappingMissing)
		return
	}
	if err := checkIfMatch(r.Header.Get("If-Match"), semanticAttributeMappingETag(current)); err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	err = executeSemanticAttributeMutation(r, repo, accessgen.GenCommandOperationRemoveSemanticAttributeClaimMapping(), func() error {
		_, callErr := writer.TombstoneTrustedClaimMapping(r.Context(), current.ID, current.MappingVersion, h.semanticAttributeMutationContext(r))
		return callErr
	})
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) PreviewSemanticAttributeImpact(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input semanticAttributeImpactWire
	if err := decodeStrictJSON(r, &input); err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	kind, err := semanticAttributeSubjectKind(input.TargetKind)
	if err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	subject, err := access.NewSubjectRef(kind, input.TargetID)
	if err != nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	registry, registryOK := repo.(access.SemanticAttributeRegistry)
	reader, readerOK := repo.(access.SemanticAttributeAssignmentReader)
	if !registryOK || !readerOK {
		writeSemanticAttributeError(w, errSemanticAttributeUnavailable)
		return
	}
	name := chi.URLParam(r, "attribute")
	definition, err := registry.SemanticAttributeDefinition(r.Context(), name)
	if err != nil {
		writeSemanticAttributeError(w, err)
		return
	}
	if input.Values == nil {
		writeSemanticAttributeError(w, invalidSemanticAttributeRequest(errors.New("values is required")))
		return
	}
	var requestedValues []string
	if len(input.Values) > 0 {
		decodedValues, err := semanticAttributeValues(definition, input.Values)
		if err != nil {
			writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
			return
		}
		requestedValues, _, err = access.CanonicalSemanticAttributeValues(definition, decodedValues)
		if err != nil {
			writeSemanticAttributeError(w, invalidSemanticAttributeRequest(err))
			return
		}
	}
	current, err := findSemanticAttributeAssignment(r, reader, definition.ID, subject)
	if err != nil && !errors.Is(err, errSemanticAttributeAssignmentMissing) {
		writeSemanticAttributeError(w, err)
		return
	}
	changed := current.ID != ""
	if len(input.Values) > 0 {
		changed = current.ID == "" || !slices.Equal(current.CanonicalValues, requestedValues)
	}
	principalCount, groupCount := int32(0), int32(0)
	if changed {
		if subject.Kind == access.SubjectKindPrincipal {
			principalCount = 1
		} else {
			groupCount = 1
		}
	}
	warnings := []string{}
	if !changed {
		if current.ID == "" {
			warnings = append(warnings, "no active assignments are currently affected")
		} else {
			warnings = append(warnings, "requested assignment is already in the requested state")
		}
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"attributeName": name, "targetKind": string(kind), "targetId": subject.ID, "affectedPrincipalCount": principalCount, "affectedGroupCount": groupCount, "warnings": warnings})
}

var (
	errSemanticAttributeUnavailable       = errors.New("semantic attribute control is unavailable")
	errSemanticAttributeAssignmentMissing = errors.New("semantic attribute assignment was not found")
	errSemanticAttributeMappingMissing    = errors.New("semantic attribute claim mapping was not found")
	errSemanticAttributeInvalid           = errors.New("invalid semantic attribute request")
)

func invalidSemanticAttributeRequest(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", errSemanticAttributeInvalid, err)
}

func (h Handler) semanticAttributeMutationContext(r *stdhttp.Request) access.SemanticAttributeMutationContext {
	return access.SemanticAttributeMutationContext{ActorPrincipalID: h.currentPrincipalID(r), RequestID: requestIDFromRequest(r), CorrelationID: correlationIDFromRequest(r)}
}

var semanticAttributeOperationAuditActions = map[string]string{
	"registerSemanticAttribute":                  access.SemanticAttributeAuditActionRegister,
	"updateSemanticAttributeMetadata":            access.SemanticAttributeAuditActionMetadataUpdate,
	"disableSemanticAttribute":                   access.SemanticAttributeAuditActionDisable,
	"restoreSemanticAttribute":                   access.SemanticAttributeAuditActionEnable,
	"upsertPrincipalSemanticAttributeAssignment": access.SemanticAttributeAuditActionAssignmentSet,
	"removePrincipalSemanticAttributeAssignment": access.SemanticAttributeAuditActionAssignmentTombstone,
	"upsertGroupSemanticAttributeAssignment":     access.SemanticAttributeAuditActionAssignmentSet,
	"removeGroupSemanticAttributeAssignment":     access.SemanticAttributeAuditActionAssignmentTombstone,
	"upsertSemanticAttributeClaimMapping":        access.SemanticAttributeAuditActionClaimMappingSet,
	"removeSemanticAttributeClaimMapping":        access.SemanticAttributeAuditActionClaimMappingTombstone,
}

// Domain semantic-control methods own their audit transaction. The executor
// wrapper is still required for generated API invocations so the transport
// command guard observes successful execution without opening a second
// repository transaction around an already transactional domain call. The
// contract action is checked here so a TypeSpec drift cannot silently report a
// different event than the repository appends.
func executeSemanticAttributeMutation(r *stdhttp.Request, repo access.Repository, operation accessgen.GenCommandOperationID, mutation func() error) error {
	if _, generated := apigencommand.OperationID(r.Context()); !generated {
		return mutation()
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), operation.APIGenOperationID(), apigencommand.Execution{
		Transactional: func(_ context.Context, contract apigencommand.Contract) error {
			if durableAction := semanticAttributeOperationAuditActions[operation.APIGenOperationID()]; durableAction != "" && contract.AuditAction != durableAction {
				return fmt.Errorf("generated audit action %q does not match semantic attribute durable action %q", contract.AuditAction, durableAction)
			}
			return mutation()
		},
	})
}

func semanticAttributeType(value string) (semanticvalue.Type, error) {
	typeName := semanticvalue.Type(value)
	switch typeName {
	case semanticvalue.TypeString, semanticvalue.TypeBoolean, semanticvalue.TypeInteger, semanticvalue.TypeDecimal, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		return typeName, nil
	}
	return "", errors.New("unsupported semantic attribute value type")
}

func semanticAttributeShape(value string) (access.SemanticAttributeShape, error) {
	shape := access.SemanticAttributeShape(value)
	if !shape.Valid() {
		return "", errors.New("unsupported semantic attribute value shape")
	}
	return shape, nil
}

func semanticAttributeMetadata(input semanticAttributeMetadataWire) (access.SemanticAttributeMetadata, error) {
	kind := access.SemanticAttributeOwnerKind(input.OwnerKind)
	if !kind.Valid() {
		return access.SemanticAttributeMetadata{}, errors.New("unsupported semantic attribute owner kind")
	}
	id := ""
	if input.OwnerID != nil {
		id = strings.TrimSpace(*input.OwnerID)
	}
	if kind != access.SemanticAttributeOwnerInstance && id == "" {
		return access.SemanticAttributeMetadata{}, errors.New("ownerId is required for principal and group owners")
	}
	if kind == access.SemanticAttributeOwnerInstance && id != "" {
		return access.SemanticAttributeMetadata{}, errors.New("instance ownerId must be omitted")
	}
	documentation := ""
	if input.DocumentationURL != nil {
		documentation = *input.DocumentationURL
	}
	return access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: kind, ID: id}, DisplayName: input.DisplayName, Description: input.Description, DocumentationURL: documentation}, nil
}

func semanticAttributeSubjectKind(value string) (access.SubjectKind, error) {
	kind := access.SubjectKind(value)
	if kind != access.SubjectKindPrincipal && kind != access.SubjectKindGroup {
		return "", errors.New("targetKind must be principal or group")
	}
	return kind, nil
}

func semanticAttributeClaimSourceKind(value string) (access.TrustedClaimSourceKind, error) {
	kind := access.TrustedClaimSourceKind(value)
	if !kind.Valid() {
		return "", errors.New("unsupported semantic attribute claim source kind")
	}
	return kind, nil
}

func semanticAttributeValues(definition access.SemanticAttributeDefinition, values []semanticAttributeValueWire) (any, error) {
	if len(values) == 0 {
		return nil, errors.New("values must contain at least one item")
	}
	decoded := make([]any, len(values))
	for i, value := range values {
		item, err := semanticAttributeValue(definition.Type, value)
		if err != nil {
			return nil, fmt.Errorf("values[%d]: %w", i, err)
		}
		decoded[i] = item
	}
	if definition.Shape == access.SemanticAttributeScalar {
		if len(decoded) != 1 {
			return nil, errors.New("scalar attributes require exactly one value")
		}
		return decoded[0], nil
	}
	return decoded, nil
}

func semanticAttributeValue(expected semanticvalue.Type, value semanticAttributeValueWire) (any, error) {
	got, err := semanticAttributeType(value.Type)
	if err != nil || got != expected {
		return nil, errors.New("value type does not match the registered attribute type")
	}
	set := 0
	if value.StringValue != nil {
		set++
	}
	if value.BooleanValue != nil {
		set++
	}
	if value.IntegerValue != nil {
		set++
	}
	if value.DecimalValue != nil {
		set++
	}
	if value.DateValue != nil {
		set++
	}
	if value.TimestampValue != nil {
		set++
	}
	if set != 1 {
		return nil, errors.New("exactly one typed value field is required")
	}
	switch expected {
	case semanticvalue.TypeString:
		if value.StringValue == nil {
			return nil, errors.New("stringValue is required")
		}
		return *value.StringValue, nil
	case semanticvalue.TypeBoolean:
		if value.BooleanValue == nil {
			return nil, errors.New("booleanValue is required")
		}
		return *value.BooleanValue, nil
	case semanticvalue.TypeInteger:
		if value.IntegerValue == nil {
			return nil, errors.New("integerValue is required")
		}
		n, err := strconv.ParseInt(*value.IntegerValue, 10, 64)
		if err != nil {
			return nil, errors.New("integerValue is invalid")
		}
		return n, nil
	case semanticvalue.TypeDecimal:
		if value.DecimalValue == nil {
			return nil, errors.New("decimalValue is required")
		}
		return *value.DecimalValue, nil
	case semanticvalue.TypeDate:
		if value.DateValue == nil {
			return nil, errors.New("dateValue is required")
		}
		return *value.DateValue, nil
	case semanticvalue.TypeTimestamp:
		if value.TimestampValue == nil {
			return nil, errors.New("timestampValue is required")
		}
		return *value.TimestampValue, nil
	default:
		return nil, errors.New("unsupported semantic attribute value type")
	}
}

func findSemanticAttributeAssignment(r *stdhttp.Request, reader access.SemanticAttributeAssignmentReader, definitionID string, subject access.SubjectRef) (access.SemanticAttributeAssignment, error) {
	rows, err := reader.SemanticAttributeAssignments(r.Context(), access.SemanticAttributeAssignmentFilter{DefinitionID: definitionID, Subject: subject})
	if err != nil {
		return access.SemanticAttributeAssignment{}, err
	}
	for _, row := range rows {
		if !row.Tombstoned {
			return row, nil
		}
	}
	return access.SemanticAttributeAssignment{}, errSemanticAttributeAssignmentMissing
}

func semanticAttributeDefinitionDTO(row access.SemanticAttributeDefinition) map[string]any {
	return map[string]any{"id": row.ID, "name": row.Name, "type": string(row.Type), "shape": string(row.Shape), "profile": row.Profile, "definitionVersion": row.DefinitionVersion, "ownerKind": string(row.Metadata.Owner.Kind), "ownerId": emptyToNil(row.Metadata.Owner.ID), "displayName": row.Metadata.DisplayName, "description": row.Metadata.Description, "documentationUrl": emptyToNil(row.Metadata.DocumentationURL), "lifecycleState": string(row.LifecycleState), "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt, "disabledAt": emptyToNil(row.DisabledAt)}
}

func semanticAttributeAssignmentDTO(row access.SemanticAttributeAssignment) map[string]any {
	values := make([]map[string]any, 0, len(row.CanonicalValues))
	for _, canonical := range row.CanonicalValues {
		values = append(values, semanticAttributeCanonicalValueDTO(row.Type, canonical))
	}
	lifecycle := "active"
	if row.Tombstoned {
		lifecycle = "tombstoned"
	}
	return map[string]any{"id": row.ID, "attributeName": row.DefinitionName, "targetKind": string(row.Subject.Kind), "targetId": row.Subject.ID, "values": values, "assignmentVersion": row.AssignmentVersion, "lifecycleState": lifecycle, "removedAt": emptyToNil(row.TombstonedAt), "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt}
}

func semanticAttributeCanonicalValueDTO(typeName semanticvalue.Type, canonical string) map[string]any {
	result := map[string]any{"type": string(typeName)}
	switch typeName {
	case semanticvalue.TypeString:
		result["stringValue"] = canonical
	case semanticvalue.TypeBoolean:
		result["booleanValue"] = canonical == "true"
	case semanticvalue.TypeInteger:
		result["integerValue"] = canonical
	case semanticvalue.TypeDecimal:
		result["decimalValue"] = canonical
	case semanticvalue.TypeDate:
		result["dateValue"] = canonical
	case semanticvalue.TypeTimestamp:
		result["timestampValue"] = canonical
	}
	return result
}

func semanticAttributeClaimMappingDTO(row access.TrustedClaimMapping) map[string]any {
	lifecycle := "active"
	if row.Tombstoned {
		lifecycle = "tombstoned"
	}
	return map[string]any{"id": row.ID, "attributeName": row.DefinitionName, "sourceKind": string(row.SourceKind), "provider": row.Provider, "issuer": row.Issuer, "audience": row.Audience, "claim": row.Claim, "mappingVersion": row.MappingVersion, "lifecycleState": lifecycle, "removedAt": emptyToNil(row.TombstonedAt), "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt}
}

func semanticAttributeDefinitionETag(row access.SemanticAttributeDefinition) string {
	return resourceETag(struct {
		ID      string
		Version int64
	}{row.ID, row.DefinitionVersion})
}
func semanticAttributeAssignmentETag(row access.SemanticAttributeAssignment) string {
	if row.ID == "" {
		return ""
	}
	return resourceETag(struct {
		ID      string
		Version int64
	}{row.ID, row.AssignmentVersion})
}
func semanticAttributeMappingETag(row access.TrustedClaimMapping) string {
	if row.ID == "" {
		return ""
	}
	return resourceETag(struct {
		ID      string
		Version int64
	}{row.ID, row.MappingVersion})
}

func writeSemanticAttributeError(w stdhttp.ResponseWriter, err error) {
	status := stdhttp.StatusInternalServerError
	switch {
	case errors.Is(err, errIfMatchRequired), errors.Is(err, errIfMatchFailed), errors.Is(err, apigencommand.ErrPreconditionFailed):
		status = stdhttp.StatusPreconditionFailed
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, pgx.ErrNoRows), errors.Is(err, access.ErrSemanticAttributeNotFound), errors.Is(err, errSemanticAttributeAssignmentMissing), errors.Is(err, errSemanticAttributeMappingMissing):
		status = stdhttp.StatusNotFound
	case errors.Is(err, access.ErrSemanticAttributeConflict), errors.Is(err, access.ErrSemanticAttributeAssignmentConflict), errors.Is(err, access.ErrSemanticAttributeMappingConflict), errors.Is(err, access.ErrSemanticAttributeSourceConflict):
		status = stdhttp.StatusConflict
	case errors.Is(err, errSemanticAttributeUnavailable):
		status = stdhttp.StatusServiceUnavailable
	case errors.Is(err, access.ErrSemanticAttributeControlCorrupt):
		status = stdhttp.StatusServiceUnavailable
	case errors.Is(err, errSemanticAttributeInvalid), errors.Is(err, semanticvalue.ErrInvalidValue), errors.Is(err, semanticvalue.ErrInvalidType), errors.Is(err, access.ErrInvalidSubjectRef), errors.Is(err, access.ErrSemanticAttributeDisabled):
		status = stdhttp.StatusBadRequest
	default:
		// Handler errors are intentionally not sent to clients: SQL and claim
		// details can disclose durable identifiers or provider configuration.
		writeJSONError(w, errors.New("internal server error"), status)
		return
	}
	detail := "invalid semantic attribute request"
	if status == stdhttp.StatusNotFound {
		detail = "semantic attribute resource was not found"
	}
	if status == stdhttp.StatusConflict {
		detail = "semantic attribute resource conflicts with current state"
	}
	if status == stdhttp.StatusPreconditionFailed {
		detail = "resource changed; refresh and retry with its current ETag"
	}
	if status == stdhttp.StatusServiceUnavailable {
		detail = "semantic attribute control is unavailable"
	}
	writeJSONError(w, errors.New(detail), status)
}
