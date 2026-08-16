package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/internal/db"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ActivateDashboardPublicationPrincipalTx installs the Access-owned identity
// for an anonymous dashboard publication inside the caller's transaction.
func ActivateDashboardPublicationPrincipalTx(ctx context.Context, tx transaction.Transaction, projectID projectgraph.ResourceID, name string) error {
	name = strings.TrimSpace(name)
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("dashboard publication principal project: %w", err)
	}
	if name == "" {
		return fmt.Errorf("dashboard publication principal requires name")
	}
	return accessdb.New(tx).UpsertPrincipal(ctx, accessdb.UpsertPrincipalParams{
		ID:          dashboardPublicationSubjectID(projectID, name),
		Kind:        string(access.PrincipalKindDashboardPublication),
		DisplayName: name,
	})
}

func dashboardPublicationSubjectID(projectID projectgraph.ResourceID, publication string) string {
	return "dashboard_publication:" + projectID.String() + "." + strings.TrimSpace(publication)
}
