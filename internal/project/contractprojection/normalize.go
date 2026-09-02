package contractprojection

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var semanticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func canonicalText(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("contract string is not valid UTF-8")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("contract string contains control character U+%04X", character)
		}
	}
	return norm.NFC.String(value), nil
}

func canonicalTextPointer(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	canonical, err := canonicalText(*value)
	if err != nil {
		return nil, err
	}
	return &canonical, nil
}

func canonicalTexts(values []string) ([]string, error) {
	if values == nil {
		return nil, nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		canonical, err := canonicalText(value)
		if err != nil {
			return nil, err
		}
		result[index] = canonical
	}
	return result, nil
}

func canonicalTextsPointer(values *[]string) (*[]string, error) {
	if values == nil {
		return nil, nil
	}
	canonical, err := canonicalTexts(*values)
	if err != nil {
		return nil, err
	}
	return &canonical, nil
}

func canonicalSet(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		canonical, err := canonicalText(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	sort.Strings(result)
	return result, nil
}

// canonicalURL applies the syntax-only normalizations required by SER-07. It
// intentionally preserves query-pair order and fragments.
func canonicalURL(value string) (string, error) {
	text, err := canonicalText(value)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse authoritative definition URL: %w", err)
	}
	if parsed.Scheme == "" {
		return "", errors.New("authoritative definition URL must be absolute")
	}
	if parsed.User != nil {
		return "", errors.New("authoritative definition URL must not contain user information")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(strings.Trim(hostname, "[]"), port)
	} else {
		parsed.Host = hostname
	}

	path, err := normalizePercentEncoding(parsed.EscapedPath())
	if err != nil {
		return "", fmt.Errorf("normalize URL path: %w", err)
	}
	path = removeDotSegments(path)
	if parsed.Host != "" && path == "" {
		path = "/"
	}
	parsed.Path, err = url.PathUnescape(path)
	if err != nil {
		return "", err
	}
	parsed.RawPath = path
	parsed.RawQuery, err = normalizePercentEncoding(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("normalize URL query: %w", err)
	}
	fragment, err := normalizePercentEncoding(parsed.EscapedFragment())
	if err != nil {
		return "", fmt.Errorf("normalize URL fragment: %w", err)
	}
	parsed.Fragment, err = url.PathUnescape(fragment)
	if err != nil {
		return "", err
	}
	parsed.RawFragment = fragment
	return parsed.String(), nil
}

func normalizePercentEncoding(value string) (string, error) {
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
		high := strings.IndexByte("0123456789abcdef", byte(strings.ToLower(value[index+1 : index+2])[0]))
		low := strings.IndexByte("0123456789abcdef", byte(strings.ToLower(value[index+2 : index+3])[0]))
		if high < 0 || low < 0 {
			return "", errors.New("invalid percent escape")
		}
		decoded := byte(high<<4 | low)
		if isUnreserved(decoded) {
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

func isUnreserved(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value))
}

// removeDotSegments is RFC 3986 section 5.2.4 without collapsing distinct
// empty path segments.
func removeDotSegments(path string) string {
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
			output = removeLastPathSegment(output)
		case input == "/..":
			input = "/"
			output = removeLastPathSegment(output)
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

func removeLastPathSegment(value string) string {
	if index := strings.LastIndexByte(value, '/'); index >= 0 {
		return value[:index]
	}
	return ""
}
