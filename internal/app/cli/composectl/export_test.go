//go:build linux

package composectl

import (
	"context"
	"io"
	"testing"
)

// NewBundledPostgresRecoveryQualificationProject exposes the existing isolated
// Compose fixture to external tests that also exercise hostinstall snapshots.
func NewBundledPostgresRecoveryQualificationProject(
	t *testing.T,
	repository, project string,
) (string, string, *Controller) {
	return newBundledPostgresDockerProject(t, repository, project)
}

// EnsureBundledPostgresRecoveryQualification runs the canonical bundled
// startup and retryable provisioning path for recovery qualification tests.
func (c *Controller) EnsureBundledPostgresRecoveryQualification(
	ctx context.Context,
) (FirstInstallPostgres, error) {
	return c.ensureBundledPostgres(ctx)
}

// ComposeBundledPostgresRecoveryQualification runs Compose for an isolated
// bundled PostgreSQL fixture.
func (c *Controller) ComposeBundledPostgresRecoveryQualification(
	ctx context.Context,
	stdout io.Writer,
	args ...string,
) error {
	if stdout == nil {
		stdout = io.Discard
	}
	return c.compose(ctx, nil, stdout, stdout, args...)
}

// QueryBundledPostgresRecoveryQualification queries a database in an isolated
// bundled PostgreSQL fixture through its bootstrap role.
func QueryBundledPostgresRecoveryQualification(
	ctx context.Context,
	root, database, query string,
) (string, error) {
	return bundledPostgresQuery(ctx, root, database, query)
}
