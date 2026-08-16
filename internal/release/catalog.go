package release

import (
	"context"

	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type ProjectRecord struct {
	ID, CreatedAt, UpdatedAt, LatestReleaseID, ActiveDeploymentID string
}

type ConnectionRecord struct {
	ID, Title, Description, ActiveRevisionID string
}

type CatalogRepository interface {
	GetProject(context.Context, string) (ProjectRecord, error)
	ListConnections(context.Context, string, string) ([]ConnectionRecord, error)
	GetConnection(context.Context, string, string, string) (ConnectionRecord, error)
}

type DeploymentLinkage interface {
	Get(context.Context, projectgraph.ResourceID, string) (Release, error)
	LinkDeployment(context.Context, string, string, string, string) error
	LinkDeploymentTx(context.Context, transaction.Transaction, string, string, string, string) error
	DeploymentRelease(context.Context, string, string) (string, string, error)
	ListDeploymentIDs(context.Context, string) ([]string, error)
	PriorDeploymentRelease(context.Context, string, string) (string, error)
}

// DeploymentPublisher is the narrow Release-owned capability consumed by the
// Deployment composition root. Candidate promotion stays inside Release while
// Deployment applies target policy and activation.
type DeploymentPublisher interface {
	DeploymentLinkage
	PublishCandidate(context.Context, PublishCandidateInput) (Release, error)
}
