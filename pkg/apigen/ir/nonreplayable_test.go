package ir

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateForbiddenIdempotencyPolicy(t *testing.T) {
	validDocument := func() Document {
		return Document{
			SchemaVersion: CurrentSchemaVersion,
			API:           API{BasePath: "/"},
			Info:          Info{Title: "Credentials", Version: "1"},
			Schemas: map[string]Schema{
				"CredentialDraftRequest": {Type: "object", Properties: map[string]SchemaProperty{"value": {Schema: SchemaRef{Type: "string"}}}, Required: []string{"value"}},
				"CredentialDraftAudit":   {Type: "object", Properties: map[string]SchemaProperty{"draftId": {Schema: SchemaRef{Type: "string"}}}, Required: []string{"draftId"}},
			},
			Endpoints: []Endpoint{{
				Method:      "post",
				Path:        "/credential-drafts",
				OperationID: "createCredentialDraft",
				RequestBody: &RequestBody{Required: true, Contents: []BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &SchemaRef{Ref: "CredentialDraftRequest"}}}},
				Responses:   []Response{{StatusCode: 201, Description: "created"}},
				Command: &Command{
					Owner:       "CredentialAPI",
					Idempotency: "forbidden",
					AuthzMode:   "authenticated",
					Audit: AuditPolicy{Required: true, SuccessAction: "credential_draft.created", Guarantee: "transactional", Payload: &AuditPayload{
						Schema: SchemaRef{Ref: "CredentialDraftAudit"}, SchemaVersion: 1, Retention: "security", Fields: []AuditField{{Name: "draftId", Sensitivity: "internal"}},
					}},
					Failures: []CommandFailure{},
				},
			}},
		}
	}
	require.NoError(t, Validate(validDocument()))

	tests := []struct {
		name    string
		mutate  func(*Document)
		wantErr string
	}{
		{name: "non post", mutate: func(doc *Document) { doc.Endpoints[0].Method = "put" }, wantErr: "forbidden idempotency requires POST"},
		{name: "missing body", mutate: func(doc *Document) { doc.Endpoints[0].RequestBody = nil }, wantErr: "requires a required JSON request body"},
		{name: "optional body", mutate: func(doc *Document) { doc.Endpoints[0].RequestBody.Required = false }, wantErr: "requires a required JSON request body"},
		{name: "non json body", mutate: func(doc *Document) {
			doc.Endpoints[0].RequestBody.Contents[0] = BodyContent{ContentType: "text/plain", BodyKind: "text", Schema: &SchemaRef{Type: "string"}}
		}, wantErr: "requires a required JSON request body"},
		{name: "optional idempotency header", mutate: func(doc *Document) {
			doc.Endpoints[0].Parameters = []Parameter{{Name: "Idempotency-Key", In: "header", Schema: SchemaRef{Type: "string"}}}
		}, wantErr: "disallows any Idempotency-Key header"},
		{name: "async", mutate: func(doc *Document) {
			doc.Endpoints[0].Command.Execution = &AsyncExecution{Mode: "async", Guarantee: "transactional", JobKind: "credential_draft.create", ResourceKind: "credential_draft", InitialEvent: "credential_draft.creating", InitialState: "creating", StatusOperation: "getCredentialDraft", EventsOperation: "listCredentialDraftEvents", Cancellation: "unsupported"}
			doc.Endpoints[0].Responses = []Response{{StatusCode: 202, Description: "accepted"}}
			doc.Endpoints = append(doc.Endpoints,
				Endpoint{Method: "get", Path: "/credential-drafts/{draft}", OperationID: "getCredentialDraft", Kind: "query", Responses: []Response{{StatusCode: 200, Description: "ok"}}},
				Endpoint{Method: "get", Path: "/credential-drafts/{draft}/events", OperationID: "listCredentialDraftEvents", Kind: "query", Responses: []Response{{StatusCode: 200, Description: "ok"}}},
			)
		}, wantErr: "does not support async execution"},
		{name: "best effort audit", mutate: func(doc *Document) { doc.Endpoints[0].Command.Audit.Guarantee = "best-effort" }, wantErr: "requires required transactional audit"},
		{name: "unaudited", mutate: func(doc *Document) { doc.Endpoints[0].Command.Audit.Required = false }, wantErr: "requires required transactional audit"},
		{name: "no auth", mutate: func(doc *Document) { doc.Endpoints[0].Command.AuthzMode = "none" }, wantErr: "requires authenticated or privilege authorization"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := validDocument()
			test.mutate(&doc)
			require.ErrorContains(t, Validate(doc), test.wantErr)
		})
	}
}
