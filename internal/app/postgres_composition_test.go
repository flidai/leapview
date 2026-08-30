package app

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/config"
)

func TestBuildProductionFailsClosedBeforeLegacySQLiteComposition(t *testing.T) {
	_, err := BuildProduction(context.Background(), config.Config{Production: true})
	if err == nil {
		t.Fatal("BuildProduction accepted missing PostgreSQL control-plane configuration")
	}
	if !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_CONTROL_URL") {
		t.Fatalf("BuildProduction error = %v, want control URL validation", err)
	}
}

func TestBuildCannotBypassProductionPostgreSQLGate(t *testing.T) {
	_, err := Build(context.Background(), config.Config{Production: true})
	if err == nil || !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_CONTROL_URL") {
		t.Fatalf("Build production gate error = %v", err)
	}
}

func TestOpenPostgresControlPlaneRejectsMissingPoolConfiguration(t *testing.T) {
	_, err := openPostgresControlPlane(context.Background(), config.Config{})
	if err == nil {
		t.Fatal("openPostgresControlPlane accepted an empty pool configuration")
	}
}
