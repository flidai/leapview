package gcstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrObjectOutsidePool reports an attempted access outside the admitted
// physical-pool namespace.
var ErrObjectOutsidePool = errors.New("object is outside the declared physical pool")

// Object is the provider-neutral inventory item returned by a target-owned
// pool store. Digest identifies the exact version accepted by DeleteConditional.
type Object struct {
	Key          string
	Digest       string
	Version      string
	Size         int64
	CreatedAt    time.Time
	LastModified time.Time
	Metadata     map[string]string
}

type DeleteRequest struct {
	PhysicalPoolID string
	Key            string
	Digest         string
	Version        string
}

type DeleteResponse struct {
	Deleted  bool
	NotFound bool
}

// PoolStore is intentionally pool-scoped. Implementations reject prefixes or
// buckets outside the declared pool before listing or deleting.
type PoolStore interface {
	Open(context.Context, string) (ObjectBody, error)
	ListPoolObjects(context.Context, string) ([]Object, error)
	DeleteConditional(context.Context, DeleteRequest) (DeleteResponse, error)
	Stat(context.Context, string, string) (Object, error)
}

// ObjectBody is an immutable object stream returned by Open.
type ObjectBody struct {
	Body     io.ReadCloser
	Size     int64
	Metadata map[string]string
}
