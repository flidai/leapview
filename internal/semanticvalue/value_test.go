package semanticvalue

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestValidateAttributeName(t *testing.T) {
	for _, name := range []string{"department", "piiAccess", "allowed_regions_2"} {
		if err := ValidateAttributeName(name); err != nil {
			t.Fatalf("ValidateAttributeName(%q) error = %v", name, err)
		}
	}
	for _, name := range []string{"", " department", "department ", "Department.Name", "9department", "dеpartment"} {
		if err := ValidateAttributeName(name); err == nil {
			t.Fatalf("ValidateAttributeName(%q) accepted invalid name", name)
		}
	}
}

func TestCanonicalizeScalars(t *testing.T) {
	tests := []struct {
		name       string
		typeName   Type
		input      any
		want       string
		wantNative any
	}{
		{name: "string NFC", typeName: TypeString, input: "Cafe\u0301", want: "Café", wantNative: "Café"},
		{name: "boolean", typeName: TypeBoolean, input: true, want: "true", wantNative: true},
		{name: "integer", typeName: TypeInteger, input: int64(math.MinInt64), want: "-9223372036854775808", wantNative: int64(math.MinInt64)},
		{name: "integer JSON", typeName: TypeInteger, input: json.Number("9223372036854775807"), want: "9223372036854775807", wantNative: int64(math.MaxInt64)},
		{name: "decimal exponent", typeName: TypeDecimal, input: json.Number("1.2300e2"), want: "123", wantNative: json.Number("123")},
		{name: "decimal negative zero", typeName: TypeDecimal, input: "-0.000", want: "0", wantNative: json.Number("0")},
		{name: "decimal fractional", typeName: TypeDecimal, input: "9007199254740993.12500", want: "9007199254740993.125", wantNative: json.Number("9007199254740993.125")},
		{name: "date", typeName: TypeDate, input: "2024-02-29", want: "2024-02-29", wantNative: "2024-02-29"},
		{name: "timestamp UTC", typeName: TypeTimestamp, input: "2024-01-02T03:04:05.1200+02:30", want: "2024-01-02T00:34:05.12Z", wantNative: "2024-01-02T00:34:05.12Z"},
		{name: "timestamp nanosecond precision", typeName: TypeTimestamp, input: "2024-01-02T03:04:05.123456789Z", want: "2024-01-02T03:04:05.123456789Z", wantNative: "2024-01-02T03:04:05.123456789Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalize(tt.typeName, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Type() != tt.typeName || got.Canonical() != tt.want {
				t.Fatalf("Canonicalize() = (%q, %q), want (%q, %q)", got.Type(), got.Canonical(), tt.typeName, tt.want)
			}
			if native := got.Native(); native != tt.wantNative {
				t.Fatalf("Native() = %#v, want %#v", native, tt.wantNative)
			}
		})
	}
}

func TestCanonicalizeRejectsInvalidOrCrossTypeValues(t *testing.T) {
	tests := []struct {
		name     string
		typeName Type
		input    any
	}{
		{name: "null", typeName: TypeString, input: nil},
		{name: "invalid UTF8", typeName: TypeString, input: string([]byte{0xff})},
		{name: "C0 control", typeName: TypeString, input: "north\namerica"},
		{name: "C1 control", typeName: TypeString, input: "north\u0085america"},
		{name: "boolean string", typeName: TypeBoolean, input: "true"},
		{name: "integer string", typeName: TypeInteger, input: "1"},
		{name: "integer float", typeName: TypeInteger, input: float64(1)},
		{name: "integer overflow", typeName: TypeInteger, input: json.Number("9223372036854775808")},
		{name: "decimal float", typeName: TypeDecimal, input: float64(1.5)},
		{name: "decimal infinite", typeName: TypeDecimal, input: "Infinity"},
		{name: "bad date", typeName: TypeDate, input: "2023-02-29"},
		{name: "date timestamp", typeName: TypeDate, input: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "timestamp without zone", typeName: TypeTimestamp, input: "2024-01-02T03:04:05"},
		{name: "leap second", typeName: TypeTimestamp, input: "2016-12-31T23:59:60Z"},
		{name: "timestamp ten fractional zeroes", typeName: TypeTimestamp, input: "2024-01-02T03:04:05.1234567890Z"},
		{name: "timestamp ten fractional digits one", typeName: TypeTimestamp, input: "2024-01-02T03:04:05.1234567891Z"},
		{name: "timestamp ten fractional digits nine", typeName: TypeTimestamp, input: "2024-01-02T03:04:05.1234567899Z"},
		{name: "timestamp comma fraction", typeName: TypeTimestamp, input: "2024-01-02T03:04:05,123Z"},
		{name: "timestamp offset without colon", typeName: TypeTimestamp, input: "2024-01-02T03:04:05+0230"},
		{name: "timestamp offset hour out of range", typeName: TypeTimestamp, input: "2024-01-02T03:04:05+24:00"},
		{name: "timestamp offset minute out of range", typeName: TypeTimestamp, input: "2024-01-02T03:04:05+02:60"},
		{name: "unsupported float type", typeName: Type("Float"), input: 1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Canonicalize(tt.typeName, tt.input); err == nil {
				t.Fatalf("Canonicalize(%q, %#v) accepted invalid value", tt.typeName, tt.input)
			}
		})
	}
}

func TestEquivalentTimestampsCanonicalizeConsistently(t *testing.T) {
	utc, err := Canonicalize(TypeTimestamp, "2024-01-02T03:04:05.123456789Z")
	if err != nil {
		t.Fatal(err)
	}
	offset, err := Canonicalize(TypeTimestamp, "2024-01-02T05:34:05.123456789+02:30")
	if err != nil {
		t.Fatal(err)
	}
	if utc.Canonical() != offset.Canonical() || utc.Digest() != offset.Digest() {
		t.Fatalf("equivalent timestamps produced different identities: %q and %q", utc.Canonical(), offset.Canonical())
	}
}

func TestCanonicalizeSetNormalizesAsSortedSet(t *testing.T) {
	got, err := CanonicalizeSet(TypeString, []any{"z", "Cafe\u0301", "Café", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Type() != TypeString || got.Len() != 3 {
		t.Fatalf("set = type %q len %d", got.Type(), got.Len())
	}
	want := []string{"Café", "a", "z"}
	values := got.Values()
	for index := range want {
		if values[index].Canonical() != want[index] {
			t.Fatalf("Values()[%d] = %q, want %q", index, values[index].Canonical(), want[index])
		}
	}
	values[0] = Value{}
	if got.Values()[0].Canonical() != want[0] {
		t.Fatal("Values returned mutable internal storage")
	}
}

func TestCanonicalizeSetRejectsEmptyAndMoreThanBound(t *testing.T) {
	if _, err := CanonicalizeSet(TypeString, nil); err == nil {
		t.Fatal("empty set accepted")
	}
	tooMany := make([]any, MaxSetValues+1)
	for index := range tooMany {
		tooMany[index] = strings.Repeat("x", index+1)
	}
	if _, err := CanonicalizeSet(TypeString, tooMany); err == nil {
		t.Fatalf("set with %d canonical values accepted", len(tooMany))
	}
	duplicates := make([]any, MaxSetValues+1)
	for index := range duplicates {
		duplicates[index] = "same"
	}
	got, err := CanonicalizeSet(TypeString, duplicates)
	if err != nil {
		t.Fatalf("duplicates are bounded after set normalization: %v", err)
	}
	if got.Len() != 1 {
		t.Fatalf("duplicate set len = %d, want 1", got.Len())
	}
}

func TestCanonicalEncodingAndDigestAreStable(t *testing.T) {
	scalar, err := Canonicalize(TypeTimestamp, "2024-01-02T03:04:05+02:30")
	if err != nil {
		t.Fatal(err)
	}
	scalarWant := `{"profile":"leapview.semantic-access/v1","type":"Timestamp","value":"2024-01-02T00:34:05Z"}`
	if got := string(scalar.CanonicalBytes()); got != scalarWant {
		t.Fatalf("scalar CanonicalBytes() = %s, want %s", got, scalarWant)
	}
	if !strings.HasPrefix(scalar.Digest(), "sha256:") || len(scalar.Digest()) != len("sha256:")+64 {
		t.Fatalf("scalar Digest() = %q", scalar.Digest())
	}

	left, err := CanonicalizeSet(TypeString, []any{"sales", "finance", "sales"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalizeSet(TypeString, []any{"finance", "sales"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"profile":"leapview.semantic-access/v1","type":"String","values":["finance","sales"]}`
	if got := string(left.CanonicalBytes()); got != want {
		t.Fatalf("CanonicalBytes() = %s, want %s", got, want)
	}
	if string(right.CanonicalBytes()) != want || left.Digest() != right.Digest() {
		t.Fatal("equivalent sets produced different identities")
	}
	if !strings.HasPrefix(left.Digest(), "sha256:") || len(left.Digest()) != len("sha256:")+64 {
		t.Fatalf("Digest() = %q", left.Digest())
	}
}
