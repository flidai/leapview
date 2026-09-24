package openai

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

type scopedProviderState struct {
	Identity string          `json:"identity"`
	State    json.RawMessage `json:"state"`
}

func (m *OpenAIModel) stateIdentity() string {
	mac := hmac.New(sha256.New, []byte(m.config.APIKey))
	mac.Write([]byte(m.config.NormalizedBaseURL() + "\x00" + m.config.Model + "\x00responses"))
	return hex.EncodeToString(mac.Sum(nil))
}
func (m *OpenAIModel) scopeResponse(response agentcore.ModelResponse) agentcore.ModelResponse {
	if len(response.ProviderState) > 0 {
		response.ProviderState, _ = json.Marshal(scopedProviderState{Identity: m.stateIdentity(), State: response.ProviderState})
	}
	return response
}
func (m *OpenAIModel) compatibleMessages(messages []agentcore.Message) []agentcore.Message {
	result := append([]agentcore.Message(nil), messages...)
	for i := range result {
		var state scopedProviderState
		if json.Unmarshal(result[i].ProviderState, &state) == nil && state.Identity == m.stateIdentity() {
			result[i].ProviderState = state.State
		} else {
			result[i].ProviderState = nil
		}
	}
	return result
}
