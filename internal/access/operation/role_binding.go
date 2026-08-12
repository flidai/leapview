package operation

import (
	"context"
	"fmt"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

type RepositoryProvider func() (access.Repository, error)

type RoleBindingCommands struct {
	repository RepositoryProvider
}

func NewRoleBindingCommands(repository RepositoryProvider) (*RoleBindingCommands, error) {
	return &RoleBindingCommands{repository: repository}, nil
}

func (c *RoleBindingCommands) CreateRoleBinding(ctx context.Context, invocation access.RoleBindingInvocation, input access.RoleBindingInput) (access.RoleBinding, error) {
	var row access.RoleBinding
	operationID := accessgen.GenCommandOperationCreateRoleBinding().APIGenOperationID()
	err := c.execute(ctx, operationID, invocation, func(executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return accessgen.ExecuteGenCreateRoleBindingCommand(ctx, executor, accessgen.GenCreateRoleBindingCommandInvocation{
			Surface: invocation.Surface, Workspace: input.WorkspaceID, IdempotencyKey: invocation.IdempotencyKey,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	}, func(repository access.Repository) error {
		var err error
		row, err = repository.CreateRoleBinding(ctx, input)
		return err
	}, nil, nil, func() access.RoleBinding { return row }, func(operationID string, invocation access.RoleBindingInvocation, row access.RoleBinding) (string, error) {
		return accessgen.EncodeGenCreateRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingCreatedAuditPayload{
			OperationId: operationID, Role: row.Role, SubjectId: row.SubjectID,
			SubjectType: string(row.SubjectType), Surface: string(invocation.Surface),
		})
	})
	return row, err
}

func (c *RoleBindingCommands) UpdateRoleBinding(ctx context.Context, invocation access.RoleBindingInvocation, workspaceID, bindingID string, input access.RoleBindingInput) (access.RoleBinding, error) {
	var row access.RoleBinding
	operationID := accessgen.GenCommandOperationUpdateRoleBinding().APIGenOperationID()
	err := c.execute(ctx, operationID, invocation, func(executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return accessgen.ExecuteGenUpdateRoleBindingCommand(ctx, executor, accessgen.GenUpdateRoleBindingCommandInvocation{
			Surface: invocation.Surface, Workspace: workspaceID, ConcurrencyToken: invocation.ConcurrencyToken,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	}, func(repository access.Repository) error {
		var err error
		row, err = repository.UpdateRoleBinding(ctx, workspaceID, bindingID, input)
		return err
	}, func(repository access.Repository) (string, error) {
		current, err := repository.GetRoleBinding(ctx, workspaceID, bindingID)
		if err != nil {
			return "", err
		}
		return access.RoleBindingRevision(current)
	}, func(ctx context.Context, executor *apigencommand.Executor, presented, current string) error {
		return accessgen.CheckGenUpdateRoleBindingCommandConcurrency(ctx, executor, presented, current)
	}, func() access.RoleBinding { return row }, roleBindingAuditEncoder(accessgen.EncodeGenUpdateRoleBindingAuditPayload))
	return row, err
}

func (c *RoleBindingCommands) DeleteRoleBinding(ctx context.Context, invocation access.RoleBindingInvocation, workspaceID, bindingID string) (access.RoleBinding, error) {
	var row access.RoleBinding
	operationID := accessgen.GenCommandOperationDeleteRoleBinding().APIGenOperationID()
	err := c.execute(ctx, operationID, invocation, func(executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return accessgen.ExecuteGenDeleteRoleBindingCommand(ctx, executor, accessgen.GenDeleteRoleBindingCommandInvocation{
			Surface: invocation.Surface, Workspace: workspaceID,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	}, func(repository access.Repository) error {
		var err error
		row, err = repository.GetRoleBinding(ctx, workspaceID, bindingID)
		if err != nil {
			return err
		}
		return repository.DeleteRoleBinding(ctx, workspaceID, bindingID)
	}, nil, nil, func() access.RoleBinding { return row }, roleBindingAuditEncoder(accessgen.EncodeGenDeleteRoleBindingAuditPayload))
	return row, err
}

func (c *RoleBindingCommands) execute(
	ctx context.Context,
	operationID string,
	invocation access.RoleBindingInvocation,
	executeGenerated func(*apigencommand.Executor, apigencommand.Execution) error,
	mutation func(access.Repository) error,
	currentRevision func(access.Repository) (string, error),
	checkConcurrency func(context.Context, *apigencommand.Executor, string, string) error,
	result func() access.RoleBinding,
	encodeAuditPayload func(string, access.RoleBindingInvocation, access.RoleBinding) (string, error),
) error {
	if invocation.Surface == access.OperationSurfaceUI {
		if err := uicommand.VerifyClaim(invocation.OperationClaims, operationID); err != nil {
			return err
		}
	}
	if c == nil || c.repository == nil {
		return fmt.Errorf("operation %q repository is required", operationID)
	}
	repository, err := c.repository()
	if err != nil {
		return err
	}
	if repository == nil {
		return fmt.Errorf("operation %q repository is required", operationID)
	}
	transactional, ok := repository.(access.AuditedMutationRepository)
	if !ok {
		return fmt.Errorf("%w: role binding repository does not support transactional auditing", access.ErrAuditTransaction)
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executeGenerated(executor, apigencommand.Execution{
		Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
			return transactional.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
				if contract.Concurrency == apigencommand.ConcurrencyIfMatch {
					if currentRevision == nil || checkConcurrency == nil {
						return access.AuditEventInput{}, fmt.Errorf("operation %q concurrency revision source is unavailable", operationID)
					}
					current, revisionErr := currentRevision(repository)
					if revisionErr != nil {
						return access.AuditEventInput{}, revisionErr
					}
					if revisionErr := checkConcurrency(ctx, executor, invocation.ConcurrencyToken, current); revisionErr != nil {
						return access.AuditEventInput{}, revisionErr
					}
				}
				if mutationErr := mutation(repository); mutationErr != nil {
					return access.AuditEventInput{}, mutationErr
				}
				return roleBindingAuditInput(contract, operationID, invocation, result(), encodeAuditPayload)
			})
		},
	})
}

func roleBindingAuditEncoder(encode func(accessgen.GenSchemaRoleBindingAuditPayload) (string, error)) func(string, access.RoleBindingInvocation, access.RoleBinding) (string, error) {
	return func(operationID string, invocation access.RoleBindingInvocation, row access.RoleBinding) (string, error) {
		return encode(accessgen.GenSchemaRoleBindingAuditPayload{
			OperationId: operationID, Role: row.Role, SubjectId: row.SubjectID,
			SubjectType: string(row.SubjectType), Surface: string(invocation.Surface),
		})
	}
}

func roleBindingAuditInput(contract apigencommand.Contract, operationID string, invocation access.RoleBindingInvocation, row access.RoleBinding, encodeAuditPayload func(string, access.RoleBindingInvocation, access.RoleBinding) (string, error)) (access.AuditEventInput, error) {
	metadata, err := encodeAuditPayload(operationID, invocation, row)
	if err != nil {
		return access.AuditEventInput{}, err
	}
	privilege, ok := access.ParsePrivilege(contract.Privilege)
	if !ok {
		return access.AuditEventInput{}, fmt.Errorf("generated operation %q has invalid privilege %q", operationID, contract.Privilege)
	}
	correlationID := strings.TrimSpace(invocation.CorrelationID)
	if correlationID == "" {
		correlationID = strings.TrimSpace(invocation.RequestID)
	}
	return access.AuditEventInput{
		WorkspaceID: row.WorkspaceID, PrincipalID: invocation.PrincipalID,
		Action: contract.AuditAction, TargetType: "role_binding", TargetID: row.ID,
		Privilege: privilege, Status: "success",
		RequestID: strings.TrimSpace(invocation.RequestID), CorrelationID: correlationID,
		MetadataJSON: metadata,
	}, nil
}
