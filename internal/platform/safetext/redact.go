// Package safetext removes credential-shaped values from bounded operational text.
package safetext

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const Replacement = "[REDACTED]"

var (
	urlUserInfoPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([^/?#\s@]*:)([^/?#\s@]+)@`)
	urlTokenPattern    = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([^/?#\s@:]+)@`)
	bearerPattern      = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]+`)
	assignmentPattern  = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:authorization|credential|password|passwd|secret|signature|token|api[_-]?key)[A-Za-z0-9_.-]*\s*[:=]\s*)([^\s,;]+)`)
	jsonPattern        = regexp.MustCompile(`(?i)("[^"]*(?:authorization|credential|password|passwd|secret|signature|token|api[_-]?key)[^"]*"\s*:\s*)"(?:\\.|[^"\\])*"`)
	queryPattern       = regexp.MustCompile(`(?i)([?&][A-Za-z0-9_.-]*(?:authorization|credential|password|passwd|secret|signature|token|api[_-]?key)[A-Za-z0-9_.-]*=)([^&#\s]+)`)
)

// Credentials removes common URL, DSN, JSON, assignment, bearer, provider,
// and signed-URL credential representations. Callers should still prefer
// allowlisted failure codes and reviewed summaries over arbitrary errors.
func Credentials(value string) string {
	if value == "" {
		return ""
	}
	value = urlUserInfoPattern.ReplaceAllString(value, "$1$2"+Replacement+"@")
	value = urlTokenPattern.ReplaceAllString(value, "$1"+Replacement+"@")
	value = bearerPattern.ReplaceAllString(value, "Bearer "+Replacement)
	value = jsonPattern.ReplaceAllString(value, "$1\""+Replacement+"\"")
	value = queryPattern.ReplaceAllString(value, "$1"+url.QueryEscape(Replacement))
	return assignmentPattern.ReplaceAllString(value, "$1"+Replacement)
}

// BoundedSummary sanitizes arbitrary text, collapses multiline output, and
// truncates at a valid UTF-8 boundary.
func BoundedSummary(value string, limit int) string {
	value = strings.Join(strings.Fields(Credentials(value)), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
