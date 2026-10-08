package credentialagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ credential.ActivationAuthority = (*AgentCredentials)(nil)
var _ credential.ActivationRuntime = (*AgentCredentials)(nil)
var _ agentmodule.ConfigurationCredentials = (*AgentCredentials)(nil)

func (a *AgentCredentials) exactRequest(ctx context.Context, actor string, resource credential.Resource, id string) (credential.ActivationRequestRecord, error) {
	if err := a.AuthorizeMutation(ctx, actor, resource); err != nil {
		return credential.ActivationRequestRecord{}, err
	}
	record, err := a.repository.GetActivationRequest(ctx, a.config.InstanceID, id)
	if err != nil {
		return record, err
	}
	if record.Resource() != resource || record.Receipt.ActorID != actor {
		return record, credential.ErrNotFound
	}
	return record, nil
}
func (a *AgentCredentials) Read(ctx context.Context, actor string, resource credential.Resource, id string) (credential.ActivationRecord, error) {
	record, err := a.exactRequest(ctx, actor, resource, id)
	return record.ActivationRecord(), err
}
func (a *AgentCredentials) Pending(ctx context.Context) (credential.ActivationRecord, error) {
	record, err := a.repository.GetPendingActivationRequest(ctx, a.config.InstanceID)
	if err == nil && record.Resource() != a.resource() {
		return credential.ActivationRecord{}, credential.ErrNotFound
	}
	return record.ActivationRecord(), err
}

func (a *AgentCredentials) candidate(ctx context.Context, tx pgx.Tx, receipt credential.ValidationReceipt) (agentmodule.ConfigurationCandidate, error) {
	if receipt.Binding.DeploymentID != a.config.InstanceID || receipt.Binding.ScopeKind != "agent" || receipt.Binding.ResourceID != a.config.InstanceID {
		return agentmodule.ConfigurationCandidate{}, credential.ErrInvalid
	}
	var candidate agentmodule.ConfigurationCandidate
	var owner string
	var err error
	if tx == nil {
		candidate, err = a.config.Store.ConfigurationCandidate(ctx, receipt.BindingID)
	} else {
		candidate, err = a.config.Store.ConfigurationCandidateTx(ctx, tx, receipt.BindingID)
	}
	if err != nil {
		return candidate, credential.ErrConflict
	}
	if tx == nil {
		owner, err = a.config.CustomerOwner.CustomerOwner(ctx)
	} else {
		owner, err = a.config.CustomerOwnerTx(ctx, tx)
	}
	if err != nil {
		return candidate, credential.ErrUnavailable
	}
	scope, err := a.ownedScope(owner, candidate.ConfigurationRevision, receipt.Binding.Provider)
	if err != nil || scope.OwnerID != receipt.Binding.OwnerID || scope.Purpose != receipt.Binding.Purpose || scope.Destination != receipt.Binding.Destination ||
		scope.Destination != receipt.ConfigurationDigest || candidate.ActorID != receipt.ActorID || candidate.ExpectedRevision != receipt.BindingRevision {
		return candidate, credential.ErrConflict
	}
	return candidate, nil
}

func (a *AgentCredentials) authorizeTx(record credential.ActivationRequestRecord, actor string, committed bool) credentialpostgres.ActivationRequestAuthorizer {
	return func(ctx context.Context, tx pgx.Tx) error {
		if err := a.config.LockFence(ctx, tx); err != nil {
			return err
		}
		pair, err := access.NewInstancePermissionPair(access.ActionPlatformSettingsUpdate, a.config.InstanceID)
		if err != nil || a.config.AuthorizeTx(ctx, tx, actor, pair) != nil {
			return credential.ErrForbidden
		}
		candidate, err := a.candidate(ctx, tx, record.Receipt)
		if err != nil {
			return err
		}
		current, err := a.config.Store.LockConfigurationTx(ctx, tx)
		if err != nil {
			return err
		}
		expected := candidate.ExpectedRevision
		if committed {
			expected++
		}
		if current.Revision != expected {
			return credential.ErrConflict
		}
		if committed && (current.CredentialVersionID != record.Request.VersionID || agentConfigurationDigest(current) != record.Receipt.ConfigurationDigest) {
			return credential.ErrConflict
		}
		return nil
	}
}
func (a *AgentCredentials) transaction(ctx context.Context, run func(pgx.Tx) error) error {
	tx, err := a.config.Pool.Begin(ctx)
	if err != nil {
		return credential.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	if err = run(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *AgentCredentials) Prepare(ctx context.Context, actor string, resource credential.Resource, request credential.ActivationRequest) (credential.ActivationRecord, error) {
	if err := a.AuthorizeMutation(ctx, actor, resource); err != nil {
		return credential.ActivationRecord{}, err
	}
	if old, err := a.repository.GetActivationRequest(ctx, a.config.InstanceID, request.OperationID); err == nil {
		if old.Resource() != resource || old.Request != request || old.Receipt.ActorID != actor {
			return credential.ActivationRecord{}, credential.ErrConflict
		}
		return old.ActivationRecord(), nil
	} else if !errors.Is(err, credential.ErrNotFound) {
		return credential.ActivationRecord{}, err
	}
	receipt, err := a.repository.ReadValidationReceipt(ctx, a.config.InstanceID, request.ReceiptID)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	if receipt.ActorID != actor {
		return credential.ActivationRecord{}, credential.ErrForbidden
	}
	if _, err := a.candidate(ctx, nil, receipt); err != nil {
		return credential.ActivationRecord{}, err
	}
	seed := credential.ActivationRequestRecord{Request: request, Receipt: receipt}
	var result credential.ActivationRequestRecord
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		before, err := a.repository.ReserveActivationRequestTx(ctx, tx, receipt, request, a.authorizeTx(seed, actor, false))
		if err != nil {
			return err
		}
		next := before
		next.State = "prepared"
		next.ConfigurationRevision = request.ExpectedBindingRevision + 1
		result, err = a.repository.TransitionActivationRequestTx(ctx, tx, before, next, actor, "", a.authorizeTx(before, actor, false))
		if err != nil {
			return err
		}
		return a.phaseAudit(ctx, tx, result, "prepared")
	})
	return result.ActivationRecord(), err
}

func (a *AgentCredentials) Switch(ctx context.Context, actor string, record credential.ActivationRecord, receiptID string) (credential.ActivationRecord, error) {
	before, err := a.exactRequest(ctx, actor, record.Resource, record.Request.OperationID)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	if before.State != "prepared" && before.State != "switching" {
		return credential.ActivationRecord{}, credential.ErrConflict
	}
	var result credential.ActivationRequestRecord
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		auth := a.authorizeTx(before, actor, false)
		if _, err := a.repository.RefreshActivationRequestReceiptTx(ctx, tx, before, receiptID, actor, auth); err != nil {
			return err
		}
		if err := a.repository.CheckActivationReceiptFreshTx(ctx, tx, a.config.InstanceID, before.Request.OperationID); err != nil {
			return err
		}
		next := before
		next.State = "switching"
		var err error
		result, err = a.repository.TransitionActivationRequestTx(ctx, tx, before, next, actor, "credential.activation.retried", auth)
		if err != nil {
			return err
		}
		return a.phaseAudit(ctx, tx, result, "switching")
	})
	return result.ActivationRecord(), err
}
func (a *AgentCredentials) Commit(ctx context.Context, actor string, record credential.ActivationRecord) (credential.ActivationRecord, error) {
	before, err := a.exactRequest(ctx, actor, record.Resource, record.Request.OperationID)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	if before.State != "switching" {
		return credential.ActivationRecord{}, credential.ErrConflict
	}
	var result credential.ActivationRequestRecord
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		auth := a.authorizeTx(before, actor, false)
		if err := auth(ctx, tx); err != nil {
			return err
		}
		if err := a.repository.CheckActivationReceiptFreshTx(ctx, tx, a.config.InstanceID, before.Request.OperationID); err != nil {
			return err
		}
		candidate, err := a.candidate(ctx, tx, before.Receipt)
		if err != nil {
			return err
		}
		config := candidate.ConfigurationRevision
		config.CredentialVersionID = before.Request.VersionID
		saved, err := a.config.Store.SaveConfigurationTx(ctx, tx, candidate.ExpectedRevision, config)
		if err != nil {
			return err
		}
		if saved.Revision != before.ConfigurationRevision {
			return credential.ErrConflict
		}
		next := before
		next.State = "committed"
		result, err = a.repository.TransitionActivationRequestTx(ctx, tx, before, next, actor, "", a.authorizeTx(before, actor, true))
		if err != nil {
			return err
		}
		return a.phaseAudit(ctx, tx, result, "committed")
	})
	return result.ActivationRecord(), err
}
func (a *AgentCredentials) Complete(ctx context.Context, record credential.ActivationRecord) (credential.ActivationRecord, error) {
	before, err := a.repository.GetActivationRequest(ctx, a.config.InstanceID, record.Request.OperationID)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	if before.Resource() != a.resource() || before.State != "committed" {
		return credential.ActivationRecord{}, credential.ErrConflict
	}
	var result credential.ActivationRequestRecord
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		// Startup completion is system reconciliation of committed authority;
		// it must not depend on the original actor still retaining a role.
		auth := func(ctx context.Context, tx pgx.Tx) error {
			if err := a.config.LockFence(ctx, tx); err != nil {
				return err
			}
			current, err := a.config.Store.LockConfigurationTx(ctx, tx)
			if err != nil {
				return err
			}
			if current.Revision != before.ConfigurationRevision || current.CredentialVersionID != before.Request.VersionID || agentConfigurationDigest(current) != before.Receipt.ConfigurationDigest {
				return credential.ErrConflict
			}
			_, err = a.candidate(ctx, tx, before.Receipt)
			return err
		}
		next := before
		next.State = "completed"
		var err error
		result, err = a.repository.TransitionActivationRequestTx(ctx, tx, before, next, before.Receipt.ActorID, "", auth)
		if err != nil {
			return err
		}
		return a.phaseAudit(ctx, tx, result, "completed")
	})
	return result.ActivationRecord(), err
}
func (a *AgentCredentials) Abort(ctx context.Context, actor string, record credential.ActivationRecord) (credential.ActivationRecord, error) {
	before, err := a.exactRequest(ctx, actor, record.Resource, record.Request.OperationID)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	if before.State == "committed" || before.State == "completed" || before.State == "aborted" {
		return credential.ActivationRecord{}, credential.ErrConflict
	}
	var result credential.ActivationRequestRecord
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		next := before
		next.State = "aborted"
		var err error
		result, err = a.repository.TransitionActivationRequestTx(ctx, tx, before, next, actor, "credential.activation.aborted", a.authorizeTx(before, actor, false))
		return err
	})
	return result.ActivationRecord(), err
}
func (a *AgentCredentials) InstallCommitted(ctx context.Context, record credential.ActivationRecord) error {
	if a.install == nil {
		return credential.ErrUnavailable
	}
	saved, err := a.repository.GetActivationRequest(ctx, a.config.InstanceID, record.Request.OperationID)
	if err != nil || saved.Resource() != a.resource() || saved.Request != record.Request || (saved.State != "committed" && saved.State != "completed") {
		return credential.ErrConflict
	}
	return a.install(ctx, saved.ConfigurationRevision)
}
func (a *AgentCredentials) RestoreCurrent(ctx context.Context) error {
	if a.restore == nil {
		return credential.ErrUnavailable
	}
	return a.restore(ctx)
}
func (a *AgentCredentials) phaseAudit(ctx context.Context, tx pgx.Tx, record credential.ActivationRequestRecord, phase string) error {
	metadata, _ := json.Marshal(struct {
		OperationID           string `json:"operation_id"`
		VersionID             string `json:"version_id"`
		State                 string `json:"state"`
		ConfigurationRevision int64  `json:"configuration_revision"`
	}{record.Request.OperationID, record.Request.VersionID, phase, record.ConfigurationRevision})
	principal := ""
	if id, err := uuid.Parse(record.Receipt.ActorID); err == nil && id.String() == record.Receipt.ActorID {
		principal = record.Receipt.ActorID
	}
	intent, err := (access.AuditIntent{EventID: uuid.NewString(), DomainEventID: uuid.NewString(), ScopeID: a.config.InstanceID, ActorID: record.Receipt.ActorID, PrincipalID: principal,
		Source: "credential", Operation: fmt.Sprintf("%sAgentCredentialActivation", phase), Action: "credential.activation." + phase, ResourceKind: "instance", ResourceID: a.config.InstanceID, Outcome: "success",
		AggregateKey: "agent-credential-activation:" + record.Request.OperationID, AggregateSequence: record.Revision, MetadataJSON: string(metadata)}).Canonicalize()
	if err != nil {
		return err
	}
	return a.config.RecordAudit(ctx, tx, intent)
}
