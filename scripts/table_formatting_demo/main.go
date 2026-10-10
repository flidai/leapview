// Local playground adapter. Uses the production authoring reducer without a database.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/document"
)

type request struct {
	Document    document.DashboardDocument `json:"document"`
	Number      uint64                     `json:"number"`
	FormatKey   string                     `json:"formatKey"`
	FormatValue string                     `json:"formatValue"`
}

func main() {
	if err := run(); err != nil {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"error": err.Error()})
		os.Exit(1)
	}
}
func run() error {
	var input request
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return err
	}
	if input.Number == 0 {
		input.Number = 1
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "playground"}
	revision, err := authoring.NewRevision(authoring.RevisionID(fmt.Sprintf("demo-%d", input.Number)), "dashboard:parts-formatting", input.Number, time.Now().UTC(), input.Document, provenance)
	if err != nil {
		return err
	}
	if input.FormatKey != "" {
		lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: "project:playground", ID: revision.DashboardID, OwnerPrincipalID: "playground", Slug: "parts-formatting", Title: "Parts formatting", SemanticModel: "parts", Visibility: authoring.VisibilityPrivate, Draft: &authoring.Draft{ID: "demo-draft", DashboardID: revision.DashboardID, Revision: revision.Token(), Provenance: provenance}})
		if err != nil {
			return err
		}
		command := authoring.Command{ID: authoring.CommandID(fmt.Sprintf("edit-%d", input.Number)), DashboardID: revision.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: revision.Token(), Provenance: provenance, UpdateVisualFormat: &authoring.UpdateVisualFormatPayload{PageID: "overview", VisualID: "parts-table", FormatKey: input.FormatKey, FormatValue: &input.FormatValue}}
		_, revision, err = authoring.ApplyEdit(lifecycle, revision, command, authoring.RevisionID(fmt.Sprintf("demo-%d", input.Number+1)), input.Number+1, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	options, err := authoring.CanonicalVisualFormatOptions(revision.Document.Spec.Visuals["parts"])
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"document": revision.Document, "options": options, "revision": map[string]any{"id": revision.ID, "number": revision.Number, "contentHash": revision.ContentHash}})
}
