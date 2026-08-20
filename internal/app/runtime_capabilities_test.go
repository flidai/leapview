package app

import (
	"context"
	"testing"
)

func TestCapabilityBuildersValidateRequiredDependencies(t *testing.T) {
	t.Run("analytics database", func(t *testing.T) {
		_, err := buildAnalyticsCapability(context.Background(), analyticsCapabilityConfig{})
		if err == nil || err.Error() != "analytics database is required" {
			t.Fatalf("error = %v, want analytics database validation", err)
		}
	})
	t.Run("access database", func(t *testing.T) {
		_, err := buildAccessCapability(context.Background(), accessCapabilityConfig{})
		if err == nil || err.Error() != "access database is required" {
			t.Fatalf("error = %v, want access database validation", err)
		}
	})
	t.Run("jobs database", func(t *testing.T) {
		_, err := buildWorkloadCapability(context.Background(), workloadCapabilityConfig{})
		if err == nil || err.Error() != "jobs database is required" {
			t.Fatalf("error = %v, want jobs database validation", err)
		}
	})
}
