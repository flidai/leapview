package semanticnumeric

import "testing"

func TestExactKindsPreserveDecimalTransport(t *testing.T) {
	integer, err := Parse("1")
	if err != nil {
		t.Fatal(err)
	}
	if got := integer.Value(); got != int64(1) {
		t.Fatalf("integer literal = %#v (%T), want int64(1)", got, got)
	}

	decimal, err := Parse("1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := decimal.Value(); got != Decimal("1") {
		t.Fatalf("Decimal literal = %#v (%T), want Decimal 1", got, got)
	}

	runtimeDecimal, err := FromValue("1")
	if err != nil {
		t.Fatal(err)
	}
	if got := runtimeDecimal.Value(); got != Decimal("1") {
		t.Fatalf("runtime Decimal = %#v (%T), want Decimal 1", got, got)
	}

	for _, test := range []struct {
		name string
		want Decimal
		call func(Number) (Number, error)
	}{
		{name: "add", want: "2", call: func(value Number) (Number, error) { return value.Add(integer) }},
		{name: "subtract", want: "0", call: func(value Number) (Number, error) { return value.Sub(integer) }},
		{name: "multiply", want: "1", call: func(value Number) (Number, error) { return value.Mul(integer) }},
		{name: "round", want: "1", call: func(value Number) (Number, error) { return value.Round(0) }},
		{name: "negate", want: "-1", call: func(value Number) (Number, error) { return value.Neg() }},
		{name: "absolute", want: "1", call: func(value Number) (Number, error) { return value.Abs() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.call(decimal)
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Value(); got != test.want {
				t.Fatalf("result = %#v (%T), want Decimal %s", got, got, test.want)
			}
		})
	}

	integerSum, err := integer.Add(integer)
	if err != nil {
		t.Fatal(err)
	}
	if got := integerSum.Value(); got != int64(2) {
		t.Fatalf("integer sum = %#v (%T), want int64(2)", got, got)
	}

	quotient, err := integer.Quo(integer)
	if err != nil {
		t.Fatal(err)
	}
	if got := quotient.Value(); got != Decimal("1") {
		t.Fatalf("exact quotient = %#v (%T), want Decimal 1", got, got)
	}
}

func TestExactDecimalOperationsNeverRoundTripThroughFloat(t *testing.T) {
	left, err := FromValue("9007199254740993.125")
	if err != nil {
		t.Fatal(err)
	}
	right, err := Parse("0.001")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := left.Add(right)
	if err != nil {
		t.Fatal(err)
	}
	if got := sum.Value(); got != Decimal("9007199254740993.126") {
		t.Fatalf("sum = %#v", got)
	}

	three, _ := Parse("3")
	eight, _ := Parse("8")
	ratio, err := three.Quo(eight)
	if err != nil {
		t.Fatal(err)
	}
	if got := ratio.Value(); got != Decimal("0.375") {
		t.Fatalf("ratio = %#v", got)
	}

	huge, _ := Parse("99999999999999999999999999999999999999.1")
	tenth, _ := Parse("0.9")
	hugeSum, err := huge.Add(tenth)
	if err != nil {
		t.Fatal(err)
	}
	if got := hugeSum.Value(); got != Decimal("100000000000000000000000000000000000000") {
		t.Fatalf("unbounded exact sum = %#v", got)
	}
}

func TestFloatValuesRemainApproximate(t *testing.T) {
	left, err := FromValue(float64(0.1))
	if err != nil {
		t.Fatal(err)
	}
	right, _ := Parse("0.2")
	sum, err := left.Add(right)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sum.Value().(float64); !ok {
		t.Fatalf("Float plus Decimal result type = %T, want float64", sum.Value())
	}
}

func TestExactQuotientUsesNullOnZeroWhileFloatRejectsNonFinite(t *testing.T) {
	one, err := Parse("1")
	if err != nil {
		t.Fatal(err)
	}
	zero, err := Parse("0")
	if err != nil {
		t.Fatal(err)
	}
	quotient, err := one.Quo(zero)
	if err != nil {
		t.Fatal(err)
	}
	if got := quotient.Value(); got != nil {
		t.Fatalf("exact quotient by zero = %#v, want nil", got)
	}

	floatOne, err := FromValue(float64(1))
	if err != nil {
		t.Fatal(err)
	}
	floatZero, err := FromValue(float64(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := floatOne.Quo(floatZero); err == nil {
		t.Fatal("Float quotient by zero was accepted")
	}
}

func TestDecimalQuotientUsesFixedScaleAndHalfEvenRounding(t *testing.T) {
	tests := []struct {
		name, left, right, want string
	}{
		{name: "high precision", left: "9007199254740993.125", right: "1", want: "9007199254740993.125"},
		{name: "repeating", left: "1", right: "6", want: "0.166666666666666667"},
		{name: "half even", left: "1", right: "400000000000000000", want: "0.000000000000000002"},
		// The 19th fractional digit is 4, so direct scale-18 rounding must
		// stay down. A precision-38 quotient would first round the 19th
		// digit (4.9 -> 5) and could then double-round the 18th digit up.
		{name: "near half does not double round", left: "1234567890123456789.12345678901234567749", right: "1", want: "1234567890123456789.123456789012345677"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := Parse(test.left)
			if err != nil {
				t.Fatal(err)
			}
			right, err := Parse(test.right)
			if err != nil {
				t.Fatal(err)
			}
			result, err := left.Quo(right)
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Value(); got != test.want {
				t.Fatalf("quotient = %#v, want %q", got, test.want)
			}
		})
	}
}

func TestDecimalQuotientFailsClosedOnPrecisionOverflow(t *testing.T) {
	left, err := Parse("99999999999999999999999999999999999999")
	if err != nil {
		t.Fatal(err)
	}
	right, err := Parse("0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := left.Quo(right); err == nil {
		t.Fatal("quotient overflow was accepted")
	}
}
