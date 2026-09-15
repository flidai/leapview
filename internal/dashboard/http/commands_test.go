package http

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
)

func TestVisualWindowRefreshUsesPerVisualRequestOrdering(t *testing.T) {
	prepared := command.PreparedRefresh{
		Plan: command.RefreshPlan{Command: "visual_window", Targets: []command.Target{{
			Kind: command.TargetWindow,
			ID:   "orders",
			WindowRequest: dashboard.TableRequest{
				Table: "orders", Block: "b", Start: 500, Count: 50, RequestSeq: 12, ResetVersion: 4,
			},
		}}},
	}
	preparation := streamPreparation(prepared)
	if preparation.SequenceKey != "window:orders" || preparation.Sequence != 12 || preparation.SequenceEpoch != 4 {
		t.Fatalf("sequence = %q/%d/%d", preparation.SequenceKey, preparation.Sequence, preparation.SequenceEpoch)
	}
}

func TestPrivateCommandsRequireAnActiveUpdatesStream(t *testing.T) {
	registry := dashboardstream.NewRegistry()
	defer registry.Close()
	handler := Handler{Coordinators: registry}
	if handler.privateStreamActive("forged-client-selected-stream") {
		t.Fatal("forged stream identity was admitted without an Updates coordinator")
	}
	registry.Ensure("open-stream", context.Background(), func(dashboardstream.RefreshEvent) {})
	if !handler.privateStreamActive("open-stream") {
		t.Fatal("active Updates stream was rejected")
	}
}
