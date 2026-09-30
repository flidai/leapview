package sourcework

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRevalidateChainsChecksAndRedactsDenial(t *testing.T) {
	denied := errors.New("secret credential payload")
	var order []string
	ctx := WithRevalidator(context.Background(), func(context.Context) error {
		order = append(order, "parent")
		return nil
	})
	ctx = WithRevalidator(ctx, func(context.Context) error {
		order = append(order, "child")
		return denied
	})

	err := Revalidate(ctx)
	if !errors.Is(err, denied) {
		t.Fatalf("Revalidate error = %v, want wrapped denial", err)
	}
	if got := strings.Join(order, ","); got != "parent,child" {
		t.Fatalf("checker order = %q, want parent,child", got)
	}
	if strings.Contains(err.Error(), "secret credential payload") {
		t.Fatalf("revalidation error leaked checker details: %v", err)
	}
}

func TestRevalidateAbsentAndExplicitNil(t *testing.T) {
	if err := Revalidate(context.Background()); err != nil {
		t.Fatalf("absent checker: %v", err)
	}
	if err := Revalidate(WithRevalidator(context.Background(), nil)); !errors.Is(err, ErrRevalidatorUnavailable) {
		t.Fatalf("explicit nil checker = %v, want ErrRevalidatorUnavailable", err)
	}
}

func TestRevalidateParentDenialDoesNotRunChild(t *testing.T) {
	denied := errors.New("parent denied")
	childRan := false
	ctx := WithRevalidator(context.Background(), func(context.Context) error { return denied })
	ctx = WithRevalidator(ctx, func(context.Context) error {
		childRan = true
		return nil
	})
	if err := Revalidate(ctx); !errors.Is(err, denied) {
		t.Fatalf("parent denial = %v, want wrapped denial", err)
	}
	if childRan {
		t.Fatal("child checker ran after parent denial")
	}
}

func TestRevalidateChecksCancellationBeforeAndAfterCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if err := Revalidate(WithRevalidator(ctx, func(context.Context) error {
		calls++
		return nil
	})); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled check = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("pre-canceled context invoked checker %d times", calls)
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	err := Revalidate(WithRevalidator(ctx, func(context.Context) error {
		cancel()
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-callback cancellation = %v, want context.Canceled", err)
	}
}
