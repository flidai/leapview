package api

import apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"

// AgentConfigRevision excludes transient health and connection-test results so
// testing a candidate does not invalidate the configuration's concurrency token.
func AgentConfigRevision(details AdminAgentResponse) (string, error) {
	return apigencommand.RevisionToken(struct {
		Revision                                               int64
		AdminManaged                                           bool
		Enabled                                                bool
		Model, BaseURL, APIMode, ReasoningEffort, SystemPrompt string
	}{details.ConfigurationRevision, details.AdminManaged, details.Enabled, details.Model, details.BaseURL, details.APIMode, details.ReasoningEffort, details.SystemPrompt})
}
