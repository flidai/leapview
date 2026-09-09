// Package successor contains the isolated, in-memory successor recovery
// evidence contracts. It deliberately has no persistence or admission hooks.
package successor

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/pkg/strictjson"
	"golang.org/x/text/unicode/norm"
)

const (
	RecoverySetVersion       int32 = 3
	ManagedManifestVersion   int32 = 2
	SourceAnchorVersion      int32 = 2
	ProviderProfileVersion   int32 = 2
	ReceiptVersion           int32 = 2
	ReceiptCoreVersion       int32 = 2
	AuthorityRegistryVersion int32 = 2

	MaxDocumentBytes = 8 << 20
	MaxSetBytes      = 1 << 20
	MaxMembers       = 10000
	MaxProfiles      = 10000
	MaxNamespaces    = 10000
	MaxTextBytes     = 4096

	manifestDomain   = "leapview/managed-observations/v2\n"
	closureDomain    = "leapview/managed-closure/v2\n"
	projectionDomain = "leapview/managed-observation-projection/v2\n"
	anchorDomain     = "leapview/recovery-source-anchor/v2\n"
	profileDomain    = "leapview/managed-provider-profiles/v2\n"
	coreDomain       = "leapview/managed-capture-core/v2\n"
	signatureDomain  = "leapview/managed-capture-signature/v2\n"
	receiptDomain    = "leapview/managed-capture-receipt/v2\n"
	frontierDomain   = "leapview/recovery-frontier/v3\n"
	timestampLayout  = "2006-01-02T15:04:05.000000Z"
)

var (
	ErrInvalid            = errors.New("successor recovery contract is invalid")
	ErrUnsupportedVersion = errors.New("successor recovery contract version is unsupported")
	digestPattern         = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	rawHashPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	canonicalInteger      = regexp.MustCompile(`^(0|[1-9][0-9]*|-[1-9][0-9]*)$`)
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

func digest(value string) bool  { return digestPattern.MatchString(value) }
func rawHash(value string) bool { return rawHashPattern.MatchString(value) }

func text(value, label string, max int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max || !utf8.ValidString(value) || norm.NFC.String(value) != value {
		return invalid("%s must be nonempty canonical UTF-8 text", label)
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return invalid("%s contains a control or replacement character", label)
		}
	}
	return nil
}

func id(value, label string) error { return text(value, label, MaxTextBytes) }

func canonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func canonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(timestampLayout, value)
	if err != nil || parsed.IsZero() || parsed.Location() != time.UTC || parsed.Format(timestampLayout) != value {
		return time.Time{}, invalid("timestamp must be UTC with six fractional digits")
	}
	return parsed, nil
}

func hash(domain string, raw []byte) string {
	sum := sha256.Sum256(append([]byte(domain), raw...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func marshal(value any, max int) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("canonical JSON encoding: %v", err)
	}
	if len(raw) > max {
		return nil, invalid("canonical JSON exceeds %d bytes", max)
	}
	// encoding/json replaces invalid UTF-8 in Go strings. Reject that lossy
	// representation, including nested legacy-owner fields, before hashing it.
	if err := rejectNonCanonicalTokens(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// strictDecode bounds and parses JSON while rejecting duplicate (including
// case aliases) keys. Each public parser then checks its exact nested shape.
func strictDecode(raw []byte, target any, max int) error {
	if len(raw) < 2 || len(raw) > max || !utf8.Valid(raw) {
		return invalid("bounded UTF-8 JSON required")
	}
	if err := strictjson.DecodeWithOptions(raw, target, strictjson.Options{
		MaxBytes: int64(max), MaxDepth: 16, DuplicateKeys: strictjson.CaseFoldedKeys,
		AllowUnknownFields: false,
	}); err != nil {
		return invalid("JSON structure: %v", err)
	}
	if err := rejectNonCanonicalTokens(raw); err != nil {
		return err
	}
	return nil
}

func rejectNonCanonicalTokens(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkTokens(decoder); err != nil {
		return err
	}
	return nil
}

func walkTokens(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return invalid("JSON token: %v", err)
	}
	switch value := token.(type) {
	case json.Number:
		if !canonicalInteger.MatchString(value.String()) {
			return invalid("noncanonical number")
		}
	case string:
		if !utf8.ValidString(value) || norm.NFC.String(value) != value || strings.ContainsRune(value, utf8.RuneError) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return invalid("invalid string")
		}
	case json.Delim:
		switch value {
		case '{':
			for decoder.More() {
				if _, err := decoder.Token(); err != nil {
					return invalid("JSON object key: %v", err)
				}
				if err := walkTokens(decoder); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return invalid("JSON object end: %v", err)
			}
		case '[':
			for decoder.More() {
				if err := walkTokens(decoder); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return invalid("JSON array end: %v", err)
			}
		}
	}
	return nil
}

func noNull(raw json.RawMessage, label string) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return invalid("%s must not be null", label)
	}
	return nil
}

func exactObject(raw []byte, fields map[string]bool, required map[string]bool, label string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, invalid("%s must be an object", label)
	}
	objectKeys := make([]string, 0, len(object))
	for key := range object {
		objectKeys = append(objectKeys, key)
	}
	sort.Strings(objectKeys)
	for _, key := range objectKeys {
		if !fields[key] {
			return nil, invalid("%s has unknown field %q", label, key)
		}
	}
	requiredKeys := make([]string, 0, len(required))
	for key := range required {
		requiredKeys = append(requiredKeys, key)
	}
	sort.Strings(requiredKeys)
	for _, key := range requiredKeys {
		value, ok := object[key]
		if !ok {
			return nil, invalid("%s requires field %q", label, key)
		}
		if err := noNull(value, label+"."+key); err != nil {
			return nil, err
		}
	}
	if len(object) < len(required) || len(object) > len(fields) {
		return nil, invalid("%s has missing or extra fields", label)
	}
	return object, nil
}

func base64Exact(value string, size int) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, invalid("invalid canonical base64")
	}
	return decoded, nil
}
