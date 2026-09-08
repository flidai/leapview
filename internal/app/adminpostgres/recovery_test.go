package adminpostgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/recoveryset"
)

func TestRecoveryOperationsRejectNonProductionWithoutOpeningDatabase(t *testing.T) {
	opened := false
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return config.Config{Production: false}, nil },
		OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) {
			opened = true
			return nil, nil
		},
	})
	for name, call := range map[string]func() error{
		"validate": func() error {
			_, err := ops.ValidateRecovery(t.Context(), admincli.RecoveryValidateRequest{Validator: "operator"})
			return err
		},
		"publish": func() error {
			_, err := ops.PublishRecovery(t.Context(), admincli.RecoveryPublishRequest{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrNativeMaintenanceUnavailable) {
				t.Fatalf("error = %v, want ErrNativeMaintenanceUnavailable", err)
			}
		})
	}
	if opened {
		t.Fatal("non-production recovery opened a maintenance pool")
	}
}

func TestPrepareRecoveryRejectsInvalidSetBeforeOpeningDatabase(t *testing.T) {
	opened := false
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) {
			return validProductionMaintenanceConfig(), nil
		},
		OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) {
			opened = true
			return nil, nil
		},
	})
	_, err := ops.PrepareRecovery(t.Context(), admincli.RecoveryPrepareRequest{Set: recoveryset.RecoverySet{Status: recoveryset.StatusPublished}, ExpiresAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	if !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("invalid set error = %v, want recoveryset.ErrInvalid", err)
	}
	if opened {
		t.Fatal("invalid recovery set opened a maintenance pool")
	}
}

func TestPrepareRecoveryRejectsMissingExpiryBeforeOpeningDatabase(t *testing.T) {
	opened := false
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return validProductionMaintenanceConfig(), nil },
		OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) {
			opened = true
			return nil, nil
		},
	})
	_, err := ops.PrepareRecovery(t.Context(), admincli.RecoveryPrepareRequest{Set: recoveryset.RecoverySet{Status: recoveryset.StatusPrepared}})
	if err == nil || !strings.Contains(err.Error(), "expiry is required") {
		t.Fatalf("missing expiry error = %v", err)
	}
	if opened {
		t.Fatal("missing expiry opened a maintenance pool")
	}
}

func TestValidateRecoveryRejectsInvalidValidatorBeforeOpeningDatabase(t *testing.T) {
	opened := false
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return validProductionMaintenanceConfig(), nil },
		OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) {
			opened = true
			return nil, nil
		},
	})
	_, err := ops.ValidateRecovery(t.Context(), admincli.RecoveryValidateRequest{Validator: " operator "})
	if err == nil || !strings.Contains(err.Error(), "validator identity") {
		t.Fatalf("invalid validator error = %v", err)
	}
	if opened {
		t.Fatal("invalid recovery validator opened a maintenance pool")
	}
}

func TestRecoveryOperationsBaselineFailureClosesPool(t *testing.T) {
	p := &testMaintenancePool{}
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return validProductionMaintenanceConfig(), nil },
		OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) { return p, nil },
		VerifyBaseline:  func(context.Context, postgresbaseline.SQLDBProvider) error { return errors.New("baseline mismatch") },
	})
	for name, call := range map[string]func() error{
		"validate": func() error {
			_, err := ops.ValidateRecovery(t.Context(), admincli.RecoveryValidateRequest{SetID: "018f3f83-7b2f-7b37-9f9e-000000000100", AttemptID: "018f3f83-7b2f-7b37-9f9e-000000000101", Validator: "operator", Evidence: []byte(`{}`)})
			return err
		},
		"publish": func() error {
			_, err := ops.PublishRecovery(t.Context(), admincli.RecoveryPublishRequest{SetID: "018f3f83-7b2f-7b37-9f9e-000000000100", Publisher: "operator", FenceEpoch: 1, ValidationAttemptID: "018f3f83-7b2f-7b37-9f9e-000000000101"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil || !strings.Contains(err.Error(), "baseline") {
				t.Fatalf("error = %v, want baseline failure", err)
			}
		})
	}
	if !p.closed {
		t.Fatal("baseline failure did not close maintenance pool")
	}
}
