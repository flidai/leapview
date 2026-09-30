package sourcework

import (
	"context"
	"errors"
)

var ErrRevalidatorUnavailable = errors.New("source work authority revalidator is unavailable")

type Revalidator func(context.Context) error

type revalidatorContextKey struct{}

type revalidatorChain struct {
	parent *revalidatorChain
	check  Revalidator
}

type revalidationError struct{ cause error }

func (e revalidationError) Error() string { return "source work authority revalidation failed" }
func (e revalidationError) Unwrap() error { return e.cause }

// WithRevalidator adds an authority check to ctx. Checks already installed on
// ctx remain in the chain and run first. An explicitly nil checker fails closed.
func WithRevalidator(ctx context.Context, check Revalidator) context.Context {
	var parent *revalidatorChain
	if current, ok := ctx.Value(revalidatorContextKey{}).(*revalidatorChain); ok {
		parent = current
	}
	return context.WithValue(ctx, revalidatorContextKey{}, &revalidatorChain{parent: parent, check: check})
}

// Revalidate runs the checks installed on ctx from oldest to newest. No
// installed checker is a no-op so callers outside native refresh keep their
// existing policy. An installed nil checker is an explicit fail-closed state.
func Revalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	chain, _ := ctx.Value(revalidatorContextKey{}).(*revalidatorChain)
	if chain == nil {
		return nil
	}
	return chain.revalidate(ctx)
}

func (c *revalidatorChain) revalidate(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if err := c.parent.revalidate(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.check == nil {
		return revalidationError{cause: ErrRevalidatorUnavailable}
	}
	if err := c.check(ctx); err != nil {
		if cancellation := ctx.Err(); cancellation != nil {
			return revalidationError{cause: errors.Join(err, cancellation)}
		}
		return revalidationError{cause: err}
	}
	return ctx.Err()
}
