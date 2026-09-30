package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTargetPoolPreparePreservesInitializationAndCleanupFailures(t *testing.T) {
	initErr := errors.New("secret-bearing initialization failure")
	closeErr := errors.New("secret-bearing close failure")
	session := &cleanupTrackingTargetSession{execErr: initErr, closeErr: closeErr}
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
	snapshot := identityTestSnapshot(t, false)
	defer snapshot.Destroy()

	_, err := factory.Prepare(t.Context(), testDuckDBTargetBinding(t), snapshot)
	require.ErrorIs(t, err, initErr)
	require.ErrorIs(t, err, ErrTargetPoolCleanupFailed)
	require.NotContains(t, err.Error(), closeErr.Error())
	require.Equal(t, 1, session.closeCalls)
}

func TestTargetPoolPrepareCleansSessionReturnedWithOpenError(t *testing.T) {
	openErr := errors.New("secret-bearing opener failure")
	closeErr := errors.New("secret-bearing close failure")
	session := &cleanupTrackingTargetSession{closeErr: closeErr}
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) {
		return session, openErr
	})
	snapshot := identityTestSnapshot(t, false)
	defer snapshot.Destroy()

	_, err := factory.Prepare(t.Context(), testDuckDBTargetBinding(t), snapshot)
	require.ErrorIs(t, err, openErr)
	require.ErrorIs(t, err, ErrTargetPoolCleanupFailed)
	require.NotContains(t, err.Error(), closeErr.Error())
	require.Equal(t, 1, session.closeCalls)
}

type cleanupTrackingTargetSession struct {
	execErr    error
	closeErr   error
	closeCalls int
}

func (session *cleanupTrackingTargetSession) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, session.execErr
}

func (session *cleanupTrackingTargetSession) Close() error {
	session.closeCalls++
	return session.closeErr
}

var _ TargetRuntimeSession = (*cleanupTrackingTargetSession)(nil)
