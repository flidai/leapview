package duckdbsql

import (
	"encoding/json"
	"math/big"
	"strconv"
	"testing"
)

func TestPlatformIntegerBoundaries(t *testing.T) {
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(strconv.IntSize-1)), big.NewInt(1))
	min := new(big.Int).Neg(new(big.Int).Add(new(big.Int).Set(max), big.NewInt(1)))
	for _, n := range []*big.Int{min, max, big.NewInt(-2147483648), big.NewInt(2147483647), big.NewInt(0)} {
		value, err := intValue(json.RawMessage(n.String()))
		if err != nil || strconv.Itoa(value) != n.String() {
			t.Fatalf("int%d(%s)=%d: %v", strconv.IntSize, n, value, err)
		}
	}
	for _, value := range []string{new(big.Int).Sub(min, big.NewInt(1)).String(), new(big.Int).Add(max, big.NewInt(1)).String(), "9223372036854775808", "-9223372036854775809", "1.5", "1e2", "true", "[]", "{}", "null", "broken", ""} {
		if _, err := intValue(json.RawMessage(value)); err == nil {
			t.Errorf("accepted invalid int%d: %q", strconv.IntSize, value)
		}
	}
	for _, value := range []string{"2147483648", "-2147483649"} {
		_, err := intValue(json.RawMessage(value))
		if (err != nil) != (strconv.IntSize == 32) {
			t.Errorf("int%d(%s): %v", strconv.IntSize, value, err)
		}
	}
}

func TestInt64DecodingIsIndependentOfPlatformWidth(t *testing.T) {
	for _, value := range []string{"-9223372036854775808", "9223372036854775807", "2147483648"} {
		got, err := int64Value(json.RawMessage(value))
		if err != nil || strconv.FormatInt(got, 10) != value {
			t.Fatalf("int64(%s)=%d: %v", value, got, err)
		}
	}
	for _, value := range []string{"9223372036854775808", "-9223372036854775809", "1.5", "broken"} {
		_, err := int64Value(json.RawMessage(value))
		if err == nil {
			t.Errorf("%q: expected malformed decoder error, got %v", value, err)
		}
	}
}
