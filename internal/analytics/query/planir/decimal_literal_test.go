package planir

import "testing"

func TestDecimalLiteralRejectsExponentNotation(t *testing.T) {
	for _, token := range []string{"1e-3", "1.2e3", "999999999999999999999999999999999999999e-1", "01", "+1", "-0", "-0.0"} {
		t.Run(token, func(t *testing.T) {
			decimal := Literal{Kind: LiteralNumber, NumberKind: NumberDecimal, NumberText: token}
			if decimal.valid() {
				t.Fatalf("exponent Decimal literal %q passed PlanIR validation", token)
			}
			args := []any{}
			if _, err := bindLiteral(decimal, &args); err == nil {
				t.Fatalf("exponent literal %q was accepted", token)
			}
		})
	}
}

func TestDecimalLiteralAcceptsCanonicalFixedPointForms(t *testing.T) {
	for _, token := range []string{"0", "0.0", "1", "1.0", "-1", "-1.0", "9007199254740993.125"} {
		t.Run(token, func(t *testing.T) {
			decimal := Literal{Kind: LiteralNumber, NumberKind: NumberDecimal, NumberText: token}
			if !decimal.valid() {
				t.Fatalf("canonical Decimal literal %q failed PlanIR validation", token)
			}
			args := []any{}
			if _, err := bindLiteral(decimal, &args); err != nil {
				t.Fatalf("canonical Decimal literal %q failed binding: %v", token, err)
			}
		})
	}
}

func TestFloatLiteralAllowsExponentNotation(t *testing.T) {
	literal := Literal{Kind: LiteralNumber, NumberKind: NumberFloat, NumberText: "1e-3"}
	if !literal.valid() {
		t.Fatal("Float exponent literal failed PlanIR validation")
	}
	args := []any{}
	if _, err := bindLiteral(literal, &args); err != nil {
		t.Fatalf("Float exponent literal failed binding: %v", err)
	}
}

func TestDecimalLiteralRejectsPrecisionOverflow(t *testing.T) {
	args := []any{}
	if _, err := bindLiteral(Literal{Kind: LiteralNumber, NumberKind: NumberDecimal, NumberText: "123456789012345678901234567890123456789"}, &args); err == nil {
		t.Fatal("39-digit decimal literal was accepted")
	}
}
