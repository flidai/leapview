package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
)

const maxProjectClaimBootstrapBody = 16 << 10

// ProjectClaimBootstrapInput is the fully validated, canonical command input
// passed to the transactional bootstrap seam. The seam is intentionally
// narrow so direct endpoint tests can use a fake while production keeps the
// PostgreSQL repository and Access audit adapter as the authorities.
type ProjectClaimBootstrapInput struct {
	PrincipalID, ProjectUID, IssuerID, Environment string
	IdempotencyKey, RequestDigest                  string
}

type ProjectClaimBootstrapResult struct {
	Claim      deployment.ProjectClaim
	Conflict   bool
	AuditID    string
	AuditInput ProjectClaimAuditInput
}

type ProjectClaimBootstrapFunc func(context.Context, ProjectClaimBootstrapInput) (ProjectClaimBootstrapResult, error)

func (m *Module) BootstrapProjectClaim(w http.ResponseWriter, r *http.Request, idempotencyKey string) {
	operationID := deploymentgen.GenCommandOperationBootstrapProjectClaim()
	if m == nil {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.New("project_claim_unavailable", "project claim bootstrap is unavailable"))
		return
	}
	principal, ok := m.principal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	if m.projectClaimBootstrap == nil && (m.persistence == nil || m.persistence.Repository == nil || m.projectClaimAudit == nil) {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.New("project_claim_unavailable", "project claim bootstrap is unavailable"))
		return
	}
	request, err := decodeProjectClaimBootstrapRequest(w, r)
	if err != nil {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.Wrap("project_claim_invalid", err))
		return
	}
	projectUID, err := projectgraph.NewResourceID(request.ProjectUid)
	if err != nil || projectUID.String() != request.ProjectUid {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.New("project_claim_invalid", "a canonical ProjectUID is required"))
		return
	}
	if issuerID, issuerErr := projectgraph.NewResourceID(request.IssuerId); issuerErr != nil || issuerID.String() != request.IssuerId {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.New("project_claim_invalid", "a canonical issuer identity is required"))
		return
	}
	if err := servingstate.ValidateEnvironment(servingstate.Environment(request.Environment)); err != nil {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.New("project_claim_invalid", "a canonical environment is required"))
		return
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.New("project_claim_invalid", "Idempotency-Key is required"))
		return
	}
	requestDigest, err := projectClaimRequestDigest(request)
	if err != nil {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.Wrap("project_claim_invalid", err))
		return
	}
	input := ProjectClaimBootstrapInput{
		PrincipalID: principal.ID, ProjectUID: projectUID.String(), IssuerID: request.IssuerId,
		Environment: request.Environment, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest,
	}
	var result ProjectClaimBootstrapResult
	if m.projectClaimBootstrap != nil {
		result, err = m.projectClaimBootstrap(r.Context(), input)
	} else {
		result, err = m.executeProjectClaimBootstrapCommand(r.Context(), input)
	}
	if err != nil {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, classifyProjectClaimBootstrapError(err))
		return
	}
	if !result.Conflict && result.Claim.ProjectID == "" {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.New("project_claim_unavailable", "project claim bootstrap returned no claim"))
		return
	}
	if result.Conflict {
		m.writeProjectClaimBootstrapFailure(w, r, operationID, apigenfailure.New("project_claim_conflict", "this instance is already claimed by a different Project or environment"))
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, deploymentgen.ProjectClaimBootstrapResponse{
		ProjectUid: result.Claim.ProjectID.String(), Environment: string(result.Claim.Environment),
		ClaimedBy: result.Claim.ClaimedBy, ClaimedAt: result.Claim.ClaimedAt.UTC().Format(time.RFC3339Nano),
	})
}

func classifyProjectClaimBootstrapError(err error) error {
	switch {
	case errors.Is(err, deployment.ErrProjectClaimConflict):
		return apigenfailure.Wrap("project_claim_conflict", err)
	case errors.Is(err, deploymentpostgres.ErrConflict):
		return apigenfailure.Wrap("project_claim_conflict", err)
	case errors.Is(err, deployment.ErrProjectClaimInvalid):
		return apigenfailure.Wrap("project_claim_invalid", err)
	case errors.Is(err, ErrProjectClaimAuditNotFound):
		return apigenfailure.Wrap("project_claim_unavailable", err)
	default:
		return apigenfailure.Wrap("project_claim_unavailable", err)
	}
}

func (m *Module) bootstrapProjectClaimTransaction(ctx context.Context, input ProjectClaimBootstrapInput) (ProjectClaimBootstrapResult, error) {
	// Idempotency is scoped to the authenticated principal as well as this
	// instance and operation. A credential cannot replay another principal's
	// audit identity or turn a request-digest mismatch into a server error.
	auditID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/project-claim/"+m.instanceID+"/"+input.PrincipalID+"/"+input.IdempotencyKey)).String()
	tx, err := m.persistence.Repository.Begin(ctx)
	if err != nil {
		return ProjectClaimBootstrapResult{}, err
	}
	defer tx.Rollback(ctx)

	// Audit rows are the durable idempotency record. Read before attempting the
	// singleton claim so exact retries do not produce a second audit insert.
	for _, result := range []string{"claimed", "conflict"} {
		outcome := "success"
		if result == "conflict" {
			outcome = "failure"
		}
		metadata, encodeErr := deploymentgen.EncodeGenBootstrapProjectClaimAuditPayload(deploymentgen.GenSchemaProjectClaimBootstrappedAuditPayload{
			ProjectUid: input.ProjectUID, IssuerId: input.IssuerID, Environment: input.Environment, Result: result,
		})
		if encodeErr != nil {
			return ProjectClaimBootstrapResult{}, encodeErr
		}
		auditInput := ProjectClaimAuditInput{AuditID: auditID, ScopeID: m.instanceID, ActorID: input.PrincipalID, ProjectUID: input.ProjectUID, RequestDigest: input.RequestDigest, AggregateKey: "project-claim:" + m.instanceID, Outcome: outcome, MetadataJSON: metadata}
		if _, readErr := m.projectClaimAudit.GetProjectClaimAudit(ctx, tx, auditInput); readErr == nil {
			claim, claimErr := m.persistence.Repository.GetProjectClaimTx(ctx, tx)
			if errors.Is(claimErr, deployment.ErrProjectClaimNotFound) && result == "conflict" {
				return ProjectClaimBootstrapResult{Conflict: true, AuditID: auditID, AuditInput: auditInput}, tx.Commit(ctx)
			}
			if claimErr != nil {
				return ProjectClaimBootstrapResult{}, claimErr
			}
			return ProjectClaimBootstrapResult{Claim: claim, Conflict: result == "conflict", AuditID: auditID, AuditInput: auditInput}, tx.Commit(ctx)
		} else if !errors.Is(readErr, ErrProjectClaimAuditNotFound) {
			return ProjectClaimBootstrapResult{}, readErr
		}
	}
	if input.Environment != string(m.instanceEnvironment) {
		metadata, err := deploymentgen.EncodeGenBootstrapProjectClaimAuditPayload(deploymentgen.GenSchemaProjectClaimBootstrappedAuditPayload{
			ProjectUid: input.ProjectUID, IssuerId: input.IssuerID, Environment: input.Environment, Result: "conflict",
		})
		if err != nil {
			return ProjectClaimBootstrapResult{}, err
		}
		auditInput := ProjectClaimAuditInput{AuditID: auditID, ScopeID: m.instanceID, ActorID: input.PrincipalID, ProjectUID: input.ProjectUID, RequestDigest: input.RequestDigest, AggregateKey: "project-claim:" + m.instanceID, Outcome: "failure", MetadataJSON: metadata}
		if _, err := m.projectClaimAudit.AppendProjectClaimAudit(ctx, tx, auditInput); err != nil {
			return ProjectClaimBootstrapResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ProjectClaimBootstrapResult{}, err
		}
		return ProjectClaimBootstrapResult{Conflict: true, AuditID: auditID, AuditInput: auditInput}, nil
	}

	projectID, err := projectgraph.NewResourceID(input.ProjectUID)
	if err != nil {
		return ProjectClaimBootstrapResult{}, fmt.Errorf("%w: %v", deployment.ErrProjectClaimInvalid, err)
	}
	claim, claimErr := m.persistence.Repository.ClaimProjectTx(ctx, tx, deployment.ProjectClaimInput{
		ProjectID: projectID, Environment: servingstate.Environment(input.Environment), ClaimedBy: input.PrincipalID, ClaimedAt: time.Now().UTC(),
	})
	conflict := errors.Is(claimErr, deployment.ErrProjectClaimConflict)
	if claimErr != nil && !conflict {
		return ProjectClaimBootstrapResult{}, claimErr
	}
	if conflict {
		claim, err = m.persistence.Repository.GetProjectClaimTx(ctx, tx)
		if err != nil {
			return ProjectClaimBootstrapResult{}, err
		}
	}
	resultName, outcome := "claimed", "success"
	if conflict {
		resultName, outcome = "conflict", "failure"
	}
	metadata, err := deploymentgen.EncodeGenBootstrapProjectClaimAuditPayload(deploymentgen.GenSchemaProjectClaimBootstrappedAuditPayload{
		ProjectUid: input.ProjectUID, IssuerId: input.IssuerID, Environment: input.Environment, Result: resultName,
	})
	if err != nil {
		return ProjectClaimBootstrapResult{}, err
	}
	auditInput := ProjectClaimAuditInput{AuditID: auditID, ScopeID: m.instanceID, ActorID: input.PrincipalID, ProjectUID: input.ProjectUID, RequestDigest: input.RequestDigest, AggregateKey: "project-claim:" + m.instanceID, Outcome: outcome, MetadataJSON: metadata}
	if _, err := m.projectClaimAudit.AppendProjectClaimAudit(ctx, tx, auditInput); err != nil {
		return ProjectClaimBootstrapResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectClaimBootstrapResult{}, err
	}
	return ProjectClaimBootstrapResult{Claim: claim, Conflict: conflict, AuditID: auditID, AuditInput: auditInput}, nil
}

func (m *Module) executeProjectClaimBootstrapCommand(ctx context.Context, input ProjectClaimBootstrapInput) (ProjectClaimBootstrapResult, error) {
	activeOperation, generated := apigencommand.OperationID(ctx)
	if !generated {
		return m.bootstrapProjectClaimTransaction(ctx, input)
	}
	operationID := deploymentgen.GenCommandOperationBootstrapProjectClaim().APIGenOperationID()
	if activeOperation != operationID {
		return ProjectClaimBootstrapResult{}, fmt.Errorf("%w: active %q, executing %q", apigencommand.ErrOperationMismatch, activeOperation, operationID)
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, m.logger)
	if err != nil {
		return ProjectClaimBootstrapResult{}, err
	}
	// The generated transactional guarantee wraps the actual claim and audit
	// mutation. It must complete before the handler sends a success response;
	// there is no post-commit verification transaction that can turn a durable
	// success into a spurious 503.
	var executed ProjectClaimBootstrapResult
	err = deploymentgen.ExecuteGenBootstrapProjectClaimCommand(ctx, executor, deploymentgen.GenBootstrapProjectClaimCommandInvocation{}, apigencommand.Execution{
		Transactional: func(verifyCtx context.Context, contract apigencommand.Contract) error {
			if contract.Guarantee != apigencommand.GuaranteeTransactional || contract.AuditAction != "project.claim.bootstrapped" {
				return fmt.Errorf("%w: project claim bootstrap contract differs", apigencommand.ErrInvalidContract)
			}
			var executionErr error
			executed, executionErr = m.bootstrapProjectClaimTransaction(verifyCtx, input)
			return executionErr
		},
	})
	return executed, err
}

func (m *Module) writeProjectClaimBootstrapFailure(w http.ResponseWriter, r *http.Request, operationID deploymentgen.GenCommandOperationID, err error) {
	if m == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "PROJECT_CLAIM_UNAVAILABLE", "Project claim bootstrap is temporarily unavailable.", nil)
		return
	}
	m.writeCommandFailure(w, r, operationID, err)
}

func decodeProjectClaimBootstrapRequest(w http.ResponseWriter, r *http.Request) (deploymentgen.ProjectClaimBootstrapRequest, error) {
	var request deploymentgen.ProjectClaimBootstrapRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxProjectClaimBootstrapBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("decode project claim bootstrap: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, errors.New("decode project claim bootstrap: multiple or malformed JSON values")
	}
	return request, nil
}

func projectClaimRequestDigest(request deploymentgen.ProjectClaimBootstrapRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
