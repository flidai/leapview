package semanticnumeric

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/cockroachdb/apd/v3"
)

// Decimal is the transport-safe representation of an exact base-10 value.
// It is an alias for the canonical string representation already produced by
// Arrow decimal decoding, so JSON, caches, budgets, and visual formatting all
// traverse the same established string boundary.
type Decimal = string

// ValidateCanonicalFixedPoint validates the lexical contract shared by
// authored expressions, PlanIR Decimal literals, and structured Decimal
// filter values. The semantic numeric parser intentionally accepts a wider
// set of decimal spellings for runtime values; this boundary is stricter so
// equivalent authored values have one deterministic representation.
//
// Canonical fixed-point values have an optional leading minus, at least one
// digit on either side of a decimal point when present, no exponent or
// leading plus, and no redundant integer leading zeroes. Negative zero is
// rejected even when it carries a fractional scale.
func ValidateCanonicalFixedPoint(token string) error {
	if token == "" {
		return fmt.Errorf("numeric literal is empty")
	}

	start := 0
	negative := false
	switch token[0] {
	case '-':
		negative = true
		start = 1
	case '+':
		return fmt.Errorf("numeric literal %q must not use a leading plus", token)
	}
	if start == len(token) {
		return fmt.Errorf("numeric literal %q has no digits", token)
	}

	dot := -1
	digitCount := 0
	for index := start; index < len(token); index++ {
		char := token[index]
		switch {
		case char >= '0' && char <= '9':
			digitCount++
		case char == '.' && dot < 0:
			dot = index
		default:
			return fmt.Errorf("numeric literal %q must use canonical fixed-point notation", token)
		}
	}
	if digitCount == 0 || (dot == start || dot == len(token)-1) {
		return fmt.Errorf("numeric literal %q must use canonical fixed-point notation", token)
	}

	integerEnd := len(token)
	if dot >= 0 {
		integerEnd = dot
	}
	integerDigits := token[start:integerEnd]
	if len(integerDigits) > 1 && integerDigits[0] == '0' {
		return fmt.Errorf("numeric literal %q must not have leading zeroes", token)
	}

	if negative {
		zero := true
		for index := start; index < len(token); index++ {
			if token[index] != '0' && token[index] != '.' {
				zero = false
				break
			}
		}
		if zero {
			return fmt.Errorf("numeric literal %q must not be negative zero", token)
		}
	}
	return nil
}

// exactKind tracks whether an exact value originated from an integer or a
// Decimal operand. The distinction is part of the evaluator's transport
// contract: an exact operation with a Decimal operand must remain a Decimal
// string even when its mathematical value is integral.
type exactKind uint8

const (
	exactInteger exactKind = iota + 1
	exactDecimal
)

// Number is a closed numeric value used by governed in-memory evaluation.
// Decimal and integer inputs stay in the exact branch. Only authored/runtime
// Float values enter the approximate branch.
type Number struct {
	exact       *apd.Decimal
	approximate float64
	isFloat     bool
	kind        exactKind
}

var decimalContext = apd.Context{
	Precision:   38,
	MaxExponent: apd.MaxExponent,
	MinExponent: apd.MinExponent,
	Traps:       apd.DefaultTraps,
	Rounding:    apd.RoundHalfEven,
}

// A 38-digit context is sufficient for the stored DECIMAL(38) value but not
// for an exact quotient of two 38-digit operands. Keep division wide until
// the single fixed-scale quantize below so an intermediate rounding cannot
// change a half-even decision at the contract boundary.
var exactQuotientContext = func() apd.Context {
	context := decimalContext
	context.Precision = 76
	return context
}()

// Exact quotient results use the same fixed-point contract as the DuckDB
// renderer. The quotient is rounded half-even to 18 fractional digits while
// retaining DECIMAL(38) total precision; overflow is returned as an error.
const exactQuotientScale int32 = 18

var exactContext = func() apd.Context {
	context := apd.BaseContext
	context.Rounding = apd.RoundHalfEven
	return context
}()

// Parse parses an authored numeric token. Fixed-point tokens are Decimal while
// integer-shaped tokens retain Integer transport for expression literals.
func Parse(token string) (Number, error) {
	return parseExact(token, tokenKind(token))
}

func parseExact(token string, kind exactKind) (Number, error) {
	value, _, err := apd.BaseContext.NewFromString(token)
	if err != nil || value.Form != apd.Finite {
		if err == nil {
			err = fmt.Errorf("number must be finite")
		}
		return Number{}, fmt.Errorf("invalid decimal %q: %w", token, err)
	}
	return Number{exact: value, kind: kind}, nil
}

func tokenKind(token string) exactKind {
	for _, char := range token {
		if char == '.' || char == 'e' || char == 'E' {
			return exactDecimal
		}
	}
	return exactInteger
}

func FromValue(value any) (Number, error) {
	switch typed := value.(type) {
	case string:
		// Strings are the transport representation of Decimal values. Keep the
		// Decimal kind even when the text has no fractional digits.
		return parseExact(typed, exactDecimal)
	case json.Number:
		return Parse(string(typed))
	case *apd.Decimal:
		if typed == nil || typed.Form != apd.Finite {
			return Number{}, fmt.Errorf("numeric value is not a finite decimal")
		}
		copy := new(apd.Decimal)
		copy.Set(typed)
		return Number{exact: copy, kind: exactDecimal}, nil
	case apd.Decimal:
		copy := new(apd.Decimal)
		copy.Set(&typed)
		return Number{exact: copy, kind: exactDecimal}, nil
	case *big.Int:
		if typed == nil {
			return Number{}, fmt.Errorf("numeric value is nil")
		}
		return Parse(typed.String())
	case big.Int:
		return Parse(typed.String())
	case int:
		return Parse(strconv.FormatInt(int64(typed), 10))
	case int8:
		return Parse(strconv.FormatInt(int64(typed), 10))
	case int16:
		return Parse(strconv.FormatInt(int64(typed), 10))
	case int32:
		return Parse(strconv.FormatInt(int64(typed), 10))
	case int64:
		return Parse(strconv.FormatInt(typed, 10))
	case uint:
		return Parse(strconv.FormatUint(uint64(typed), 10))
	case uint8:
		return Parse(strconv.FormatUint(uint64(typed), 10))
	case uint16:
		return Parse(strconv.FormatUint(uint64(typed), 10))
	case uint32:
		return Parse(strconv.FormatUint(uint64(typed), 10))
	case uint64:
		return Parse(strconv.FormatUint(typed, 10))
	case float32:
		return newFloat(float64(typed))
	case float64:
		return newFloat(typed)
	default:
		return Number{}, fmt.Errorf("expression value %T is not numeric", value)
	}
}

func newFloat(value float64) (Number, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Number{}, fmt.Errorf("float value must be finite")
	}
	return Number{approximate: value, isFloat: true}, nil
}

func (n Number) Value() any {
	if n.isFloat {
		return n.approximate
	}
	if n.exact == nil {
		return nil
	}
	if n.kind == exactInteger {
		if value, err := n.exact.Int64(); err == nil {
			return value
		}
	}
	reduced := new(apd.Decimal)
	reduced.Reduce(n.exact)
	return Decimal(reduced.Text('f'))
}

func (n Number) IsZero() bool {
	if n.isFloat {
		return n.approximate == 0
	}
	return n.exact != nil && n.exact.IsZero()
}

func (n Number) Cmp(other Number) (int, error) {
	if n.isFloat || other.isFloat {
		left, right, err := approximatePair(n, other)
		if err != nil {
			return 0, err
		}
		switch {
		case left < right:
			return -1, nil
		case left > right:
			return 1, nil
		default:
			return 0, nil
		}
	}
	return n.exact.Cmp(other.exact), nil
}

func (n Number) Add(other Number) (Number, error) { return n.binary(other, '+') }
func (n Number) Sub(other Number) (Number, error) { return n.binary(other, '-') }
func (n Number) Mul(other Number) (Number, error) { return n.binary(other, '*') }
func (n Number) Quo(other Number) (Number, error) {
	// Exact authored division follows SQL null-on-zero semantics. Float raw
	// division intentionally remains on the approximate path below, where a
	// non-finite result is rejected by newFloat.
	if !n.isFloat && !other.isFloat && other.IsZero() {
		return Number{}, nil
	}
	return n.binary(other, '/')
}

func (n Number) binary(other Number, operator byte) (Number, error) {
	if n.isFloat || other.isFloat {
		left, right, err := approximatePair(n, other)
		if err != nil {
			return Number{}, err
		}
		var value float64
		switch operator {
		case '+':
			value = left + right
		case '-':
			value = left - right
		case '*':
			value = left * right
		case '/':
			value = left / right
		default:
			return Number{}, fmt.Errorf("unsupported arithmetic operator %q", operator)
		}
		return newFloat(value)
	}
	result := new(apd.Decimal)
	var err error
	switch operator {
	case '+':
		_, err = exactContext.Add(result, n.exact, other.exact)
	case '-':
		_, err = exactContext.Sub(result, n.exact, other.exact)
	case '*':
		_, err = exactContext.Mul(result, n.exact, other.exact)
	case '/':
		_, err = exactQuotientContext.Quo(result, n.exact, other.exact)
		if err == nil {
			_, err = decimalContext.Quantize(result, result, -exactQuotientScale)
		}
	default:
		return Number{}, fmt.Errorf("unsupported arithmetic operator %q", operator)
	}
	if err != nil {
		return Number{}, err
	}
	kind := exactInteger
	if operator == '/' || n.kind == exactDecimal || other.kind == exactDecimal {
		kind = exactDecimal
	}
	return Number{exact: result, kind: kind}, nil
}

func (n Number) Neg() (Number, error) {
	if n.isFloat {
		return newFloat(-n.approximate)
	}
	result := new(apd.Decimal)
	if _, err := exactContext.Neg(result, n.exact); err != nil {
		return Number{}, err
	}
	return Number{exact: result, kind: n.kind}, nil
}

func (n Number) Abs() (Number, error) {
	if n.isFloat {
		return newFloat(math.Abs(n.approximate))
	}
	result := new(apd.Decimal)
	if _, err := exactContext.Abs(result, n.exact); err != nil {
		return Number{}, err
	}
	return Number{exact: result, kind: n.kind}, nil
}

func (n Number) Round(digits int32) (Number, error) {
	if n.isFloat {
		factor := math.Pow10(int(digits))
		return newFloat(math.Round(n.approximate*factor) / factor)
	}
	result := new(apd.Decimal)
	if _, err := decimalContext.Quantize(result, n.exact, -digits); err != nil {
		return Number{}, err
	}
	return Number{exact: result, kind: n.kind}, nil
}

func (n Number) Int32() (int32, error) {
	if n.isFloat {
		if math.Trunc(n.approximate) != n.approximate || n.approximate < math.MinInt32 || n.approximate > math.MaxInt32 {
			return 0, fmt.Errorf("number %v is not an int32", n.approximate)
		}
		return int32(n.approximate), nil
	}
	value, err := n.exact.Int64()
	if err != nil || value < math.MinInt32 || value > math.MaxInt32 {
		return 0, fmt.Errorf("number %s is not an int32", n.exact)
	}
	return int32(value), nil
}

func approximatePair(left, right Number) (float64, float64, error) {
	l, err := left.float64()
	if err != nil {
		return 0, 0, err
	}
	r, err := right.float64()
	return l, r, err
}

func (n Number) float64() (float64, error) {
	if n.isFloat {
		return n.approximate, nil
	}
	value, err := n.exact.Float64()
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("decimal %s cannot be represented as Float", n.exact)
	}
	return value, nil
}
