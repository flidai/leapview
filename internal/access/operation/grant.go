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

type GrantCommands struct {
	repository RepositoryProvider
}

func NewGrantCommands(repository RepositoryProvider) (*GrantCommands, error) {
	return &GrantCommands{repository: repository}, nil
}

func (c *GrantCommands) CreateGrant(ctx context.Context, invocation access.GrantInvocation, input access.GrantInput) (access.Grant, error) {
	var row access.Grant
	operationID := accessgen.GenCommandOperationCreateGrant().APIGenOperationID()
	err := c.execute(ctx, operationID, invocation, func(executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return accessgen.ExecuteGenCreateGrantCommand(ctx, executor, accessgen.GenCreateGrantCommandInvocation{
			Surface: invocation.Surface, Workspace: input.Object.WorkspaceID, IdempotencyKey: invocation.IdempotencyKey,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	}, nil, nil, func(repository access.Repository) error {
		var err error
		row, err = repository.CreateGrant(ctx, input)
		return err
	}, func() access.Grant { return row }, accessgen.EncodeGenCreateGrantAuditPayload)
	return row, err
}

func (c *GrantCommands) CreateGrants(ctx context.Context, invocation access.GrantInvocation, inputs []access.GrantInput) ([]access.Grant, error) {
	operationID := accessgen.GenCommandOperationCreateGrant().APIGenOperationID()
	if len(inputs) == 0 {
		return nil, fmt.Errorf("operation %q requires at least one grant", operationID)
	}
	workspaceID := strings.TrimSpace(inputs[0].Object.WorkspaceID)
	for _, input := range inputs {
		if strings.TrimSpace(input.Object.WorkspaceID) != workspaceID {
			return nil, fmt.Errorf("operation %q requires one workspace per batch", operationID)
		}
	}
	if invocation.Surface == access.OperationSurfaceUI {
		if err := uicommand.VerifyClaim(invocation.OperationClaims, operationID); err != nil {
			return nil, err
		}
	}
	if c == nil || c.repository == nil {
		return nil, fmt.Errorf("operation %q repository is required", operationID)
	}
	repository, err := c.repository()
	if err != nil {
		return nil, err
	}
	transactional, ok := repository.(access.AuditedMutationBatchRepository)
	if !ok {
		return nil, fmt.Errorf("%w: grant repository does not support transactional batch auditing", access.ErrAuditTransaction)
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return nil, err
	}
	rows := make([]access.Grant, 0, len(inputs))
	err = accessgen.ExecuteGenCreateGrantCommand(ctx, executor, accessgen.GenCreateGrantCommandInvocation{
		Surface: invocation.Surface, Workspace: workspaceID, IdempotencyKey: invocation.IdempotencyKey,
		RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
	}, apigencommand.Execution{Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
		return transactional.RunAuditedMutationBatch(ctx, func(repository access.Repository) ([]access.AuditEventInput, error) {
			rows = rows[:0]
			events := make([]access.AuditEventInput, 0, len(inputs))
			for _, input := range inputs {
				row, mutationErr := repository.CreateGrant(ctx, input)
				if mutationErr != nil {
					return nil, mutationErr
				}
				event, auditErr := grantAuditInput(contract, operationID, invocation, row, accessgen.EncodeGenCreateGrantAuditPayload)
				if auditErr != nil {
					return nil, auditErr
				}
				rows = append(rows, row)
				events = append(events, event)
			}
			return events, nil
		})
	}})
	if err != nil {
		return nil, err
	}
	return rows, err
}

func (c *GrantCommands) UpdateGrant(ctx context.Context, invocation access.GrantInvocation, workspaceID, grantID string, input access.GrantInput) (access.Grant, error) {
	var row access.Grant
	operationID := accessgen.GenCommandOperationUpdateGrant().APIGenOperationID()
	err := c.execute(ctx, operationID, invocation, func(executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return accessgen.ExecuteGenUpdateGrantCommand(ctx, executor, accessgen.GenUpdateGrantCommandInvocation{
			Surface: invocation.Surface, Workspace: workspaceID, ConcurrencyToken: invocation.ConcurrencyToken,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	}, func(repository access.Repository) (string, error) {
		current, err := repository.GetGrant(ctx, workspaceID, grantID)
		if err != nil {
			return "", err
		}
		return access.GrantRevision(current)
	}, func(ctx context.Context, executor *apigencommand.Executor, presented, current string) error {
		return accessgen.CheckGenUpdateGrantCommandConcurrency(ctx, executor, presented, current)
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
	}, func() access.Grant { return row }, accessgen.EncodeGenUpdateGrantAuditPayload)
	return row, err
}

func (c *GrantCommands) DeleteGrant(ctx context.Context, invocation access.GrantInvocation, workspaceID, grantID string) (access.Grant, error) {
	var row access.Grant
	operationID := accessgen.GenCommandOperationDeleteGrant().APIGenOperationID()
	err := c.execute(ctx, operationID, invocation, func(executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return accessgen.ExecuteGenDeleteGrantCommand(ctx, executor, accessgen.GenDeleteGrantCommandInvocation{
			Surface: invocation.Surface, Workspace: workspaceID,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	}, nil, nil, func(repository access.Repository) error {
		var err error
		row, err = repository.GetGrant(ctx, workspaceID, grantID)
		if err != nil {
			return err
		}
		return repository.DeleteGrant(ctx, workspaceID, grantID)
	}, func() access.Grant { return row }, accessgen.EncodeGenDeleteGrantAuditPayload)
	return row, err
}

func (c *GrantCommands) execute(
	ctx context.Context,
	operationID string,
	invocation access.GrantInvocation,
	executeGenerated func(*apigencommand.Executor, apigencommand.Execution) error,
	currentRevision func(access.Repository) (string, error),
	checkConcurrency func(context.Context, *apigencommand.Executor, string, string) error,
	mutation func(access.Repository) error,
	result func() access.Grant,
	encodeAuditPayload func(accessgen.GenSchemaGrantAuditPayload) (string, error),
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
		return fmt.Errorf("%w: grant repository does not support transactional auditing", access.ErrAuditTransaction)
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
				return grantAuditInput(contract, operationID, invocation, result(), encodeAuditPayload)
			})
		},
	})
}

func grantAuditInput(contract apigencommand.Contract, operationID string, invocation access.GrantInvocation, row access.Grant, encodeAuditPayload func(accessgen.GenSchemaGrantAuditPayload) (string, error)) (access.AuditEventInput, error) {
	payload := accessgen.GenSchemaGrantAuditPayload{
		OperationId: operationID, ObjectId: row.ObjectID,
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
	metadata, err := encodeAuditPayload(payload)
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
		WorkspaceID: workspaceID, PrincipalID: invocation.PrincipalID,
		Action: contract.AuditAction, TargetType: "grant", TargetID: row.ID,
		Privilege: privilege, Status: "success",
		RequestID: strings.TrimSpace(invocation.RequestID), CorrelationID: correlationID,
		MetadataJSON: metadata,
	}, nil
}
