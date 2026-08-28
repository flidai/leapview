package queryaudit

import (
	"regexp"

	"github.com/flidai/leapview/internal/platform/safetext"
)

const redactedValue = safetext.Replacement

var sensitiveSQLSingleQuotedValuePattern = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|client_secret|secret_access_key|access_token|refresh_token|api_key|authorization)\b(\s*(?:=>|=)?\s*)'((?:''|[^'])*)'`)

func RedactSensitiveText(text string) string {
	if text == "" {
		return ""
	}
	text = sensitiveSQLSingleQuotedValuePattern.ReplaceAllString(text, "$1$2'"+redactedValue+"'")
	return safetext.Credentials(text)
}
