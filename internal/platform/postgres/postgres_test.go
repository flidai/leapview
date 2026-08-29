package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("unexpected destination count")
	}
	for i := range dest {
		switch value := dest[i].(type) {
		case *string:
			v, ok := r.values[i].(string)
			if !ok {
				return errors.New("expected string destination")
			}
			*value = v
		case *bool:
			v, ok := r.values[i].(bool)
			if !ok {
				return errors.New("expected bool destination")
			}
			*value = v
		default:
			return errors.New("unsupported destination")
		}
	}
	return nil
}

type fakeProbe struct{ row pgx.Row }

func (p fakeProbe) QueryRow(context.Context, string, ...any) pgx.Row { return p.row }

func testConfig() Config {
	return Config{URL: "postgres://leapview@localhost/leapview_control", RuntimeRole: "leapview_runtime"}
}

func TestValidateAcceptsPostgreSQL18ReadWriteRole(t *testing.T) {
	cfg := testConfig()
	probe := fakeProbe{row: fakeRow{values: []any{"180006", "leapview_runtime", "off", false}}}
	if err := Validate(t.Context(), probe, cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsMajorRoleAndIntentMismatches(t *testing.T) {
	base := testConfig()
	tests := []struct {
		name string
		cfg  Config
		row  fakeRow
	}{
		{name: "major", cfg: base, row: fakeRow{values: []any{"170006", "leapview_runtime", "off", false}}},
		{name: "role", cfg: base, row: fakeRow{values: []any{"180006", "wrong_role", "off", false}}},
		{name: "read-write standby", cfg: base, row: fakeRow{values: []any{"180006", "leapview_runtime", "on", true}}},
		{name: "read-only primary", cfg: Config{URL: base.URL, RuntimeRole: base.RuntimeRole, Intent: IntentReadOnly}, row: fakeRow{values: []any{"180006", "leapview_runtime", "off", false}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(t.Context(), fakeProbe{row: test.row}, test.cfg); err == nil {
				t.Fatal("Validate() unexpectedly accepted invalid capabilities")
			}
		})
	}
}

func TestConfigValidateRequiresURLAndRole(t *testing.T) {
	for _, cfg := range []Config{{}, {URL: "postgres://localhost/db"}} {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Config.Validate(%#v) unexpectedly succeeded", cfg)
		}
	}
}

func TestConfigValidateTLSIntent(t *testing.T) {
	cfg := testConfig()
	cfg.RequireTLS = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("plaintext PostgreSQL URL accepted when TLS is required")
	}
	cfg.URL += "?sslmode=verify-full"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("TLS PostgreSQL URL rejected: %v", err)
	}
}

func TestConfigurePoolAppliesIndependentBoundsAndSessionTimeouts(t *testing.T) {
	parsed, err := pgxpool.ParseConfig("postgres://leapview@localhost/leapview_control")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		URL:                    "postgres://leapview@localhost/leapview_control",
		RuntimeRole:            "leapview_runtime",
		MinConns:               2,
		MaxConns:               7,
		AcquireTimeout:         1500 * time.Millisecond,
		StatementTimeout:       2 * time.Second,
		LockTimeout:            250 * time.Millisecond,
		IdleTransactionTimeout: 3 * time.Second,
	}
	if err := ConfigurePool(parsed, cfg); err != nil {
		t.Fatal(err)
	}
	if parsed.MinConns != 2 || parsed.MaxConns != 7 {
		t.Fatalf("pool bounds = %d/%d, want 2/7", parsed.MinConns, parsed.MaxConns)
	}
	params := parsed.ConnConfig.RuntimeParams
	for key, want := range map[string]string{
		"statement_timeout":                   "2000",
		"lock_timeout":                        "250",
		"idle_in_transaction_session_timeout": "3000",
		"default_transaction_read_only":       "off",
	} {
		if params[key] != want {
			t.Fatalf("runtime param %s = %q, want %q", key, params[key], want)
		}
	}
}

func TestConfigurePoolReadOnlyIntentEnablesReadOnlySession(t *testing.T) {
	parsed, err := pgxpool.ParseConfig("postgres://leapview@localhost/leapview_ducklake")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.Intent = IntentReadOnly
	if err := ConfigurePool(parsed, cfg); err != nil {
		t.Fatal(err)
	}
	if got := parsed.ConnConfig.RuntimeParams["default_transaction_read_only"]; got != "on" {
		t.Fatalf("default_transaction_read_only = %q, want on", got)
	}
}
