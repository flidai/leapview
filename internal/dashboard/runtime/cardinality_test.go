package runtime

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
)

func TestConsumerTableCountIsExplicitlyOptIn(t *testing.T) {
	initial := dashboard.TableRequest{Block: "a", Start: 0}.WithDefaults()
	if consumerTableNeedsExactCount("selection", initial, false) {
		t.Fatal("bounded table scheduled an implicit exact count")
	}
	if !consumerTableNeedsExactCount("selection", initial, true) {
		t.Fatal("exact table did not schedule its requested count")
	}
	if consumerTableNeedsExactCount("visual_window", initial, true) {
		t.Fatal("scrolling window scheduled a count")
	}
}

func TestConsumerTableMetadataPendingTracksExactCountLifecycle(t *testing.T) {
	initial := dashboard.TableRequest{Block: "a", Start: 0}.WithDefaults()
	tests := []struct {
		name       string
		command    string
		request    dashboard.TableRequest
		exact      bool
		totalKnown bool
		want       bool
	}{
		{name: "exact count is pending", command: "selection", request: initial, exact: true, want: true},
		{name: "known total is complete", command: "selection", request: initial, exact: true, totalKnown: true},
		{name: "bounded window has no count", command: "visual_window", request: initial, exact: true},
		{name: "non exact table has no count", command: "selection", request: initial},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := consumerTableMetadataPending(test.command, test.request, test.exact, test.totalKnown); got != test.want {
				t.Fatalf("metadata pending = %t, want %t", got, test.want)
			}
		})
	}
}
