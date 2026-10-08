package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	agentapp "github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type routerTestTransport func(*http.Request) (*http.Response, error)

func (f routerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOpenRouterDeepSeekUsesSupportedNonThinkingParameter(t *testing.T) {
	for _, tc := range []struct {
		url, model, effort, reasoning string
	}{
		{"https://openrouter.ai/api/v1", "deepseek/deepseek-v4.1-flash", "", `{"enabled":false}`},
		{"https://openrouter.ai/api/v1", "deepseek/deepseek-v4.1-flash", "high", `{"effort":"high"}`},
		{"https://openrouter.ai/api/v1", "deepseek/deepseek-v4.1-flash", "none", `{"effort":"none"}`},
		{"https://openrouter.ai/api/v1", "other/model", "low", `{"effort":"low"}`},
		{"https://openrouter.ai/api/v1", "other/model", "", ""},
		{"https://openrouter.ai.example.com/api/v1", "deepseek/deepseek-v4.1-flash", "high", ""},
	} {
		t.Run(tc.url+tc.model+tc.effort, func(t *testing.T) {
			client := &http.Client{Transport: routerTestTransport(func(r *http.Request) (*http.Response, error) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if tc.reasoning != "" {
					if string(body["reasoning"]) != tc.reasoning {
						t.Fatalf("reasoning=%s", body["reasoning"])
					}
					if _, ok := body["thinking"]; ok {
						t.Fatal("native DeepSeek parameter sent to router")
					}
				} else if _, ok := body["reasoning"]; ok {
					t.Fatal("unrelated provider altered")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`))}, nil
			})}
			_, err := NewModel(agentapp.Config{APIKey: "test", BaseURL: tc.url, Model: tc.model, ReasoningEffort: tc.effort}, client).Complete(context.Background(), agentcore.ModelRequest{}, nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
