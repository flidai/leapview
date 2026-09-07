package contractprojection

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/semver"
	"golang.org/x/text/unicode/norm"
)

func validSemanticVersion(value string) bool {
	// x/mod/semver performs the numeric-prerelease checks that a permissive
	// regular expression misses (for example, 1.0.0-01). Keep the authored
	// spelling unchanged; this is validation only, not normalization.
	core := strings.SplitN(strings.SplitN(value, "+", 2)[0], "-", 2)[0]
	return len(strings.Split(core, ".")) == 3 && semver.IsValid("v"+value)
}

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
