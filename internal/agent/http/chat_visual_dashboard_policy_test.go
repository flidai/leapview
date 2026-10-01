package http

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
)

func TestChatVisualDashboardEditInPlacePolicyRequiresUnpublishedInstanceDraft(t *testing.T) {
	for _, test := range []struct {
		name string
		item catalog.Dashboard
		want bool
	}{
		{
			name: "unpublished authored draft",
			item: catalog.Dashboard{Source: catalog.SourceInstance, Status: authoring.LifecycleStatusDraft, DraftID: "draft"},
			want: true,
		},
		{
			name: "published authored dashboard with retained draft pointer",
			item: catalog.Dashboard{Source: catalog.SourceInstance, Status: authoring.LifecycleStatusPublished, DraftID: "draft"},
			want: false,
		},
		{
			name: "published project dashboard",
			item: catalog.Dashboard{Source: catalog.SourceProject, Status: authoring.LifecycleStatusPublished},
			want: false,
		},
		{
			name: "draft without current draft pointer",
			item: catalog.Dashboard{Source: catalog.SourceInstance, Status: authoring.LifecycleStatusDraft},
			want: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := chatVisualDashboardHasEditableDraft(test.item); got != test.want {
				t.Fatalf("chatVisualDashboardHasEditableDraft() = %t, want %t", got, test.want)
			}
		})
	}
}
