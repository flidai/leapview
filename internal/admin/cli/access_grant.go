package cli

import (
	"context"
	"errors"
	"io"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/spf13/cobra"
)

// StageAccessGrantRequest is explicit local operator policy intent. It cannot
// select the installation/environment, bypass publication, or issue a token.
type StageAccessGrantRequest struct {
	ProjectID, GrantID, PrincipalID, ResourceID, ResourceKind string
	Actions                                                   []string
	ExpectedRevision                                          int64
	OperationID                                               string
	Apply                                                     bool
}

type AccessGrantOperations interface {
	StageAccessGrant(context.Context, StageAccessGrantRequest, io.Writer) error
}

func (r StageAccessGrantRequest) Grant() (access.AuthorizationGrant, error) {
	projectID, err := graph.NewResourceID(r.ProjectID)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	resource, err := access.NewResourceRef(graph.ResourceID(r.ResourceID), graph.Kind(r.ResourceKind))
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	if r.ExpectedRevision < 1 || r.OperationID == "" || len(r.Actions) == 0 {
		return access.AuthorizationGrant{}, errors.New("positive expected revision, operation ID, and at least one action are required")
	}
	pairs := make([]access.PermissionPair, 0, len(r.Actions))
	for _, action := range r.Actions {
		definition, ok := access.Permission(access.Action(action))
		if !ok || !definition.Delegable || definition.Scope != access.PermissionScopeResource {
			return access.AuthorizationGrant{}, errors.New("staged grants require delegable exact-resource actions")
		}
		pair, err := access.NewExactPermissionPair(definition.Action, projectID, resource)
		if err != nil {
			return access.AuthorizationGrant{}, err
		}
		pairs = append(pairs, pair)
	}
	grant := access.AuthorizationGrant{ID: r.GrantID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: r.PrincipalID}, Resource: resource, PermissionProfile: access.PermissionCatalogProfile, Permissions: pairs}
	if err := access.ValidateAuthorizationGrant(grant); err != nil {
		return access.AuthorizationGrant{}, err
	}
	return grant, nil
}

func accessGrantCommand(ctx context.Context, operations Operations) *cobra.Command {
	request := StageAccessGrantRequest{}
	parent := adminGroupCommand("access", "Offline access policy administration")
	command := &cobra.Command{
		Use:         "stage-grant",
		Short:       "Stage an exact resource grant for normal candidate approval and activation",
		Annotations: map[string]string{"leapview.dev/effect": "write", "leapview.dev/confirmation": "required"},
		Args:        cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if _, err := request.Grant(); err != nil {
				return err
			}
			operator, ok := operations.(AccessGrantOperations)
			if !ok {
				return errors.New("offline access grant staging is unavailable")
			}
			return operator.StageAccessGrant(ctx, request, command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&request.ProjectID, "project", "", "exact claimed Project ID")
	command.Flags().StringVar(&request.GrantID, "id", "", "stable grant ID")
	command.Flags().StringVar(&request.PrincipalID, "principal", "", "exact recipient principal ID")
	command.Flags().StringVar(&request.ResourceID, "resource", "", "exact resource ID; validated against the candidate graph at admission")
	command.Flags().StringVar(&request.ResourceKind, "kind", "", "canonical resource kind")
	command.Flags().StringSliceVar(&request.Actions, "action", nil, "exact typed action; may be repeated")
	command.Flags().Int64Var(&request.ExpectedRevision, "expected-revision", 0, "current target policy revision")
	command.Flags().StringVar(&request.OperationID, "operation-id", "", "stable idempotency key")
	command.Flags().BoolVar(&request.Apply, "apply", false, "persist the staged policy; otherwise preview only")
	parent.AddCommand(command)
	return parent
}
