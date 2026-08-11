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

type GrantCommands struct {
	repository RepositoryProvider
	catalog    access.OperationCatalog
}

func NewGrantCommands(repository RepositoryProvider, catalog access.OperationCatalog) (*GrantCommands, error) {
	for _, operationID := range []access.OperationID{
		access.OperationCreateGrant,
		access.OperationUpdateGrant,
		access.OperationDeleteGrant,
	} {
		if _, ok := catalog.DescribeOperation(operationID); !ok {
			return nil, fmt.Errorf("grant operation %q is required", operationID)
		}
	}
	return &GrantCommands{repository: repository, catalog: catalog}, nil
}

func (c *GrantCommands) DescribeOperation(operationID access.OperationID) (access.OperationDescriptor, bool) {
	if c == nil {
		return access.OperationDescriptor{}, false
	}
	return c.catalog.DescribeOperation(operationID)
}

func (c *GrantCommands) CreateGrant(ctx context.Context, invocation access.GrantInvocation, input access.GrantInput) (access.Grant, error) {
	var row access.Grant
	err := c.execute(ctx, access.OperationCreateGrant, invocation, func(repository access.Repository) error {
		var err error
		row, err = repository.CreateGrant(ctx, input)
		return err
	}, func() access.Grant { return row })
	return row, err
}

func (c *GrantCommands) UpdateGrant(ctx context.Context, invocation access.GrantInvocation, workspaceID, grantID string, input access.GrantInput) (access.Grant, error) {
	var row access.Grant
	err := c.execute(ctx, access.OperationUpdateGrant, invocation, func(repository access.Repository) error {
		updater, ok := repository.(interface {
			UpdateGrant(context.Context, string, string, access.GrantInput) (access.Grant, error)
		})
		if !ok {
			return fmt.Errorf("grant updates are unavailable")
		}
		var err error
		row, err = updater.UpdateGrant(ctx, workspaceID, grantID, input)
		return err
	}, func() access.Grant { return row })
	return row, err
}

func (c *GrantCommands) DeleteGrant(ctx context.Context, invocation access.GrantInvocation, workspaceID, grantID string) (access.Grant, error) {
	var row access.Grant
	err := c.execute(ctx, access.OperationDeleteGrant, invocation, func(repository access.Repository) error {
		var err error
		row, err = repository.GetGrant(ctx, workspaceID, grantID)
		if err != nil {
			return err
		}
		return repository.DeleteGrant(ctx, workspaceID, grantID)
	}, func() access.Grant { return row })
	return row, err
}

func (c *GrantCommands) execute(
	ctx context.Context,
	operationID access.OperationID,
	invocation access.GrantInvocation,
	mutation func(access.Repository) error,
	result func() access.Grant,
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
		return grantAuditInput(descriptor, invocation, result()), nil
	}
	transactional, ok := repository.(access.AuditedMutationRepository)
	if !ok {
		return fmt.Errorf("%w: grant repository does not support transactional auditing", access.ErrAuditTransaction)
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

func grantAuditInput(descriptor access.OperationDescriptor, invocation access.GrantInvocation, row access.Grant) access.AuditEventInput {
	metadataValues := map[string]string{
		"operationId": string(descriptor.ID),
		"objectId":    row.ObjectID,
		"objectType":  string(row.ObjectType),
		"subjectId":   row.SubjectID,
		"subjectType": string(row.SubjectType),
		"privilege":   string(row.Privilege),
		"surface":     string(invocation.Surface),
	}
	workspaceID := row.WorkspaceID
	if row.ObjectType == access.SecurableProjectEnvironment {
		workspaceID = ""
		metadataValues["projectId"] = row.WorkspaceID
		metadataValues["environment"] = row.ObjectID
	}
	metadata, _ := json.Marshal(metadataValues)
	correlationID := strings.TrimSpace(invocation.CorrelationID)
	if correlationID == "" {
		correlationID = strings.TrimSpace(invocation.RequestID)
	}
	return access.AuditEventInput{
		WorkspaceID: workspaceID, PrincipalID: invocation.PrincipalID,
		Action: descriptor.AuditEvent, TargetType: "grant", TargetID: row.ID,
		Privilege: descriptor.Privilege, Status: "success",
		RequestID: strings.TrimSpace(invocation.RequestID), CorrelationID: correlationID,
		MetadataJSON: string(metadata),
	}
}
