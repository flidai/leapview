package module

import (
	"context"
	"encoding/json"

	"github.com/flidai/leapview/internal/dashboard/publication"
	publicationsqlite "github.com/flidai/leapview/internal/dashboard/publication/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type PublicationActivationInput struct {
	ProjectID, ServingStateID, ActorID string
	Publications                       map[string]json.RawMessage
}

type PublicationPrincipalActivator func(context.Context, transaction.Transaction, projectgraph.ResourceID, string) error

func ReconcilePublications(
	ctx context.Context,
	tx transaction.Transaction,
	input PublicationActivationInput,
	activatePrincipal PublicationPrincipalActivator,
) error {
	projectID, err := projectgraph.NewResourceID(input.ProjectID)
	if err != nil {
		return err
	}
	publications := make(map[string]publication.Definition, len(input.Publications))
	for name, raw := range input.Publications {
		var definition publication.Definition
		if err := json.Unmarshal(raw, &definition); err != nil {
			return err
		}
		publications[name] = definition
	}
	return publicationsqlite.ReconcileTx(ctx, tx, publication.ReconcileInput{
		ProjectID:      projectID,
		ServingStateID: input.ServingStateID, ActorID: input.ActorID,
		Publications: publications,
	}, activatePrincipal)
}
