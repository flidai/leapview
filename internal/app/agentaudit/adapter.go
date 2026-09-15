// Package agentaudit composes the Agent-owned audit intent port with Access's
// canonical PostgreSQL audit authority. Keeping this adapter in app
// composition prevents Agent persistence from importing Access storage while
// preserving one caller-owned transaction for the agent mutation and audit
// row.
package agentaudit

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	"github.com/flidai/leapview/internal/app/auditadapter"
)

// Adapter is stateless and safe to share between Agent requests.
type Adapter struct {
	authority auditadapter.Authority
}

var _ agentpostgres.AuditIntentRecorder = (*Adapter)(nil)

// NewWithRepository is useful to composition tests and keeps the dependency
// explicit without exposing Access's concrete event projection to Agent.
func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{authority: auditadapter.New(audit)}
}

// RecordAuditIntent appends and reads back the canonical audit intent using
// the exact transaction supplied by Agent. It never begins, commits, or rolls
// back that transaction.
func (a *Adapter) RecordAuditIntent(ctx context.Context, tx agentpostgres.Tx, intent access.AuditIntent) error {
	if a == nil || !a.authority.Configured() {
		return errors.New("agent audit adapter is not configured")
	}
	_, err := a.authority.Record(ctx, tx, intent)
	return err
}
