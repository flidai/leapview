package app

import (
	"context"
	"testing"
)

func TestCapabilityBuildersValidateRequiredDependencies(t *testing.T) {
	t.Run("analytics persistence", func(t *testing.T) {
		_, err := buildAnalyticsCapability(context.Background(), analyticsCapabilityConfig{})
		if err == nil || err.Error() != "analytics persistence is required" {
			t.Fatalf("error = %v, want analytics persistence validation", err)
		}
	})
	t.Run("access persistence", func(t *testing.T) {
		_, err := buildAccessCapability(context.Background(), accessCapabilityConfig{})
		if err == nil || err.Error() != "access persistence is required" {
			t.Fatalf("error = %v, want access persistence validation", err)
		}
	})
	t.Run("jobs persistence", func(t *testing.T) {
		_, err := buildWorkloadCapability(context.Background(), workloadCapabilityConfig{})
		if err == nil || err.Error() != "jobs persistence is required" {
			t.Fatalf("error = %v, want jobs persistence validation", err)
		}
	})
}
