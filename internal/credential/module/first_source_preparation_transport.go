package module

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	credential "github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/pkg/strictjson"
)

const maxFirstSourcePreparationBodyBytes int64 = 4096

func (d credentialDraftAPIGenDispatcher) PrepareFirstSourceCredential(w http.ResponseWriter, r *http.Request, project, target, connection, version string) {
	d.prepareFirstSource(w, r, project, target, connection, version, apigencommand.SurfaceAPI)
}

func (d credentialDraftAPIGenDispatcher) RenewFirstSourceCredentialPreparation(w http.ResponseWriter, r *http.Request, project, target, connection, preparation string) {
	d.renewFirstSource(w, r, project, target, connection, preparation, apigencommand.SurfaceAPI)
}

// DispatchFirstSourcePreparationBrowser executes the same canonical commands
// through their generated UI exposure. Mount with browser session authentication,
// CSRF, ingress limits and the generated browser mutation middleware. Scope is
// server-bound; an operation claim never chooses another command or authority.
func DispatchFirstSourcePreparationBrowser(config CredentialDraftAPIGenConfig, operation string, w http.ResponseWriter, r *http.Request, project, target, connection, identity string) bool {
	d := credentialDraftAPIGenDispatcher{config: config}
	switch operation {
	case "abortCredentialActivation":
		if _, ok := d.firstSourcePrincipal(w, r, d.resource(project, target, connection)); !ok {
			return true
		}
		d.abortCredentialActivation(w, r, project, target, connection, identity, apigencommand.SurfaceUI)
	case "saveCredentialDraft", "validateCredentialDraft":
		if _, ok := d.firstSourcePrincipal(w, r, d.resource(project, target, connection)); !ok {
			return true
		}
		if operation == "saveCredentialDraft" {
			d.saveCredentialDraft(w, r, project, target, connection, apigencommand.SurfaceUI)
		} else {
			d.validateCredentialDraft(w, r, project, target, connection, identity, apigencommand.SurfaceUI)
		}
	case "prepareFirstSourceCredential":
		d.prepareFirstSource(w, r, project, target, connection, identity, apigencommand.SurfaceUI)
	case "renewFirstSourceCredentialPreparation":
		d.renewFirstSource(w, r, project, target, connection, identity, apigencommand.SurfaceUI)
	default:
		return false
	}
	return true
}

func (d credentialDraftAPIGenDispatcher) firstSourcePrincipal(w http.ResponseWriter, r *http.Request, resource credential.Resource) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if d.config.CurrentPrincipal == nil {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Authentication is required.", nil)
		return "", false
	}
	actor, ok := d.config.CurrentPrincipal(r)
	if !ok || actor == "" || actor != strings.TrimSpace(actor) {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Authentication is required.", nil)
		return "", false
	}
	if typednil.IsNil(d.config.FirstSourcePreparation) {
		writeFirstSourcePreparationError(w, r, credential.ErrUnavailable)
		return "", false
	}
	if err := d.config.FirstSourcePreparation.Authorize(r.Context(), actor, resource); err != nil {
		writeFirstSourcePreparationError(w, r, err)
		return "", false
	}
	return actor, true
}

func (d credentialDraftAPIGenDispatcher) prepareFirstSource(w http.ResponseWriter, r *http.Request, project, target, connection, version string, surface apigencommand.Surface) {
	resource := d.resource(project, target, connection)
	actor, ok := d.firstSourcePrincipal(w, r, resource)
	if !ok {
		return
	}
	body, ok := decodeFirstSourcePreparationObject(w, r)
	if !ok {
		return
	}
	request := FirstSourcePreparationCommandRequest{VersionID: version}
	if len(body) != 6 || !credentialActivationUUID(version) ||
		json.Unmarshal(body["preparationId"], &request.PreparationID) != nil || !credentialActivationUUID(request.PreparationID) ||
		json.Unmarshal(body["receiptId"], &request.ReceiptID) != nil || !credentialActivationUUID(request.ReceiptID) ||
		json.Unmarshal(body["sourceDigest"], &request.SourceDigest) != nil || platformdigest.ValidateSHA256Identity(request.SourceDigest) != nil ||
		json.Unmarshal(body["sourceAttestationDigest"], &request.SourceAttestationDigest) != nil || platformdigest.ValidateSHA256Identity(request.SourceAttestationDigest) != nil ||
		json.Unmarshal(body["planIdempotencyKey"], &request.PlanIdempotencyKey) != nil || request.PlanIdempotencyKey == "" || len(request.PlanIdempotencyKey) > 255 || request.PlanIdempotencyKey != strings.TrimSpace(request.PlanIdempotencyKey) ||
		json.Unmarshal(body["expectedTargetRevision"], &request.ExpectedTargetRevision) != nil || request.ExpectedTargetRevision < 1 {
		writeFirstSourcePreparationError(w, r, credential.ErrInvalid)
		return
	}
	executeFirstSourcePreparation(w, r, "prepareFirstSourceCredential", surface, connection, func(ctx context.Context) (FirstSourcePreparationCommandResult, error) {
		return d.config.FirstSourcePreparation.Prepare(ctx, actor, resource, request)
	})
}

func (d credentialDraftAPIGenDispatcher) renewFirstSource(w http.ResponseWriter, r *http.Request, project, target, connection, preparation string, surface apigencommand.Surface) {
	resource := d.resource(project, target, connection)
	actor, ok := d.firstSourcePrincipal(w, r, resource)
	if !ok {
		return
	}
	body, ok := decodeFirstSourcePreparationObject(w, r)
	if !ok {
		return
	}
	var receipt string
	if len(body) != 1 || !credentialActivationUUID(preparation) || json.Unmarshal(body["receiptId"], &receipt) != nil || !credentialActivationUUID(receipt) {
		writeFirstSourcePreparationError(w, r, credential.ErrInvalid)
		return
	}
	executeFirstSourcePreparation(w, r, "renewFirstSourceCredentialPreparation", surface, connection, func(ctx context.Context) (FirstSourcePreparationCommandResult, error) {
		return d.config.FirstSourcePreparation.Renew(ctx, actor, resource, preparation, receipt)
	})
}

func decodeFirstSourcePreparationObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	for name := range r.Header {
		if strings.EqualFold(name, "Idempotency-Key") {
			writeFirstSourcePreparationError(w, r, credential.ErrInvalid)
			return nil, false
		}
	}
	if r.Body == nil || r.Body == http.NoBody {
		writeFirstSourcePreparationError(w, r, credential.ErrInvalid)
		return nil, false
	}
	if r.ContentLength > maxFirstSourcePreparationBodyBytes {
		writeFirstSourcePreparationTooLarge(w, r)
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxFirstSourcePreparationBodyBytes+1))
	if int64(len(raw)) > maxFirstSourcePreparationBodyBytes {
		writeFirstSourcePreparationTooLarge(w, r)
		return nil, false
	}
	var body map[string]json.RawMessage
	if err != nil || (r.ContentLength > 0 && int64(len(raw)) != r.ContentLength) || !utf8.Valid(raw) || !validCredentialJSONText(raw) ||
		strictjson.DecodeWithOptions(raw, &body, strictjson.Options{MaxBytes: maxFirstSourcePreparationBodyBytes, MaxDepth: 2}) != nil || body == nil {
		writeFirstSourcePreparationError(w, r, credential.ErrInvalid)
		return nil, false
	}
	return body, true
}

func executeFirstSourcePreparation(w http.ResponseWriter, r *http.Request, operation string, surface apigencommand.Surface, connection string, mutate func(context.Context) (FirstSourcePreparationCommandResult, error)) {
	contract, found := credentialgen.GetAPIGenCommandRuntimeContract(operation)
	if !found || mutate == nil {
		writeFirstSourcePreparationError(w, r, credential.ErrUnavailable)
		return
	}
	executor, err := apigencommand.NewExecutor(credentialgen.GetAPIGenCommandRuntimeContract, nil)
	var result FirstSourcePreparationCommandResult
	if err == nil {
		err = apigencommand.ExecuteInvocation(r.Context(), executor, contract, apigencommand.Invocation{
			OperationID: operation, Surface: surface, TargetValues: map[string]string{"connection": connection},
			RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")), CorrelationID: strings.TrimSpace(r.Header.Get("X-Correlation-ID")),
		}, apigencommand.Execution{Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
			var mutationErr error
			result, mutationErr = mutate(ctx)
			return mutationErr
		}})
	}
	if err != nil {
		writeFirstSourcePreparationError(w, r, err)
		return
	}
	if !credentialActivationUUID(result.PreparationID) || platformdigest.ValidateSHA256Identity(result.IntentDigest) != nil || platformdigest.ValidateSHA256Identity(result.PlanRequestDigest) != nil || result.CreatedAt.IsZero() {
		writeFirstSourcePreparationError(w, r, credential.ErrUnavailable)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, credentialgen.FirstSourcePreparationResponse{
		PreparationId: result.PreparationID, IntentDigest: result.IntentDigest, PlanRequestDigest: result.PlanRequestDigest, CreatedAt: result.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}

func writeFirstSourcePreparationTooLarge(w http.ResponseWriter, r *http.Request) {
	apitransport.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "First-source preparation request is too large.", nil)
}

func writeFirstSourcePreparationError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, detail := http.StatusServiceUnavailable, "FIRST_SOURCE_PREPARATION_UNAVAILABLE", "First-source preparation is unavailable."
	switch {
	case errors.Is(err, credential.ErrInvalid):
		status, code, detail = http.StatusBadRequest, "INVALID_FIRST_SOURCE_PREPARATION", "First-source preparation request is invalid."
	case errors.Is(err, credential.ErrForbidden), errors.Is(err, access.ErrForbidden):
		status, code, detail = http.StatusForbidden, "FIRST_SOURCE_PREPARATION_FORBIDDEN", "First-source preparation is forbidden."
	case errors.Is(err, credential.ErrNotFound):
		status, code, detail = http.StatusNotFound, "FIRST_SOURCE_PREPARATION_NOT_FOUND", "First-source preparation evidence was not found."
	case errors.Is(err, credential.ErrConflict):
		status, code, detail = http.StatusConflict, "FIRST_SOURCE_PREPARATION_CONFLICT", "First-source preparation conflicts with stored state."
	}
	apitransport.WriteProblem(w, r, status, code, detail, nil)
}
