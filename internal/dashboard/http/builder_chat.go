package http

import "strings"

// chatDashboardDraftSlug separates same-title chat drafts while retaining the
// stable create request identity for retries and any explicitly authored slug.
func chatDashboardDraftSlug(embed, slug, requestID string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" && embed == "chat" {
		return "chat-" + requestID
	}
	return slug
}
