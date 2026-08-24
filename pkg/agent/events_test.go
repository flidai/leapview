package agent

import (
	"context"
	"testing"
)

func TestStreamingTextUsesOneStableOrderedOutputPart(t *testing.T) {
	events := &recordingEvents{}
	model := ModelFunc(func(ctx context.Context, req ModelRequest, stream ModelStream) (ModelResponse, error) {
		if err := stream.Delta(ctx, "hello"); err != nil {
			return ModelResponse{}, err
		}
		return ModelResponse{Content: "hello", FinishReason: FinishReasonStop}, nil
	})
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Events:       events,
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}

	var added, delta, done Event
	for _, event := range events.events {
		switch event.Type {
		case EventTypeOutputPartAdded:
			added = event
		case EventTypeOutputTextDelta:
			delta = event
		case EventTypeOutputPartDone:
			done = event
		}
	}
	if added.OutputKind != OutputPartKindText || added.OutputPartID == "" || added.OutputOrdinal != 0 {
		t.Fatalf("output part added = %#v", added)
	}
	if delta.OutputPartID != added.OutputPartID || delta.ParentMessageID != added.ParentMessageID || delta.Delta != "hello" {
		t.Fatalf("output delta = %#v, added = %#v", delta, added)
	}
	if done.OutputPartID != added.OutputPartID || done.Content != "hello" || done.Severity != SeverityInfo {
		t.Fatalf("output done = %#v, added = %#v", done, added)
	}
}

func TestProviderMetadataIsCopiedToLifecycleEvents(t *testing.T) {
	events := &recordingEvents{}
	model := &fakeModel{responses: []ModelResponse{{
		Content:      "hello",
		FinishReason: FinishReasonStop,
		ProviderMetadata: map[string]any{
			"provider": "openai",
			"model":    "gpt-test",
			"request":  "req_123",
		},
	}}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Events:       events,
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}

	var modelResponse Event
	for _, event := range events.events {
		if event.Type == EventTypeModelResponse {
			modelResponse = event
		}
	}
	if modelResponse.ProviderMetadata["provider"] != "openai" || modelResponse.ProviderMetadata["model"] != "gpt-test" {
		t.Fatalf("model_response metadata = %#v", modelResponse.ProviderMetadata)
	}
	if modelResponse.ProviderMetadata["request"] != "req_123" {
		t.Fatalf("model_response metadata = %#v", modelResponse.ProviderMetadata)
	}
}
