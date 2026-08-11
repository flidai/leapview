package operation

import (
	"context"
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
	err := c.execute(ctx, access.OperationCreateGrant, invocation, input.Object.WorkspaceID, nil, func(repository access.Repository) error {
		var err error
		row, err = repository.CreateGrant(ctx, input)
		return err
	}, func() access.Grant { return row })
	return row, err
}

func (c *GrantCommands) UpdateGrant(ctx context.Context, invocation access.GrantInvocation, workspaceID, grantID string, input access.GrantInput) (access.Grant, error) {
	var row access.Grant
	err := c.execute(ctx, access.OperationUpdateGrant, invocation, workspaceID, func(repository access.Repository) (string, error) {
		current, err := repository.GetGrant(ctx, workspaceID, grantID)
		if err != nil {
			return "", err
		}
		return access.GrantRevision(current)
	}, func(repository access.Repository) error {
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
	err := c.execute(ctx, access.OperationDeleteGrant, invocation, workspaceID, nil, func(repository access.Repository) error {
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
	targetValue string,
	currentRevision func(access.Repository) (string, error),
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
		return grantAuditInput(descriptor, invocation, result())
	}
	transactional, ok := repository.(access.AuditedMutationRepository)
	if !ok {
		return fmt.Errorf("%w: grant repository does not support transactional auditing", access.ErrAuditTransaction)
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	if _, generated := apigencommand.OperationID(ctx); !generated {
		contract, ok := accessgen.GetAPIGenCommandRuntimeContract(string(operationID))
		if !ok {
			return fmt.Errorf("generated command contract %q is unavailable", operationID)
		}
		ctx, _, err = apigencommand.BeginInvocation(ctx, contract, apigencommand.Invocation{
			Surface:        apigencommand.Surface(invocation.Surface),
			TargetValues:   map[string]string{descriptor.Target.Parameter: targetValue},
			IdempotencyKey: invocation.IdempotencyKey, ConcurrencyToken: invocation.ConcurrencyToken,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		if err != nil {
			return err
		}
	}
	return executor.Execute(ctx, string(operationID), apigencommand.Execution{
		Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
			return transactional.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
				if contract.Concurrency == apigencommand.ConcurrencyIfMatch {
					if currentRevision == nil {
						return access.AuditEventInput{}, fmt.Errorf("operation %q concurrency revision source is unavailable", operationID)
					}
					current, revisionErr := currentRevision(repository)
					if revisionErr != nil {
						return access.AuditEventInput{}, revisionErr
					}
					if revisionErr := executor.CheckConcurrency(ctx, string(operationID), invocation.ConcurrencyToken, current); revisionErr != nil {
						return access.AuditEventInput{}, revisionErr
					}
				}
				input, mutationErr := audited(repository)
				if mutationErr == nil && input.Action != contract.AuditAction {
					return access.AuditEventInput{}, fmt.Errorf("generated audit action %q does not match mutation action %q", contract.AuditAction, input.Action)
				}
				return input, mutationErr
			})
		},
	})
}

func grantAuditInput(descriptor access.OperationDescriptor, invocation access.GrantInvocation, row access.Grant) (access.AuditEventInput, error) {
	payload := accessgen.GenSchemaGrantAuditPayload{
		OperationId: string(descriptor.ID), ObjectId: row.ObjectID,
		ObjectType: string(row.ObjectType), SubjectId: row.SubjectID,
		SubjectType: string(row.SubjectType), Privilege: string(row.Privilege),
		Surface: string(invocation.Surface),
	}
	workspaceID := row.WorkspaceID
	if row.ObjectType == access.SecurableProjectEnvironment {
		workspaceID = ""
		payload.ProjectId = row.WorkspaceID
		payload.Environment = row.ObjectID
	}
	metadata, err := encodeGrantAuditPayload(descriptor.ID, payload)
	if err != nil {
		return access.AuditEventInput{}, err
	}
	correlationID := strings.TrimSpace(invocation.CorrelationID)
	if correlationID == "" {
		correlationID = strings.TrimSpace(invocation.RequestID)
	}
	return access.AuditEventInput{
		WorkspaceID: workspaceID, PrincipalID: invocation.PrincipalID,
		Action: descriptor.AuditEvent, TargetType: "grant", TargetID: row.ID,
		Privilege: descriptor.Privilege, Status: "success",
		RequestID: strings.TrimSpace(invocation.RequestID), CorrelationID: correlationID,
		MetadataJSON: metadata,
	}, nil
}

func encodeGrantAuditPayload(operationID access.OperationID, payload accessgen.GenSchemaGrantAuditPayload) (string, error) {
	switch operationID {
	case access.OperationCreateGrant:
		return accessgen.EncodeGenCreateGrantAuditPayload(payload)
	case access.OperationUpdateGrant:
		return accessgen.EncodeGenUpdateGrantAuditPayload(payload)
	default:
		return accessgen.EncodeGenDeleteGrantAuditPayload(payload)
	}
}
