package agent

import "strings"

type Config struct {
	APIKey          string
	BaseURL         string
	Model           string
	ReasoningEffort string
}

func (c Config) Enabled() bool {
	return strings.TrimSpace(c.APIKey) != "" && strings.TrimSpace(c.Model) != ""
}

func (c Config) NormalizedBaseURL() string {
	if strings.TrimSpace(c.BaseURL) == "" {
		return "https://api.openai.com/v1"
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c Config) NormalizedReasoningEffort() string {
	if effort := strings.ToLower(strings.TrimSpace(c.ReasoningEffort)); effort != "" {
		return effort
	}
	return "high"
}
