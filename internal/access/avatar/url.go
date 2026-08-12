package avatar

import (
	"net/url"
	"strings"
)

// URL returns the canonical content-addressed path for avatar metadata.
func URL(value Metadata) string {
	principalID := strings.TrimSpace(value.PrincipalID)
	digest := strings.ToLower(strings.TrimSpace(value.SHA256))
	if principalID == "" || digest == "" {
		return ""
	}
	return "/profile/avatars/" + url.PathEscape(principalID) + "/" + digest
}

// URLForPrincipal builds the canonical path when the caller already knows the
// principal and the metadata reader only returns content fields.
func URLForPrincipal(principalID string, value Metadata) string {
	if strings.TrimSpace(value.PrincipalID) == "" {
		value.PrincipalID = principalID
	}
	return URL(value)
}
