package contractprojection

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// canonicalURL applies the syntax-only URL normalizations required by SER-07.
// It deliberately leaves query-pair order and fragments significant. Opaque
// URIs (including URNs) are supported alongside hierarchical URLs.
func canonicalURL(value string) (string, error) {
	text, err := canonicalText(value)
	if err != nil {
		return "", err
	}
	if strings.IndexFunc(text, unicode.IsSpace) >= 0 {
		return "", errors.New("authoritative definition URL contains whitespace")
	}

	parsed, err := url.Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse authoritative definition URL: %w", err)
	}
	if !validURLScheme(parsed.Scheme) {
		return "", errors.New("authoritative definition URL must have an absolute URI scheme")
	}
	if parsed.User != nil {
		return "", errors.New("authoritative definition URL must not contain user information")
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hasAuthority := strings.HasPrefix(text[strings.IndexByte(text, ':')+1:], "//")
	hasFragment := strings.ContainsRune(text, '#')
	if hasAuthority {
		if parsed.Host == "" || parsed.Hostname() == "" {
			return "", errors.New("authoritative definition URL authority must contain a host")
		}
		if err := canonicalURLHost(parsed); err != nil {
			return "", err
		}
	}

	if parsed.Opaque != "" {
		parsed.Opaque, err = normalizeURLPercentEncoding(parsed.Opaque)
		if err != nil {
			return "", fmt.Errorf("normalize URL opaque part: %w", err)
		}
	} else {
		path, err := normalizeURLPercentEncoding(parsed.EscapedPath())
		if err != nil {
			return "", fmt.Errorf("normalize URL path: %w", err)
		}
		path = removeURLDotSegments(path)
		if hasAuthority && path == "" {
			path = "/"
		}
		parsed.Path, err = url.PathUnescape(path)
		if err != nil {
			return "", fmt.Errorf("decode URL path: %w", err)
		}
		parsed.RawPath = path
	}

	parsed.RawQuery, err = normalizeURLPercentEncoding(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("normalize URL query: %w", err)
	}
	fragment, err := normalizeURLPercentEncoding(parsed.EscapedFragment())
	if err != nil {
		return "", fmt.Errorf("normalize URL fragment: %w", err)
	}
	parsed.Fragment, err = url.PathUnescape(fragment)
	if err != nil {
		return "", fmt.Errorf("decode URL fragment: %w", err)
	}
	parsed.RawFragment = fragment

	result := parsed.String()
	// net/url has no ForceFragment bit, so retain an explicitly empty
	// fragment marker ("...#" versus "...") after String() drops it.
	if hasFragment && !strings.ContainsRune(result, '#') {
		result += "#"
	}
	return result, nil
}

func validURLScheme(value string) bool {
	if value == "" || (value[0] < 'A' || value[0] > 'Z') && (value[0] < 'a' || value[0] > 'z') {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '+' && character != '-' && character != '.' {
			return false
		}
	}
	return true
}

func canonicalURLHost(parsed *url.URL) error {
	rawHost := parsed.Host
	port := parsed.Port()
	if port != "" {
		for _, character := range port {
			if character < '0' || character > '9' {
				return errors.New("authoritative definition URL port must be numeric")
			}
		}
	}
	host := rawHost
	if strings.HasPrefix(rawHost, "[") {
		end := strings.LastIndexByte(rawHost, ']')
		if end < 0 {
			return errors.New("authoritative definition URL has an invalid bracketed host")
		}
		if suffix := rawHost[end+1:]; suffix != "" && suffix != ":" && port == "" {
			return errors.New("authoritative definition URL port must be numeric")
		}
		host = rawHost[:end+1]
	} else if port != "" {
		suffix := ":" + port
		if !strings.HasSuffix(rawHost, suffix) {
			return errors.New("authoritative definition URL has an invalid port")
		}
		host = rawHost[:len(rawHost)-len(suffix)]
	} else {
		// A trailing colon denotes an empty port. It has no canonical value.
		if colon := strings.LastIndexByte(rawHost, ':'); colon >= 0 && colon != len(rawHost)-1 {
			return errors.New("authoritative definition URL port must be numeric")
		}
		host = strings.TrimSuffix(rawHost, ":")
	}
	host, err := normalizeURLHost(host)
	if err != nil {
		return fmt.Errorf("normalize URL host: %w", err)
	}
	if host == "" {
		return errors.New("authoritative definition URL authority must contain a host")
	}
	if (parsed.Scheme == "http" && isURLPort(port, "80")) || (parsed.Scheme == "https" && isURLPort(port, "443")) {
		port = ""
	}
	parsed.Host = host
	if port != "" {
		parsed.Host += ":" + port
	}
	return nil
}

func normalizeURLHost(value string) (string, error) {
	value = strings.ToLower(value)
	if !strings.HasPrefix(value, "[") {
		return normalizeURLPercentEncoding(value)
	}
	end := strings.LastIndexByte(value, ']')
	if end < 0 {
		return "", errors.New("authoritative definition URL has an invalid bracketed host")
	}
	inner := value[1:end]
	if zone := strings.IndexByte(inner, '%'); zone >= 0 {
		// net/url decodes the RFC 6874 %25 delimiter in URL.Host and escapes
		// it again when String renders the authority. Keep the decoded marker
		// here while retaining the lower-cased zone identifier.
		address, err := normalizeURLPercentEncoding(inner[:zone])
		if err != nil {
			return "", err
		}
		zoneName := inner[zone+1:]
		// url.Parse may leave the RFC 6874 delimiter escaped in Host, while
		// Hostname exposes it decoded. Normalize both spellings to one %25.
		if strings.HasPrefix(zoneName, "25") {
			zoneName = zoneName[2:]
		}
		if zoneName == "" || strings.ContainsRune(zoneName, '%') {
			return "", errors.New("authoritative definition URL has an invalid IPv6 zone")
		}
		return "[" + address + "%" + zoneName + "]", nil
	}
	normalized, err := normalizeURLPercentEncoding(inner)
	if err != nil {
		return "", err
	}
	return "[" + normalized + "]", nil
}

func isURLPort(value, want string) bool {
	trimmed := strings.TrimLeft(value, "0")
	return trimmed != "" && trimmed == want
}

func normalizeURLPercentEncoding(value string) (string, error) {
	const hex = "0123456789ABCDEF"
	var result strings.Builder
	result.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			result.WriteByte(value[index])
			continue
		}
		if index+2 >= len(value) {
			return "", errors.New("incomplete percent escape")
		}
		high := hexValue(value[index+1])
		low := hexValue(value[index+2])
		if high < 0 || low < 0 {
			return "", errors.New("invalid percent escape")
		}
		decoded := byte(high<<4 | low)
		if isURLUnreserved(decoded) {
			result.WriteByte(decoded)
		} else {
			result.WriteByte('%')
			result.WriteByte(hex[high])
			result.WriteByte(hex[low])
		}
		index += 2
	}
	return result.String(), nil
}

func hexValue(value byte) int {
	switch {
	case value >= '0' && value <= '9':
		return int(value - '0')
	case value >= 'a' && value <= 'f':
		return int(value-'a') + 10
	case value >= 'A' && value <= 'F':
		return int(value-'A') + 10
	default:
		return -1
	}
}

func isURLUnreserved(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		strings.ContainsRune("-._~", rune(value))
}

// removeURLDotSegments is RFC 3986 section 5.2.4 without collapsing distinct
// empty path segments.
func removeURLDotSegments(path string) string {
	input := path
	output := ""
	for input != "" {
		switch {
		case strings.HasPrefix(input, "../"):
			input = input[3:]
		case strings.HasPrefix(input, "./"):
			input = input[2:]
		case strings.HasPrefix(input, "/./"):
			input = "/" + input[3:]
		case input == "/.":
			input = "/"
		case strings.HasPrefix(input, "/../"):
			input = "/" + input[4:]
			output = removeURLLastPathSegment(output)
		case input == "/..":
			input = "/"
			output = removeURLLastPathSegment(output)
		case input == "." || input == "..":
			input = ""
		default:
			length := len(input)
			start := 0
			if input[0] == '/' {
				start = 1
			}
			if next := strings.IndexByte(input[start:], '/'); next >= 0 {
				length = start + next
			}
			output += input[:length]
			input = input[length:]
		}
	}
	return output
}

func removeURLLastPathSegment(value string) string {
	if index := strings.LastIndexByte(value, '/'); index >= 0 {
		return value[:index]
	}
	return ""
}
