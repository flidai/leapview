// Package semanticvalue implements the canonical scalar and homogeneous-set
// contract shared by semantic access authoring, control-plane attributes,
// runtime evaluation, cache identity, and audit projection.
package semanticvalue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cockroachdb/apd/v3"
	"golang.org/x/text/unicode/norm"
)

const (
	// Profile is the normative semantic-access value profile. It participates
	// in canonical identity so a future profile cannot silently reuse v1
	// authorization-sensitive cache or audit identities.
	Profile = "leapview.semantic-access/v1"

	// MaxSetValues is the fixed post-normalization cardinality bound from the
	// v1 semantic-access contract.
	MaxSetValues = 1024
)

var (
	ErrInvalidType     = errors.New("invalid semantic value type")
	ErrInvalidValue    = errors.New("invalid semantic value")
	ErrEmptySet        = errors.New("semantic value set is empty")
	ErrTooManySetItems = errors.New("semantic value set exceeds cardinality bound")
)

// Type is the closed logical type vocabulary accepted by semantic-access v1.
// Float and the SemanticModel-only Time and timezone-free DateTime types are
// intentionally absent.
type Type string

const (
	TypeString    Type = "String"
	TypeBoolean   Type = "Boolean"
	TypeInteger   Type = "Integer"
	TypeDecimal   Type = "Decimal"
	TypeDate      Type = "Date"
	TypeTimestamp Type = "Timestamp"
)

func (t Type) valid() bool {
	switch t {
	case TypeString, TypeBoolean, TypeInteger, TypeDecimal, TypeDate, TypeTimestamp:
		return true
	default:
		return false
	}
}

// ValidateAttributeName enforces the case-sensitive ASCII identifier grammar
// used by SemanticModel names. Values are never trimmed or case-folded.
func ValidateAttributeName(name string) error {
	if name == "" || !asciiIdentifierStart(name[0]) {
		return fmt.Errorf("%w: attribute name %q must match ^[A-Za-z_][A-Za-z0-9_]*$", ErrInvalidValue, name)
	}
	for index := 1; index < len(name); index++ {
		if !asciiIdentifierContinue(name[index]) {
			return fmt.Errorf("%w: attribute name %q must match ^[A-Za-z_][A-Za-z0-9_]*$", ErrInvalidValue, name)
		}
	}
	return nil
}

func asciiIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func asciiIdentifierContinue(value byte) bool {
	return asciiIdentifierStart(value) || value >= '0' && value <= '9'
}

// Value is one immutable canonical scalar. canonical is the exact byte-level
// representation used for matching and identity; Native returns the strict Go
// representation used by existing planner parameter binding.
type Value struct {
	typeName  Type
	canonical string
}

func (v Value) Type() Type        { return v.typeName }
func (v Value) Canonical() string { return v.canonical }

// Native returns the strict transport value for existing typed consumers.
func (v Value) Native() any {
	switch v.typeName {
	case TypeString, TypeDate, TypeTimestamp:
		return v.canonical
	case TypeBoolean:
		return v.canonical == "true"
	case TypeInteger:
		value, err := strconv.ParseInt(v.canonical, 10, 64)
		if err != nil {
			return nil
		}
		return value
	case TypeDecimal:
		return json.Number(v.canonical)
	default:
		return nil
	}
}

type canonicalValueWire struct {
	Profile string `json:"profile"`
	Type    Type   `json:"type"`
	Value   string `json:"value"`
}

// CanonicalBytes returns the deterministic, profile-qualified scalar identity.
func (v Value) CanonicalBytes() []byte {
	if !v.typeName.valid() {
		return nil
	}
	encoded, err := json.Marshal(canonicalValueWire{Profile: Profile, Type: v.typeName, Value: v.canonical})
	if err != nil {
		return nil
	}
	return encoded
}

// Digest returns the SHA-256 identity of CanonicalBytes.
func (v Value) Digest() string {
	return digest(v.CanonicalBytes())
}

// Canonicalize validates one typed scalar and returns its canonical form.
// Callers must supply the registered logical type; no cross-type coercion is
// performed.
func Canonicalize(typeName Type, input any) (Value, error) {
	if !typeName.valid() {
		return Value{}, fmt.Errorf("%w: %q is not supported by %s", ErrInvalidType, typeName, Profile)
	}
	if input == nil {
		return Value{}, fmt.Errorf("%w: null is not an access value", ErrInvalidValue)
	}

	var (
		canonical string
		err       error
	)
	switch typeName {
	case TypeString:
		canonical, err = canonicalString(input)
	case TypeBoolean:
		canonical, err = canonicalBoolean(input)
	case TypeInteger:
		canonical, err = canonicalInteger(input)
	case TypeDecimal:
		canonical, err = canonicalDecimal(input)
	case TypeDate:
		canonical, err = canonicalDate(input)
	case TypeTimestamp:
		canonical, err = canonicalTimestamp(input)
	}
	if err != nil {
		return Value{}, fmt.Errorf("%w: %s: %v", ErrInvalidValue, typeName, err)
	}
	return Value{typeName: typeName, canonical: canonical}, nil
}

func canonicalString(input any) (string, error) {
	value, ok := input.(string)
	if !ok {
		return "", fmt.Errorf("value of type %T is not a string", input)
	}
	if !utf8.ValidString(value) {
		return "", errors.New("string is not valid UTF-8")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("string contains control character U+%04X", character)
		}
	}
	return norm.NFC.String(value), nil
}

func canonicalBoolean(input any) (string, error) {
	value, ok := input.(bool)
	if !ok {
		return "", fmt.Errorf("value of type %T is not a boolean", input)
	}
	return strconv.FormatBool(value), nil
}

func canonicalInteger(input any) (string, error) {
	var value int64
	switch typed := input.(type) {
	case int:
		value = int64(typed)
	case int8:
		value = int64(typed)
	case int16:
		value = int64(typed)
	case int32:
		value = int64(typed)
	case int64:
		value = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return "", fmt.Errorf("value %d is outside signed 64-bit range", typed)
		}
		value = int64(typed)
	case uint8:
		value = int64(typed)
	case uint16:
		value = int64(typed)
	case uint32:
		value = int64(typed)
	case uint64:
		if typed > math.MaxInt64 {
			return "", fmt.Errorf("value %d is outside signed 64-bit range", typed)
		}
		value = int64(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return "", fmt.Errorf("value %q is not a signed 64-bit integer", typed)
		}
		value = parsed
	default:
		return "", fmt.Errorf("value of type %T is not an integer", input)
	}
	return strconv.FormatInt(value, 10), nil
}

func canonicalDecimal(input any) (string, error) {
	var token string
	switch typed := input.(type) {
	case string:
		token = typed
	case json.Number:
		token = string(typed)
	case int:
		token = strconv.FormatInt(int64(typed), 10)
	case int8:
		token = strconv.FormatInt(int64(typed), 10)
	case int16:
		token = strconv.FormatInt(int64(typed), 10)
	case int32:
		token = strconv.FormatInt(int64(typed), 10)
	case int64:
		token = strconv.FormatInt(typed, 10)
	case uint:
		token = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		token = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		token = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		token = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		token = strconv.FormatUint(typed, 10)
	default:
		return "", fmt.Errorf("value of type %T is not an exact decimal", input)
	}
	value, _, err := apd.BaseContext.NewFromString(token)
	if err != nil || value.Form != apd.Finite {
		if err == nil {
			err = errors.New("decimal must be finite")
		}
		return "", fmt.Errorf("value %q is not an exact decimal: %v", token, err)
	}
	if value.IsZero() {
		return "0", nil
	}
	reduced := new(apd.Decimal)
	reduced.Reduce(value)
	return reduced.Text('f'), nil
}

func canonicalDate(input any) (string, error) {
	value, ok := input.(string)
	if !ok {
		return "", fmt.Errorf("value of type %T is not a date string", input)
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return "", fmt.Errorf("value %q is not a Gregorian YYYY-MM-DD date", value)
	}
	return value, nil
}

func canonicalTimestamp(input any) (string, error) {
	value, ok := input.(string)
	if !ok {
		return "", fmt.Errorf("value of type %T is not an RFC 3339 timestamp string", input)
	}
	if err := validateRFC3339Timestamp(value); err != nil {
		return "", fmt.Errorf("value %q is not a strict RFC 3339 timestamp: %v", value, err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("value %q is not an RFC 3339 timestamp", value)
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func validateRFC3339Timestamp(value string) error {
	if len(value) < len("2006-01-02T15:04:05Z") {
		return errors.New("timestamp is too short")
	}
	for index := 0; index < len("2006-01-02T15:04:05"); index++ {
		switch index {
		case 4, 7:
			if value[index] != '-' {
				return errors.New("date components must use hyphens")
			}
		case 10:
			if value[index] != 'T' {
				return errors.New("date and time must use an uppercase T separator")
			}
		case 13, 16:
			if value[index] != ':' {
				return errors.New("time components must use colons")
			}
		default:
			if !asciiDigit(value[index]) {
				return errors.New("date and time components must contain only ASCII digits")
			}
		}
	}

	timezoneStart := len("2006-01-02T15:04:05")
	if value[timezoneStart] == '.' {
		fractionStart := timezoneStart + 1
		timezoneStart = fractionStart
		for timezoneStart < len(value) && asciiDigit(value[timezoneStart]) {
			timezoneStart++
		}
		fractionDigits := timezoneStart - fractionStart
		if fractionDigits == 0 {
			return errors.New("fractional seconds require at least one digit")
		}
		if fractionDigits > 9 {
			return errors.New("fractional seconds exceed nanosecond precision")
		}
	}

	if timezoneStart >= len(value) {
		return errors.New("timestamp requires a timezone")
	}
	if value[timezoneStart] == 'Z' {
		if timezoneStart != len(value)-1 {
			return errors.New("UTC designator must terminate the timestamp")
		}
		return nil
	}
	if len(value)-timezoneStart != len("+00:00") ||
		(value[timezoneStart] != '+' && value[timezoneStart] != '-') ||
		value[timezoneStart+3] != ':' ||
		!asciiDigit(value[timezoneStart+1]) ||
		!asciiDigit(value[timezoneStart+2]) ||
		!asciiDigit(value[timezoneStart+4]) ||
		!asciiDigit(value[timezoneStart+5]) {
		return errors.New("timezone must be uppercase Z or a signed HH:MM offset")
	}
	offsetHour := 10*int(value[timezoneStart+1]-'0') + int(value[timezoneStart+2]-'0')
	offsetMinute := 10*int(value[timezoneStart+4]-'0') + int(value[timezoneStart+5]-'0')
	if offsetHour >= 24 || offsetMinute >= 60 {
		return errors.New("timezone offset is outside the RFC 3339 range")
	}
	return nil
}

func asciiDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

// Set is an immutable homogeneous set sorted by canonical bytes.
type Set struct {
	typeName Type
	values   []Value
}

func (s Set) Type() Type { return s.typeName }
func (s Set) Len() int   { return len(s.values) }

func (s Set) Values() []Value {
	return append([]Value(nil), s.values...)
}

// CanonicalizeSet canonicalizes, deduplicates, and sorts one homogeneous
// collection. The cardinality limit applies after set normalization.
func CanonicalizeSet(typeName Type, inputs []any) (Set, error) {
	if !typeName.valid() {
		return Set{}, fmt.Errorf("%w: %q is not supported by %s", ErrInvalidType, typeName, Profile)
	}
	if len(inputs) == 0 {
		return Set{}, ErrEmptySet
	}
	unique := make(map[string]Value, len(inputs))
	for index, input := range inputs {
		value, err := Canonicalize(typeName, input)
		if err != nil {
			return Set{}, fmt.Errorf("set value %d: %w", index, err)
		}
		unique[value.canonical] = value
	}
	if len(unique) > MaxSetValues {
		return Set{}, fmt.Errorf("%w: got %d, limit %d", ErrTooManySetItems, len(unique), MaxSetValues)
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]Value, len(keys))
	for index, key := range keys {
		values[index] = unique[key]
	}
	return Set{typeName: typeName, values: values}, nil
}

type canonicalSetWire struct {
	Profile string   `json:"profile"`
	Type    Type     `json:"type"`
	Values  []string `json:"values"`
}

// CanonicalBytes returns the deterministic, profile-qualified set identity.
func (s Set) CanonicalBytes() []byte {
	if !s.typeName.valid() || len(s.values) == 0 {
		return nil
	}
	values := make([]string, len(s.values))
	for index, value := range s.values {
		values[index] = value.canonical
	}
	encoded, err := json.Marshal(canonicalSetWire{Profile: Profile, Type: s.typeName, Values: values})
	if err != nil {
		return nil
	}
	return encoded
}

// Digest returns the SHA-256 identity of CanonicalBytes.
func (s Set) Digest() string {
	return digest(s.CanonicalBytes())
}

func digest(encoded []byte) string {
	if len(encoded) == 0 {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}
