package agent

import (
	"fmt"
	"net/url"
	"strings"
)

type Config struct {
	APIMode         string
	Revision        int64
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
	return ""
}

func (c Config) Validate(enabled bool) error {
	if len(c.Model) > 256 || len(c.BaseURL) > 2048 || len(c.APIKey) > 16384 {
		return fmt.Errorf("agent provider configuration exceeds supported field limits")
	}
	if c.APIMode != "" && c.APIMode != "responses" && c.APIMode != "chat-completions" {
		return fmt.Errorf("apiMode must be responses or chat-completions")
	}
	if enabled && !c.Enabled() {
		return fmt.Errorf("enabled agent configuration requires apiKey and model")
	}
	if baseURL := strings.TrimSpace(c.BaseURL); baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("agent baseUrl must be an HTTP(S) origin or path without credentials, query, or fragment")
		}
	}
	switch c.NormalizedReasoningEffort() {
	case "", "none", "low", "medium", "high", "xhigh", "max":
		return nil
	default:
		return fmt.Errorf("agent reasoningEffort must be none, low, medium, high, xhigh, or max")
	}
}
