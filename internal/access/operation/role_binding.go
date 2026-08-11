package operation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

type RepositoryProvider func() (access.Repository, error)

type RoleBindingCommands struct {
	repository RepositoryProvider
	catalog    access.OperationCatalog
}

func NewRoleBindingCommands(repository RepositoryProvider, catalog access.OperationCatalog) (*RoleBindingCommands, error) {
	for _, operationID := range []access.OperationID{
		access.OperationCreateRoleBinding,
		access.OperationUpdateRoleBinding,
		access.OperationDeleteRoleBinding,
	} {
		if _, ok := catalog.DescribeOperation(operationID); !ok {
			return nil, fmt.Errorf("role binding operation %q is required", operationID)
		}
	}
	return &RoleBindingCommands{repository: repository, catalog: catalog}, nil
}

func (c *RoleBindingCommands) DescribeOperation(operationID access.OperationID) (access.OperationDescriptor, bool) {
	if c == nil {
		return access.OperationDescriptor{}, false
	}
	return c.catalog.DescribeOperation(operationID)
}

func (c *RoleBindingCommands) CreateRoleBinding(ctx context.Context, invocation access.RoleBindingInvocation, input access.RoleBindingInput) (access.RoleBinding, error) {
	var row access.RoleBinding
	err := c.execute(ctx, access.OperationCreateRoleBinding, invocation, func(repository access.Repository) error {
		var err error
		row, err = repository.CreateRoleBinding(ctx, input)
		return err
	}, func() access.RoleBinding { return row })
	return row, err
}

func (c *RoleBindingCommands) UpdateRoleBinding(ctx context.Context, invocation access.RoleBindingInvocation, workspaceID, bindingID string, input access.RoleBindingInput) (access.RoleBinding, error) {
	var row access.RoleBinding
	err := c.execute(ctx, access.OperationUpdateRoleBinding, invocation, func(repository access.Repository) error {
		var err error
		row, err = repository.UpdateRoleBinding(ctx, workspaceID, bindingID, input)
		return err
	}, func() access.RoleBinding { return row })
	return row, err
}

func (c *RoleBindingCommands) DeleteRoleBinding(ctx context.Context, invocation access.RoleBindingInvocation, workspaceID, bindingID string) (access.RoleBinding, error) {
	var row access.RoleBinding
	err := c.execute(ctx, access.OperationDeleteRoleBinding, invocation, func(repository access.Repository) error {
		var err error
		row, err = repository.GetRoleBinding(ctx, workspaceID, bindingID)
		if err != nil {
			return err
		}
		return repository.DeleteRoleBinding(ctx, workspaceID, bindingID)
	}, func() access.RoleBinding { return row })
	return row, err
}

func (c *RoleBindingCommands) execute(
	ctx context.Context,
	operationID access.OperationID,
	invocation access.RoleBindingInvocation,
	mutation func(access.Repository) error,
	result func() access.RoleBinding,
) error {
	descriptor, ok := c.DescribeOperation(operationID)
	if !ok {
		return fmt.Errorf("unknown operation %q", operationID)
	}
	if !descriptor.Exposes(invocation.Surface) {
		return fmt.Errorf("operation %q is not exposed to surface %q", operationID, invocation.Surface)
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
	audited := func(repository access.Repository) (access.AuditEventInput, error) {
		if err := mutation(repository); err != nil {
			return access.AuditEventInput{}, err
		}
		return roleBindingAuditInput(descriptor, invocation, result()), nil
	}
	transactional, ok := repository.(access.AuditedMutationRepository)
	if !ok {
		return fmt.Errorf("%w: role binding repository does not support transactional auditing", access.ErrAuditTransaction)
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, string(operationID), apigencommand.Execution{
		Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
			return transactional.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
				input, mutationErr := audited(repository)
				if mutationErr == nil && input.Action != contract.AuditAction {
					return access.AuditEventInput{}, fmt.Errorf("generated audit action %q does not match mutation action %q", contract.AuditAction, input.Action)
				}
				return input, mutationErr
			})
		},
	})
}

func roleBindingAuditInput(descriptor access.OperationDescriptor, invocation access.RoleBindingInvocation, row access.RoleBinding) access.AuditEventInput {
	metadata, _ := json.Marshal(map[string]any{
		"operationId": string(descriptor.ID),
		"role":        row.Role,
		"subjectId":   row.SubjectID,
		"subjectType": string(row.SubjectType),
		"surface":     string(invocation.Surface),
	})
	correlationID := strings.TrimSpace(invocation.CorrelationID)
	if correlationID == "" {
		correlationID = strings.TrimSpace(invocation.RequestID)
	}
	return access.AuditEventInput{
		WorkspaceID: row.WorkspaceID, PrincipalID: invocation.PrincipalID,
		Action: descriptor.AuditEvent, TargetType: "role_binding", TargetID: row.ID,
		Privilege: descriptor.Privilege, Status: "success",
		RequestID: strings.TrimSpace(invocation.RequestID), CorrelationID: correlationID,
		MetadataJSON: string(metadata),
	}
}
