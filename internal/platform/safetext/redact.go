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
	urlUserInfoPattern    = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([^/?#\s@]*:)([^/?#\s@]+)@`)
	urlTokenPattern       = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([^/?#\s@:]+)@`)
	bearerPattern         = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]+`)
	basicPattern          = regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/=_-]+`)
	pemPattern            = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	awsAccessKeyPattern   = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	credentialName        = `(?:authorization|credential|password|passwd|pwd|secret|signature|token|api[_-]?key|access[_-]?key|account[_-]?key|private[_-]?key|client[_-]?secret|shared[_-]?access[_-]?signature|sas|sig)`
	assignmentPattern     = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*` + credentialName + `[A-Za-z0-9_.-]*\s*[:=]\s*)([^\s,;]+)`)
	jsonPattern           = regexp.MustCompile(`(?i)("[^"]*` + credentialName + `[^"]*"\s*:\s*)"(?:\\.|[^"\\])*"`)
	queryPattern          = regexp.MustCompile(`(?i)([?&][A-Za-z0-9_.-]*` + credentialName + `[A-Za-z0-9_.-]*=)([^&#\s]+)`)
	azureSignaturePattern = regexp.MustCompile(`(?i)(SharedAccessSignature\s*=\s*)([^\s]+)`)
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
	value = basicPattern.ReplaceAllString(value, "Basic "+Replacement)
	value = pemPattern.ReplaceAllString(value, Replacement)
	value = awsAccessKeyPattern.ReplaceAllString(value, Replacement)
	value = azureSignaturePattern.ReplaceAllString(value, "$1"+Replacement)
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
