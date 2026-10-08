package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

func (d credentialDraftAPIGenDispatcher) StartCredentialActivation(w http.ResponseWriter, r *http.Request, project, target, connection, version string) {
	actor, ok := d.activationPrincipal(w, r)
	if !ok {
		return
	}
	body, ok := decodeCredentialActivationObject(w, r)
	if !ok {
		return
	}
	input := credential.ActivationRequest{VersionID: version}
	if len(body) != 3 || json.Unmarshal(body["operationId"], &input.OperationID) != nil ||
		json.Unmarshal(body["receiptId"], &input.ReceiptID) != nil ||
		json.Unmarshal(body["expectedBindingRevision"], &input.ExpectedBindingRevision) != nil || input.ExpectedBindingRevision < 1 || input.Validate() != nil {
		writeCredentialActivationInvalid(w, r)
		return
	}
	invocation := credentialgen.GenStartCredentialActivationCommandInvocation{
		Surface: apigencommand.SurfaceAPI, Connection: connection,
		RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")), CorrelationID: strings.TrimSpace(r.Header.Get("X-Correlation-ID")),
	}
	executeCredentialActivation(w, r, credentialgen.GenCommandOperationStartCredentialActivation(),
		func(ctx context.Context, executor *apigencommand.Executor, execution apigencommand.Execution) error {
			return credentialgen.ExecuteGenStartCredentialActivationCommand(ctx, executor, invocation, execution)
		}, func(ctx context.Context) (credential.ActivationStatus, error) {
			return d.config.Activation.StartActivation(ctx, actor, d.resource(project, target, connection), input)
		})
}

func (d credentialDraftAPIGenDispatcher) RetryCredentialActivation(w http.ResponseWriter, r *http.Request, project, target, connection, operation string) {
	actor, ok := d.activationPrincipal(w, r)
	if !ok {
		return
	}
	body, ok := decodeCredentialActivationObject(w, r)
	if !ok {
		return
	}
	receipt := ""
	if len(body) > 1 || !credentialActivationUUID(operation) {
		writeCredentialActivationInvalid(w, r)
		return
	}
	if len(body) == 1 && (json.Unmarshal(body["receiptId"], &receipt) != nil || !credentialActivationUUID(receipt)) {
		writeCredentialActivationInvalid(w, r)
		return
	}
	invocation := credentialgen.GenRetryCredentialActivationCommandInvocation{
		Surface: apigencommand.SurfaceAPI, Connection: connection,
		RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")), CorrelationID: strings.TrimSpace(r.Header.Get("X-Correlation-ID")),
	}
	executeCredentialActivation(w, r, credentialgen.GenCommandOperationRetryCredentialActivation(),
		func(ctx context.Context, executor *apigencommand.Executor, execution apigencommand.Execution) error {
			return credentialgen.ExecuteGenRetryCredentialActivationCommand(ctx, executor, invocation, execution)
		}, func(ctx context.Context) (credential.ActivationStatus, error) {
			return d.config.Activation.RetryActivation(ctx, actor, d.resource(project, target, connection), operation, receipt)
		})
}

func (d credentialDraftAPIGenDispatcher) AbortCredentialActivation(w http.ResponseWriter, r *http.Request, project, target, connection, operation string) {
	actor, ok := d.activationPrincipal(w, r)
	if !ok {
		return
	}
	body, ok := decodeCredentialActivationObject(w, r)
	if !ok {
		return
	}
	if len(body) != 0 || !credentialActivationUUID(operation) {
		writeCredentialActivationInvalid(w, r)
		return
	}
	invocation := credentialgen.GenAbortCredentialActivationCommandInvocation{
		Surface: apigencommand.SurfaceAPI, Connection: connection,
		RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")), CorrelationID: strings.TrimSpace(r.Header.Get("X-Correlation-ID")),
	}
	executeCredentialActivation(w, r, credentialgen.GenCommandOperationAbortCredentialActivation(),
		func(ctx context.Context, executor *apigencommand.Executor, execution apigencommand.Execution) error {
			return credentialgen.ExecuteGenAbortCredentialActivationCommand(ctx, executor, invocation, execution)
		}, func(ctx context.Context) (credential.ActivationStatus, error) {
			return d.config.Activation.AbortActivation(ctx, actor, d.resource(project, target, connection), operation)
		})
}

func (d credentialDraftAPIGenDispatcher) GetCredentialActivation(w http.ResponseWriter, r *http.Request, project, target, connection, operation string) {
	actor, ok := d.activationPrincipal(w, r)
	if !ok {
		return
	}
	if !credentialActivationUUID(operation) {
		writeCredentialActivationInvalid(w, r)
		return
	}
	status, err := d.config.Activation.GetActivation(r.Context(), actor, d.resource(project, target, connection), operation)
	if err != nil {
		code, name := credentialActivationFailure(err)
		apitransport.WriteProblem(w, r, code, name, "Credential activation is unavailable for this request.", nil)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, credentialActivationResponse(status))
}

func (d credentialDraftAPIGenDispatcher) activationPrincipal(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := d.principal(w, r)
	if !ok {
		return "", false
	}
	if typednil.IsNil(d.config.Activation) {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "CREDENTIAL_ACTIVATION_UNAVAILABLE", "Credential activation service is unavailable.", nil)
		return "", false
	}
	return actor, true
}

func decodeCredentialActivationObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	const maxBytes int64 = 2048
	if r.ContentLength > maxBytes {
		apitransport.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Credential activation request is too large.", nil)
		return nil, false
	}
	// The retained operation UUID, rather than a second transport journal, owns
	// explicit recovery after a lost response.
	if r.Header.Get("Idempotency-Key") != "" || r.Body == nil || r.Body == http.NoBody {
		writeCredentialActivationInvalid(w, r)
		return nil, false
	}
	var body map[string]json.RawMessage
	err := strictjson.DecodeReader(r.Body, &body, strictjson.Options{MaxBytes: maxBytes, MaxDepth: 2})
	if errors.Is(err, strictjson.ErrSizeLimit) {
		apitransport.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Credential activation request is too large.", nil)
		return nil, false
	}
	if err != nil || body == nil {
		writeCredentialActivationInvalid(w, r)
		return nil, false
	}
	return body, true
}

func writeCredentialActivationInvalid(w http.ResponseWriter, r *http.Request) {
	apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CREDENTIAL_ACTIVATION", "Credential activation request is invalid.", nil)
}

func credentialActivationUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func executeCredentialActivation(w http.ResponseWriter, r *http.Request, operation credentialgen.GenCommandOperationID,
	execute func(context.Context, *apigencommand.Executor, apigencommand.Execution) error,
	mutate func(context.Context) (credential.ActivationStatus, error),
) {
	executor, err := apigencommand.NewExecutor(credentialgen.GetAPIGenCommandRuntimeContract, nil)
	var status credential.ActivationStatus
	if err == nil {
		err = execute(r.Context(), executor, apigencommand.Execution{Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
			// The service commits each durable command outcome and its audit in
			// the same transaction; runtime preparation/drain remains service-owned.
			var serviceErr error
			status, serviceErr = mutate(ctx)
			return serviceErr
		}})
	}
	if err != nil {
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
		}
		apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil, operation, credentialgen.GetAPIGenCommandFailureContracts,
			apigenfailure.New(kind, "credential activation failed"))
		return
	}
	code := http.StatusAccepted
	if status.State == "completed" || status.State == "aborted" {
		code = http.StatusOK
	}
	apitransport.WriteJSON(w, code, credentialActivationResponse(status))
}

func credentialActivationResponse(status credential.ActivationStatus) credentialgen.CredentialActivationResponse {
	optional := func(value string) *string {
		if value == "" {
			return nil
		}
		return &value
	}
	return credentialgen.CredentialActivationResponse{
		OperationId: status.OperationID, VersionId: status.VersionID, State: status.State,
		BindingRevision: status.BindingRevision, RuntimeReady: status.RuntimeReady,
		CreatedAt: status.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: status.UpdatedAt.UTC().Format(time.RFC3339Nano),
		CandidateId: optional(status.CandidateID), GenerationId: optional(status.GenerationID), PublicationId: optional(status.PublicationID),
	}
}

func credentialActivationFailure(err error) (int, string) {
	switch {
	case errors.Is(err, credential.ErrInvalid):
		return http.StatusBadRequest, "INVALID_CREDENTIAL_ACTIVATION"
	case errors.Is(err, credential.ErrForbidden):
		return http.StatusForbidden, "CREDENTIAL_ACTIVATION_FORBIDDEN"
	case errors.Is(err, credential.ErrNotFound):
		return http.StatusNotFound, "CREDENTIAL_ACTIVATION_NOT_FOUND"
	case errors.Is(err, credential.ErrConflict):
		return http.StatusConflict, "CREDENTIAL_ACTIVATION_CONFLICT"
	default:
		return http.StatusServiceUnavailable, "CREDENTIAL_ACTIVATION_UNAVAILABLE"
	}
}
