package authoring

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalReducerAddsSlicerAtomically(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	command := Command{
		ID: CommandID("add-slicer"), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
		ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
	}
	command = canonicalReducerCommandWithPayload(command, &AddSlicerPayload{
		PageID: "overview", Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect", Targets: []string{"base"},
	})
	_, next, err := ApplyEdit(lifecycle, current, command, RevisionID("slicer-revision"), current.Number+1, time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Document.Spec.Filters) != 1 {
		t.Fatalf("filter count = %d", len(next.Document.Spec.Filters))
	}
	filter := next.Document.Spec.Filters[0]
	if filter.ID == "" || filter.Label != "Status" || filter.Dimension != "status" {
		t.Fatalf("filter = %#v", filter)
	}
	if filter.Targets == nil || len(*filter.Targets) != 1 || (*filter.Targets)[0] != "base" {
		t.Fatalf("slicer targets = %#v, want explicit compatible visual target", filter.Targets)
	}
	components := next.Document.Spec.Pages[0].Components
	component, ok := components[len(components)-1].Value.(*document.FilterDashboardPageComponent)
	if !ok || component.ID == "" || component.Filter != filter.ID {
		t.Fatalf("slicer component = %#v", components[len(components)-1])
	}
}

func TestAddSlicerDerivedTargetsDoNotChangeCommandFingerprint(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	command := Command{
		ID: CommandID("add-slicer-fingerprint"), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
		ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
		AddSlicer: &AddSlicerPayload{PageID: "overview", Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"},
	}
	withoutTargets, err := command.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	command.AddSlicer.Targets = []string{"base"}
	withTargets, err := command.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if withTargets != withoutTargets {
		t.Fatalf("server-derived targets changed command fingerprint: %s != %s", withTargets, withoutTargets)
	}
}
