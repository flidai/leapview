package module

import (
	"context"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/refresh/notification"
)

// RunChange is a commit-scoped invalidation hint, not a replacement for the
// durable refresh run read model.
type RunChange = notification.Change

const RunChangeStreamID = notification.StreamID

type RunChangeListener interface {
	Start(context.Context) error
	Stop(context.Context) error
}

func NewPostgresRunChangeListener(pool *platformpostgres.Pool, wake func(RunChange)) RunChangeListener {
	return notification.NewListener(pool, wake)
}
