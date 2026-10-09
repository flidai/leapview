package module

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	credential "github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	"github.com/flidai/leapview/internal/credential/encryption"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/pkg/strictjson"
)

const maxCredentialDraftEncodedBodyBytes int64 = 32 << 10

// CredentialDraftAPIGenConfig is the request-facing seam for the generated
// credential API. The service owns exact-resource authorization and storage;
// principal and environment values come only from the composed server.
type CredentialDraftAPIGenConfig struct {
	Service                *credential.Service
	Validation             *credential.ValidationService
	Activation             credential.ActivationService
	FirstSourcePreparation FirstSourcePreparationCommandService
	Environment            string
	CurrentPrincipal       func(*http.Request) (string, bool)
}

type credentialDraftAPIGenDispatcher struct {
	config CredentialDraftAPIGenConfig
}

func (d credentialDraftAPIGenDispatcher) ListConnectionCredentialDrafts(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
	params credentialgen.GenListConnectionCredentialDraftsParams,
) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := d.principal(w, r)
	if !ok {
		return
	}
	limit := 0
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	before := ""
	if params.BeforeVersionId != nil {
		before = *params.BeforeVersionId
	}
	if !validCredentialDraftListParams(params.Limit, params.BeforeVersionId) {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CREDENTIAL_DRAFT", "Credential draft request is invalid.", nil)
		return
	}
	page, err := d.config.Service.ListDrafts(r.Context(), actor, d.resource(project, target, connection), limit, before)
	if err != nil {
		writeCredentialDraftQueryError(w, r, err)
		return
	}
	items := make([]credentialgen.CredentialDraftResponse, 0, len(page.Items))
	for _, metadata := range page.Items {
		items = append(items, credentialDraftResponse(metadata))
	}
	response := credentialgen.CredentialDraftListResponse{Items: items}
	if page.NextBeforeVersionID != "" {
		response.NextBeforeVersionId = &page.NextBeforeVersionID
	}
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func validCredentialDraftListParams(limit *int32, beforeVersionID *string) bool {
	if limit != nil && (*limit < 1 || *limit > int32(credential.MaxDraftPageSize)) {
		return false
	}
	return beforeVersionID == nil || *beforeVersionID != ""
}

func (d credentialDraftAPIGenDispatcher) SaveCredentialDraft(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := d.principal(w, r)
	if !ok {
		return
	}
	fields, ok := decodeCredentialDraftFields(w, r)
	if !ok {
		return
	}
	defer clear(fields)
	resource := d.resource(project, target, connection)
	invocation := credentialgen.GenSaveCredentialDraftCommandInvocation{
		Surface: apigencommand.SurfaceAPI, Connection: connection,
		RequestID:     strings.TrimSpace(r.Header.Get("X-Request-ID")),
		CorrelationID: strings.TrimSpace(r.Header.Get("X-Correlation-ID")),
	}
	executor, err := apigencommand.NewExecutor(credentialgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		writeCredentialDraftCommandError(w, r, err)
		return
	}
	var metadata credential.Metadata
	err = credentialgen.ExecuteGenSaveCredentialDraftCommand(r.Context(), executor, invocation, apigencommand.Execution{
		// SaveDraft performs the encrypted write and its audit in one repository
		// transaction. The generated command executor marks that completed
		// transaction on the transport guard; it does not persist request bytes.
		Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
			var saveErr error
			metadata, saveErr = d.config.Service.SaveDraft(ctx, actor, resource, fields)
			return saveErr
		},
	})
	if err != nil {
		writeCredentialDraftCommandError(w, r, err)
		return
	}
	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+metadata.Binding.VersionID)
	apitransport.WriteJSON(w, http.StatusCreated, credentialDraftResponse(metadata))
}

func (d credentialDraftAPIGenDispatcher) GetConnectionCredentialDraft(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection, version string,
) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := d.principal(w, r)
	if !ok {
		return
	}
	metadata, err := d.config.Service.GetDraft(r.Context(), actor, d.resource(project, target, connection), version)
	if err != nil {
		writeCredentialDraftQueryError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, credentialDraftResponse(metadata))
}

func (d credentialDraftAPIGenDispatcher) principal(w http.ResponseWriter, r *http.Request) (string, bool) {
	if d.config.Service == nil {
		writeCredentialDraftUnavailable(w, r)
		return "", false
	}
	if d.config.CurrentPrincipal == nil {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Authentication is required.", nil)
		return "", false
	}
	actor, ok := d.config.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(actor) == "" {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Authentication is required.", nil)
		return "", false
	}
	return actor, true
}

func (d credentialDraftAPIGenDispatcher) resource(project, target, connection string) credential.Resource {
	return credential.Resource{
		ScopeKind: "connection", TargetID: target,
		ProjectID: project, Environment: d.config.Environment,
		ResourceID: connection,
	}
}

func credentialDraftResponse(metadata credential.Metadata) credentialgen.CredentialDraftResponse {
	return credentialgen.CredentialDraftResponse{
		VersionId: metadata.Binding.VersionID,
		CreatedAt: metadata.CreatedAt.UTC().Format(time.RFC3339Nano),
		State:     "draft",
	}
}

func DispatchAPIGenOperation(
	config CredentialDraftAPIGenConfig,
	operationID string,
	_ *slog.Logger,
	w http.ResponseWriter,
	r *http.Request,
) bool {
	return credentialgen.DispatchAPIGenOperation(operationID, credentialDraftAPIGenDispatcher{config: config}, credentialDraftTransportErrorResponder{}, w, r)
}

type credentialDraftTransportErrorResponder struct{}

func (credentialDraftTransportErrorResponder) RespondTransportError(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	failure credentialgen.GenTransportError,
) {
	// Generated path/query diagnostics can contain caller-controlled names.
	// Never log or include their Causes at this secret-bearing API boundary.
	apitransport.WriteAPIGenFailure(ctx, w, r, nil, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail,
	})
}

func decodeCredentialDraftFields(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	if r.Body == nil || r.Body == http.NoBody {
		writeCredentialDraftInvalidBody(w, r)
		return nil, false
	}
	if r.ContentLength > maxCredentialDraftEncodedBodyBytes {
		writeCredentialDraftBodyTooLarge(w, r)
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxCredentialDraftEncodedBodyBytes+1))
	if err != nil {
		writeCredentialDraftInvalidBody(w, r)
		return nil, false
	}
	if int64(len(raw)) > maxCredentialDraftEncodedBodyBytes {
		writeCredentialDraftBodyTooLarge(w, r)
		return nil, false
	}
	defer clear(raw)
	if !utf8.Valid(raw) || !validCredentialJSONText(raw) {
		writeCredentialDraftInvalidBody(w, r)
		return nil, false
	}
	var envelope map[string]json.RawMessage
	if err := strictjson.DecodeWithOptions(raw, &envelope, strictjson.Options{
		MaxBytes: maxCredentialDraftEncodedBodyBytes, MaxDepth: 3,
	}); err != nil || envelope == nil || len(envelope) != 1 {
		writeCredentialDraftInvalidBody(w, r)
		return nil, false
	}
	fieldValue, ok := envelope["fields"]
	if !ok {
		writeCredentialDraftInvalidBody(w, r)
		return nil, false
	}
	fieldBytes := bytes.TrimSpace(fieldValue)
	if len(fieldBytes) == 0 || fieldBytes[0] != '{' {
		writeCredentialDraftInvalidBody(w, r)
		return nil, false
	}
	var rawFields map[string]json.RawMessage
	if err := strictjson.DecodeWithOptions(fieldBytes, &rawFields, strictjson.Options{
		MaxBytes: maxCredentialDraftEncodedBodyBytes, MaxDepth: 2,
	}); err != nil || rawFields == nil {
		writeCredentialDraftInvalidBody(w, r)
		return nil, false
	}
	fields := make(map[string]string, len(rawFields))
	for name, value := range rawFields {
		encoded := bytes.TrimSpace(value)
		if len(encoded) == 0 || encoded[0] != '"' {
			writeCredentialDraftInvalidBody(w, r)
			return nil, false
		}
		var text string
		if err := json.Unmarshal(encoded, &text); err != nil || !utf8.ValidString(text) {
			writeCredentialDraftInvalidBody(w, r)
			return nil, false
		}
		fields[name] = text
	}
	return fields, true
}

// validCredentialJSONText prevents encoding/json's replacement of malformed
// escaped UTF-16 surrogates with U+FFFD from silently changing a secret value.
func validCredentialJSONText(raw []byte) bool {
	for i := 0; i < len(raw); {
		if raw[i] != '"' {
			i++
			continue
		}
		i++
		for i < len(raw) {
			switch raw[i] {
			case '"':
				i++
				goto nextString
			case '\\':
				if i+1 >= len(raw) {
					return false
				}
				if raw[i+1] != 'u' {
					i += 2
					continue
				}
				unit, ok := credentialHexUnit(raw[i+2:])
				if !ok {
					return false
				}
				if unit >= 0xD800 && unit <= 0xDBFF {
					if i+12 > len(raw) || raw[i+6] != '\\' || raw[i+7] != 'u' {
						return false
					}
					low, ok := credentialHexUnit(raw[i+8:])
					if !ok || low < 0xDC00 || low > 0xDFFF {
						return false
					}
					i += 12
					continue
				}
				if unit >= 0xDC00 && unit <= 0xDFFF {
					return false
				}
				i += 6
			default:
				i++
			}
		}
		return false
	nextString:
	}
	return true
}

func credentialHexUnit(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var value uint16
	for _, digit := range raw[:4] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func writeCredentialDraftInvalidBody(w http.ResponseWriter, r *http.Request) {
	apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CREDENTIAL_DRAFT", "Credential draft request body is invalid.", nil)
}

func writeCredentialDraftBodyTooLarge(w http.ResponseWriter, r *http.Request) {
	apitransport.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "CONTENT_TOO_LARGE", "The credential draft request body exceeds the configured size limit.", nil)
}

func writeCredentialDraftUnavailable(w http.ResponseWriter, r *http.Request) {
	apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "CREDENTIAL_SERVICE_UNAVAILABLE", "Credential draft service is unavailable.", nil)
}

func writeCredentialDraftQueryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, credential.ErrInvalid):
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CREDENTIAL_DRAFT", "Credential draft request is invalid.", nil)
	case errors.Is(err, credential.ErrInvalidCursor):
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CREDENTIAL_DRAFT", "Credential draft cursor is invalid.", nil)
	case errors.Is(err, credential.ErrForbidden):
		apitransport.WriteProblem(w, r, http.StatusForbidden, "CREDENTIAL_DRAFT_FORBIDDEN", "Credential draft access is forbidden.", nil)
	case errors.Is(err, credential.ErrNotFound):
		apitransport.WriteProblem(w, r, http.StatusNotFound, "CREDENTIAL_DRAFT_NOT_FOUND", "Credential draft was not found.", nil)
	default:
		writeCredentialDraftUnavailable(w, r)
	}
}

func writeCredentialDraftCommandError(w http.ResponseWriter, r *http.Request, err error) {
	kind := "provider_unavailable"
	switch {
	case errors.Is(err, credential.ErrInvalid):
		kind = "invalid"
	case errors.Is(err, credential.ErrForbidden):
		kind = "unauthorized"
	case errors.Is(err, credential.ErrNotFound):
		kind = "not_found"
	case errors.Is(err, credential.ErrConflict):
		kind = "conflict"
	case errors.Is(err, encryption.ErrBudgetExhausted), errors.Is(err, credential.ErrUnavailable):
		kind = "provider_unavailable"
	}
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil,
		credentialgen.GenCommandOperationSaveCredentialDraft(),
		credentialgen.GetAPIGenCommandFailureContracts,
		apigenfailure.New(kind, "credential draft operation failed"),
	)
}
