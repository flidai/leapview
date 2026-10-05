package servergo

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestEmit_GeneratesNonReplayableUIAndRuntimePolicy(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Credentials", Version: "1"},
		Schemas: map[string]ir.Schema{
			"CredentialDraftRequest": {Type: "object", Properties: map[string]ir.SchemaProperty{"value": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"value"}},
			"CredentialDraftAudit":   {Type: "object", Properties: map[string]ir.SchemaProperty{"draftId": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"draftId"}},
		},
		Endpoints: []ir.Endpoint{{
			Method:      "post",
			Path:        "/credential-drafts",
			OperationID: "createCredentialDraft",
			RequestBody: &ir.RequestBody{Required: true, Contents: jsonContent(ir.SchemaRef{Ref: "CredentialDraftRequest"})},
			Responses:   []ir.Response{{StatusCode: 201, Description: "created"}},
			Command: &ir.Command{
				Owner:               "CredentialAPI",
				Idempotency:         "forbidden",
				AuthzMode:           "authenticated",
				AdditionalExposures: []string{"ui"},
				UI:                  &ir.UIAction{ActionID: "credential.draft.create"},
				Audit: ir.AuditPolicy{Required: true, SuccessAction: "credential_draft.created", Guarantee: "transactional", Payload: &ir.AuditPayload{
					Schema: ir.SchemaRef{Ref: "CredentialDraftAudit"}, SchemaVersion: 1, Retention: "security", Fields: []ir.AuditField{{Name: "draftId", Sensitivity: "internal"}},
				}},
				Failures: []ir.CommandFailure{},
			},
		}},
	}

	generated, err := Emit(doc, Options{PackageName: "gen"})
	require.NoError(t, err)
	content := string(generated)
	require.Contains(t, content, `apigenui.MustNonReplayableAction("credential.draft.create", "createCredentialDraft")`)
	require.Contains(t, content, `Idempotency: apigencommand.IdempotencyPolicy(contract.Command.Idempotency)`)
	assertGeneratedServerCompiles(t, generated, `package gen

import "testing"

type GenSchemaCredentialDraftAudit struct { DraftId string }
type GenSchemaCredentialDraftRequest struct { Value string }

func TestGeneratedNonReplayableContract(t *testing.T) {
	action := GenUIActionCreateCredentialDraft()
	if !action.ReplayForbidden() {
		t.Fatal("generated UI action permits replay")
	}
	contract, ok := GetAPIGenCommandRuntimeContract("createCredentialDraft")
	if !ok || string(contract.Idempotency) != "forbidden" {
		t.Fatalf("runtime idempotency policy = %q, found=%v", contract.Idempotency, ok)
	}
}
`)
}
