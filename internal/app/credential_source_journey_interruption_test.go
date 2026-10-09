package app

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Fail only the final journal acknowledgement, after the native publication
// and runtime installation have succeeded. The disposable database owns this
// trigger; production code has no fault injection or alternate runtime path.
func (f *sourceCredentialHTTPJourney) interruptSourceCompletion(t *testing.T) func() {
	t.Helper()
	admin, err := pgx.Connect(t.Context(), f.control.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, err = admin.Exec(t.Context(), `
CREATE SEQUENCE credential.fixture_completion_attempts;
CREATE FUNCTION credential.fixture_interrupt_completion() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
  IF OLD.state = 'committed' AND NEW.state = 'completed' THEN
    PERFORM nextval('credential.fixture_completion_attempts'::regclass);
    RAISE EXCEPTION 'fixture interrupted credential completion';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER fixture_interrupt_completion BEFORE UPDATE ON credential.activation_request
FOR EACH ROW EXECUTE FUNCTION credential.fixture_interrupt_completion();`)
	if err != nil {
		t.Fatal(err)
	}
	remove := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, `DROP TRIGGER IF EXISTS fixture_interrupt_completion ON credential.activation_request;
DROP FUNCTION IF EXISTS credential.fixture_interrupt_completion();
DROP SEQUENCE IF EXISTS credential.fixture_completion_attempts;`)
		if err != nil {
			t.Errorf("remove disposable completion interruption: %v", err)
		}
	}
	t.Cleanup(remove)
	return func() {
		// Sequence advancement survives rollback and proves that the injected
		// completion failure, rather than an earlier unrelated failure, occurred.
		var count int64
		var called bool
		if err := admin.QueryRow(t.Context(), "SELECT last_value, is_called FROM credential.fixture_completion_attempts").Scan(&count, &called); err != nil || !called || count != 1 {
			t.Fatalf("completion interruption was not reached exactly once: %v", err)
		}
		remove()
	}
}
