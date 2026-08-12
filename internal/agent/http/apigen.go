package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

// APIGenDispatcher adapts Agent's HTTP handler to its generated transport
// contract. It deliberately lives with the capability transport instead of in
// the application composition root.
type APIGenDispatcher struct {
	handler *Handler
}

func NewAPIGenDispatcher(handler *Handler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

type APIGenTransportErrorResponder struct {
	Logger *slog.Logger
}

func (responder APIGenTransportErrorResponder) RespondTransportError(ctx context.Context, w stdhttp.ResponseWriter, r *stdhttp.Request, failure agentgen.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func (d *APIGenDispatcher) UpdateAgentConfig(w stdhttp.ResponseWriter, r *stdhttp.Request, headers agentgen.GenUpdateAgentConfigHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdateAgentConfig(w, r)
}

func (d *APIGenDispatcher) GetAgentConfig(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.GetAgentConfig(w, r)
}

func (d *APIGenDispatcher) ListAgentConversations(w stdhttp.ResponseWriter, r *stdhttp.Request, _ agentgen.GenListAgentConversationsParams) {
	d.handler.ListConversations(w, r)
}

func (d *APIGenDispatcher) CreateAgentConversation(w stdhttp.ResponseWriter, r *stdhttp.Request, _ agentgen.GenCreateAgentConversationHeaders) {
	d.handler.CreateConversation(w, r)
}

func (d *APIGenDispatcher) GetAgentConversation(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetConversation(w, r)
}

func (d *APIGenDispatcher) UpdateAgentConversation(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, headers agentgen.GenUpdateAgentConversationHeaders) {
	r.Header.Set("If-Match", headers.IfMatch)
	d.handler.UpdateConversation(w, r)
}

func (d *APIGenDispatcher) ArchiveAgentConversation(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.ArchiveConversation(w, r)
}

func (d *APIGenDispatcher) ListAgentMessages(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ agentgen.GenListAgentMessagesParams) {
	d.handler.ListMessages(w, r)
}

func (d *APIGenDispatcher) CreateAgentRun(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ agentgen.GenCreateAgentRunHeaders) {
	d.handler.CreateRun(w, r)
}

func (d *APIGenDispatcher) ListAgentRuns(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ agentgen.GenListAgentRunsParams) {
	d.handler.ListRuns(w, r)
}

func (d *APIGenDispatcher) GetAgentRun(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetRun(w, r)
}

func (d *APIGenDispatcher) ListAgentEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ agentgen.GenListAgentEventsParams, _ agentgen.GenListAgentEventsHeaders) {
	d.handler.ListEvents(w, r)
}

func (d *APIGenDispatcher) CancelAgentRun(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ agentgen.GenCancelAgentRunHeaders) {
	d.handler.CancelRun(w, r)
}
