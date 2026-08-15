package authoring

import (
	"encoding/json"
	"errors"
	"testing"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func validMetadataCommand() Command {
	return Command{
		ID: "command-1", DashboardID: "sales", DraftID: "draft-1", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(),
		Metadata: &MetadataPatch{Title: stringPtr("Updated sales")},
	}
}

func stringPtr(value string) *string { return &value }

func commandRevisionToken() RevisionToken {
	return RevisionToken{RevisionID: "rev-1", Number: 1, ContentHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
}

func TestCommandRequiresExactlyOneTypedPayload(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Command)
		want error
	}{
		{name: "none", edit: func(command *Command) { command.Metadata = nil }, want: ErrInvalidPayload},
		{name: "two", edit: func(command *Command) { command.RemovePage = &RemovePagePayload{PageID: "overview"} }, want: ErrInvalidPayload},
		{name: "invalid id", edit: func(command *Command) { command.DashboardID = "sales dashboard" }, want: ErrInvalidIdentifier},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			command := validMetadataCommand()
			test.edit(&command)
			if err := command.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCommandPayloadsDeclareAuthorizationAction(t *testing.T) {
	cases := []struct {
		name    string
		payload authoringPayload
		want    AuthorizationAction
	}{
		{name: "metadata", payload: &MetadataPatch{Title: stringPtr("title")}, want: AuthorizationActionEdit},
		{name: "page", payload: &UpsertPagePayload{}, want: AuthorizationActionEdit},
		{name: "remove page", payload: &RemovePagePayload{}, want: AuthorizationActionEdit},
		{name: "visual", payload: &UpsertVisualPayload{}, want: AuthorizationActionEdit},
		{name: "remove visual", payload: &RemoveVisualPayload{}, want: AuthorizationActionEdit},
		{name: "layout", payload: &SetLayoutPayload{}, want: AuthorizationActionEdit},
		{name: "filters", payload: &SetFiltersPayload{}, want: AuthorizationActionEdit},
		{name: "interaction", payload: &SetInteractionPayload{}, want: AuthorizationActionEdit},
		{name: "publish", payload: &PublishPayload{}, want: AuthorizationActionPublish},
		{name: "archive", payload: &ArchivePayload{}, want: AuthorizationActionArchive},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.payload.RequiredAction()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("RequiredAction() = %q, want %q", got, test.want)
			}
		})
	}
	if AuthorizationActionView.Valid() == false || AuthorizationAction("unknown").Valid() {
		t.Fatal("authorization action enum validation is not closed")
	}
}

func TestCommandPayloadOperationsRequireTheirContent(t *testing.T) {
	cases := []struct {
		name    string
		command Command
	}{
		{name: "page upsert missing page", command: Command{ID: "c", DashboardID: "sales", DraftID: "d", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), UpsertPage: &UpsertPagePayload{}}},
		{name: "visual upsert missing variant", command: Command{ID: "c", DashboardID: "sales", DraftID: "d", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), UpsertVisual: &UpsertVisualPayload{VisualID: "revenue"}}},
		{name: "layout no edit", command: Command{ID: "c", DashboardID: "sales", DraftID: "d", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), SetLayout: &SetLayoutPayload{PageID: "overview"}}},
		{name: "interaction no target", command: Command{ID: "c", DashboardID: "sales", DraftID: "d", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), SetInteraction: &SetInteractionPayload{}}},
		{name: "clear filters with replacement", command: Command{ID: "c", DashboardID: "sales", DraftID: "d", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), SetFilters: &SetFiltersPayload{Clear: true, Definitions: map[string]dashboardfilter.Definition{"x": {}}}}},
		{name: "clear interaction with replacement", command: Command{ID: "c", DashboardID: "sales", DraftID: "d", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), SetInteraction: &SetInteractionPayload{VisualID: "v", Clear: true, Interaction: &Interaction{}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.command.Validate(); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("Validate() error = %v, want ErrInvalidPayload", err)
			}
		})
	}
}

func TestMutationCommandRequiresCompleteExpectedRevision(t *testing.T) {
	command := validMetadataCommand()
	command.ExpectedRevision = RevisionToken{}
	if err := command.Validate(); err == nil {
		t.Fatal("mutation command with zero expected revision unexpectedly validated")
	}
}

func TestCommandDraftRequirementAndExplicitClears(t *testing.T) {
	archive := Command{ID: "archive-1", DashboardID: "sales", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), Archive: &ArchivePayload{}}
	if err := archive.Validate(); err != nil {
		t.Fatalf("dashboard-level archive should not require draft: %v", err)
	}
	publish := archive
	publish.ID = "publish-1"
	publish.Publish = &PublishPayload{}
	publish.Archive = nil
	if err := publish.Validate(); !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("publish without draft error = %v", err)
	}
	empty := ""
	clearDescription := Command{ID: "metadata-1", DashboardID: "sales", DraftID: "draft-1", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), Metadata: &MetadataPatch{Description: &empty}}
	if err := clearDescription.Validate(); err != nil {
		t.Fatalf("explicit description clear rejected: %v", err)
	}
	clearLayout := Command{ID: "layout-1", DashboardID: "sales", DraftID: "draft-1", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), SetLayout: &SetLayoutPayload{PageID: "overview", Placements: map[string]dashboardmodel.PagePlacement{}}}
	if err := clearLayout.Validate(); err != nil {
		t.Fatalf("explicit empty placement clear rejected: %v", err)
	}
}

func TestCommandFingerprintSeparatesInputsAndIsDeterministic(t *testing.T) {
	first := validMetadataCommand()
	first.ContentHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	one, err := first.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	second := first
	second.ID = "command-2" // command ID is the external idempotency key, not a request fingerprint input.
	two, err := second.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if one != two {
		t.Fatalf("fingerprint changed with idempotency key: %q != %q", one, two)
	}
	second.ExpectedRevision = RevisionToken{RevisionID: "rev-2", Number: 2, ContentHash: first.ExpectedRevision.ContentHash}
	three, err := second.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if one == three {
		t.Fatal("fingerprint did not change with expected revision")
	}
	second = first
	second.Provenance.ActorID = "principal-2"
	four, err := second.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if one == four {
		t.Fatal("fingerprint did not change with provenance")
	}
	second = first
	second.Metadata = &MetadataPatch{Title: stringPtr("Another title")}
	five, err := second.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if one == five {
		t.Fatal("fingerprint did not change with payload")
	}
}

func TestCommandJSONRoundTripAndMapOrderFingerprint(t *testing.T) {
	first := Command{ID: "command-1", DashboardID: "sales", DraftID: "draft-1", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), SetFilters: &SetFiltersPayload{Definitions: map[string]dashboardfilter.Definition{
		"region": {Field: "region"}, "channel": {Field: "channel"},
	}}}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded Command
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	one, err := first.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	two, err := decoded.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatalf("JSON round-trip changed fingerprint: %q != %q", one, two)
	}
	second := Command{ID: "command-2", DashboardID: "sales", DraftID: "draft-1", ExpectedRevision: commandRevisionToken(), Provenance: contractProvenance(), SetFilters: &SetFiltersPayload{Definitions: map[string]dashboardfilter.Definition{
		"channel": {Field: "channel"}, "region": {Field: "region"},
	}}}
	three, err := second.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if one != three {
		t.Fatalf("map insertion order changed fingerprint: %q != %q", one, three)
	}
}
