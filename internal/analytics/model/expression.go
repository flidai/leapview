package model

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

type Expression struct {
	root  expressionNode
	refs  []string
	calls []string
}

// ScalarExpressionNode is the immutable, renderer-neutral view of a parsed
// scalar expression. Number is retained as its source token so downstream
// typed plans do not round-trip decimal literals through float64.
type ScalarExpressionKind string
type ScalarExpressionOperator string
type ScalarExpressionFunction string

type ScalarExpressionNode struct {
	Kind     ScalarExpressionKind
	Metric   string
	Number   string
	Operator ScalarExpressionOperator
	Function ScalarExpressionFunction
	Children []ScalarExpressionNode
}

const (
	ScalarExpressionMetric ScalarExpressionKind = "metric_ref"
	ScalarExpressionNumber ScalarExpressionKind = "number"
	ScalarExpressionUnary  ScalarExpressionKind = "unary"
	ScalarExpressionBinary ScalarExpressionKind = "binary"
	ScalarExpressionCall   ScalarExpressionKind = "function"
)

// Tree returns a detached typed AST for a parsed expression. Keeping this
// conversion beside the parser ensures every planner consumes the same
// grammar and function/arity checks.
func (e Expression) Tree() (ScalarExpressionNode, error) {
	if e.root == nil {
		return ScalarExpressionNode{}, fmt.Errorf("expression is not parsed")
	}
	return expressionTree(e.root)
}

func expressionTree(node expressionNode) (ScalarExpressionNode, error) {
	var out ScalarExpressionNode
	var children []expressionNode
	switch value := node.(type) {
	case expressionRef:
		out.Kind, out.Metric = ScalarExpressionMetric, string(value)
	case expressionNumber:
		out.Kind, out.Number = ScalarExpressionNumber, string(value)
	case expressionUnary:
		out.Kind, out.Operator = ScalarExpressionUnary, ScalarExpressionOperator(value.op)
		children = []expressionNode{value.node}
	case expressionBinary:
		out.Kind, out.Operator = ScalarExpressionBinary, ScalarExpressionOperator(value.op)
		children = []expressionNode{value.left, value.right}
	case expressionCall:
		out.Kind, out.Function = ScalarExpressionCall, ScalarExpressionFunction(value.name)
		children = value.args
	default:
		return ScalarExpressionNode{}, fmt.Errorf("unsupported expression node %T", node)
	}
	if len(children) != 0 {
		out.Children = make([]ScalarExpressionNode, len(children))
		for index, child := range children {
			converted, err := expressionTree(child)
			if err != nil {
				return ScalarExpressionNode{}, err
			}
			out.Children[index] = converted
		}
	}
	return out, nil
}

func ParseExpression(input string) (Expression, error) {
	p := expressionParser{input: strings.TrimSpace(input)}
	if p.input == "" {
		return Expression{}, fmt.Errorf("expression is required")
	}
	root, err := p.parseAdditive()
	if err != nil {
		return Expression{}, err
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return Expression{}, fmt.Errorf("unexpected token at position %d", p.pos+1)
	}
	return Expression{root: root, refs: append([]string{}, p.refs...), calls: append([]string{}, p.calls...)}, nil
}

func (e Expression) References() []string {
	return append([]string{}, e.refs...)
}

func (e Expression) Functions() []string {
	return append([]string{}, e.calls...)
}

func (e Expression) SQL(resolve func(string) (string, error)) (string, error) {
	if e.root == nil {
		return "", fmt.Errorf("expression is not parsed")
	}
	return e.root.sql(resolve)
}

// Evaluate applies the parsed scalar expression to already aggregated values.
func (e Expression) Evaluate(resolve func(string) (any, error)) (any, error) {
	if e.root == nil {
		return nil, fmt.Errorf("expression is not parsed")
	}
	return e.root.evaluate(resolve)
}

type expressionNode interface {
	sql(func(string) (string, error)) (string, error)
	evaluate(func(string) (any, error)) (any, error)
}

type expressionRef string

func (n expressionRef) sql(resolve func(string) (string, error)) (string, error) {
	return resolve(string(n))
}

func (n expressionRef) evaluate(resolve func(string) (any, error)) (any, error) {
	return resolve(string(n))
}

type expressionNumber string

func (n expressionNumber) sql(_ func(string) (string, error)) (string, error) {
	return string(n), nil
}

func (n expressionNumber) evaluate(_ func(string) (any, error)) (any, error) {
	return strconv.ParseFloat(string(n), 64)
}

type expressionUnary struct {
	op   byte
	node expressionNode
}

func (n expressionUnary) sql(resolve func(string) (string, error)) (string, error) {
	value, err := n.node.sql(resolve)
	if err != nil {
		return "", err
	}
	return "(" + string(n.op) + value + ")", nil
}

func (n expressionUnary) evaluate(resolve func(string) (any, error)) (any, error) {
	value, err := n.node.evaluate(resolve)
	if err != nil || value == nil {
		return value, err
	}
	number, err := expressionFloat(value)
	if err != nil {
		return nil, err
	}
	if n.op == '-' {
		return -number, nil
	}
	return number, nil
}

type expressionBinary struct {
	op          byte
	left, right expressionNode
}

func (n expressionBinary) sql(resolve func(string) (string, error)) (string, error) {
	left, err := n.left.sql(resolve)
	if err != nil {
		return "", err
	}
	right, err := n.right.sql(resolve)
	if err != nil {
		return "", err
	}
	return "(" + left + " " + string(n.op) + " " + right + ")", nil
}

func (n expressionBinary) evaluate(resolve func(string) (any, error)) (any, error) {
	left, err := n.left.evaluate(resolve)
	if err != nil {
		return nil, err
	}
	right, err := n.right.evaluate(resolve)
	if err != nil {
		return nil, err
	}
	if left == nil || right == nil {
		return nil, nil
	}
	l, err := expressionFloat(left)
	if err != nil {
		return nil, err
	}
	r, err := expressionFloat(right)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case '+':
		return l + r, nil
	case '-':
		return l - r, nil
	case '*':
		return l * r, nil
	case '/':
		return l / r, nil
	default:
		return nil, fmt.Errorf("unsupported arithmetic operator %q", n.op)
	}
}

type expressionCall struct {
	name string
	args []expressionNode
}

func (n expressionCall) sql(resolve func(string) (string, error)) (string, error) {
	args := make([]string, 0, len(n.args))
	for _, arg := range n.args {
		value, err := arg.sql(resolve)
		if err != nil {
			return "", err
		}
		args = append(args, value)
	}
	if n.name == "safe_divide" {
		return "(" + args[0] + " / NULLIF(" + args[1] + ", 0))", nil
	}
	return strings.ToUpper(n.name) + "(" + strings.Join(args, ", ") + ")", nil
}

func (n expressionCall) evaluate(resolve func(string) (any, error)) (any, error) {
	args := make([]any, len(n.args))
	for i, arg := range n.args {
		value, err := arg.evaluate(resolve)
		if err != nil {
			return nil, err
		}
		args[i] = value
	}
	switch n.name {
	case "coalesce":
		for _, value := range args {
			if value != nil {
				return value, nil
			}
		}
		return nil, nil
	case "nullif":
		if args[0] == nil || args[1] == nil {
			return args[0], nil
		}
		left, err := expressionFloat(args[0])
		if err != nil {
			return nil, err
		}
		right, err := expressionFloat(args[1])
		if err != nil {
			return nil, err
		}
		if left == right {
			return nil, nil
		}
		return args[0], nil
	case "abs":
		if args[0] == nil {
			return nil, nil
		}
		value, err := expressionFloat(args[0])
		return math.Abs(value), err
	case "round":
		if args[0] == nil {
			return nil, nil
		}
		value, err := expressionFloat(args[0])
		if err != nil {
			return nil, err
		}
		digits := 0
		if len(args) == 2 {
			if args[1] == nil {
				return nil, nil
			}
			digitValue, digitErr := expressionFloat(args[1])
			if digitErr != nil {
				return nil, digitErr
			}
			digits = int(digitValue)
		}
		factor := math.Pow10(digits)
		return math.Round(value*factor) / factor, nil
	case "safe_divide":
		if args[0] == nil || args[1] == nil {
			return nil, nil
		}
		numerator, err := expressionFloat(args[0])
		if err != nil {
			return nil, err
		}
		denominator, err := expressionFloat(args[1])
		if err != nil {
			return nil, err
		}
		if denominator == 0 {
			return nil, nil
		}
		return numerator / denominator, nil
	default:
		return nil, fmt.Errorf("unsupported expression function %q", n.name)
	}
}

func expressionFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case int:
		return float64(typed), nil
	case int8:
		return float64(typed), nil
	case int16:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case uint:
		return float64(typed), nil
	case uint8:
		return float64(typed), nil
	case uint16:
		return float64(typed), nil
	case uint32:
		return float64(typed), nil
	case uint64:
		return float64(typed), nil
	case float32:
		return float64(typed), nil
	case float64:
		return typed, nil
	case interface{ Float64() float64 }:
		return typed.Float64(), nil
	default:
		return 0, fmt.Errorf("expression value %T is not numeric", value)
	}
}

type expressionParser struct {
	input     string
	pos       int
	refs      []string
	calls     []string
	seen      map[string]struct{}
	seenCalls map[string]struct{}
}

func (p *expressionParser) parseAdditive() (expressionNode, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if !p.peek('+') && !p.peek('-') {
			return left, nil
		}
		op := p.input[p.pos]
		p.pos++
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		left = expressionBinary{op: op, left: left, right: right}
	}
}

func (p *expressionParser) parseMultiplicative() (expressionNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if !p.peek('*') && !p.peek('/') {
			return left, nil
		}
		op := p.input[p.pos]
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = expressionBinary{op: op, left: left, right: right}
	}
}

func (p *expressionParser) parseUnary() (expressionNode, error) {
	p.skipSpace()
	if p.peek('+') || p.peek('-') {
		op := p.input[p.pos]
		p.pos++
		node, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return expressionUnary{op: op, node: node}, nil
	}
	return p.parsePrimary()
}

func (p *expressionParser) parsePrimary() (expressionNode, error) {
	p.skipSpace()
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	if p.peek('(') {
		p.pos++
		node, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if !p.peek(')') {
			return nil, fmt.Errorf("missing closing parenthesis at position %d", p.pos+1)
		}
		p.pos++
		return node, nil
	}
	if strings.HasPrefix(p.input[p.pos:], "${") {
		return p.parseReference()
	}
	if isDigit(p.input[p.pos]) || p.input[p.pos] == '.' {
		return p.parseNumber()
	}
	if isIdentifierStart(rune(p.input[p.pos])) {
		return p.parseCall()
	}
	return nil, fmt.Errorf("unexpected character %q at position %d", p.input[p.pos], p.pos+1)
}

func (p *expressionParser) parseReference() (expressionNode, error) {
	start := p.pos + 2
	end := strings.IndexByte(p.input[start:], '}')
	if end < 0 {
		return nil, fmt.Errorf("unterminated reference at position %d", p.pos+1)
	}
	end += start
	ref := strings.TrimSpace(p.input[start:end])
	if ref == "" {
		return nil, fmt.Errorf("empty reference at position %d", p.pos+1)
	}
	for _, part := range strings.Split(ref, ".") {
		if err := validateSemanticIdentifier(part); err != nil {
			return nil, fmt.Errorf("invalid reference %q: %w", ref, err)
		}
	}
	p.pos = end + 1
	if p.seen == nil {
		p.seen = map[string]struct{}{}
	}
	if _, ok := p.seen[ref]; !ok {
		p.seen[ref] = struct{}{}
		p.refs = append(p.refs, ref)
	}
	return expressionRef(ref), nil
}

func (p *expressionParser) parseNumber() (expressionNode, error) {
	start := p.pos
	seenDot := false
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if isDigit(ch) {
			p.pos++
			continue
		}
		if ch == '.' && !seenDot {
			seenDot = true
			p.pos++
			continue
		}
		break
	}
	value := p.input[start:p.pos]
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		return nil, fmt.Errorf("invalid number %q", value)
	}
	return expressionNumber(value), nil
}

func (p *expressionParser) parseCall() (expressionNode, error) {
	start := p.pos
	for p.pos < len(p.input) && isIdentifierPart(rune(p.input[p.pos])) {
		p.pos++
	}
	name := strings.ToLower(p.input[start:p.pos])
	p.skipSpace()
	if !p.peek('(') {
		return nil, fmt.Errorf("bare identifier %q is not allowed; use ${%s}", name, name)
	}
	allowed := map[string][2]int{
		"coalesce":    {2, 8},
		"nullif":      {2, 2},
		"abs":         {1, 1},
		"round":       {1, 2},
		"safe_divide": {2, 2},
	}
	bounds, ok := allowed[name]
	if !ok {
		return nil, fmt.Errorf("unsupported expression function %q", name)
	}
	if p.seenCalls == nil {
		p.seenCalls = map[string]struct{}{}
	}
	if _, ok := p.seenCalls[name]; !ok {
		p.seenCalls[name] = struct{}{}
		p.calls = append(p.calls, name)
	}
	p.pos++
	args := []expressionNode{}
	for {
		p.skipSpace()
		if p.peek(')') {
			p.pos++
			break
		}
		arg, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		p.skipSpace()
		if p.peek(',') {
			p.pos++
			continue
		}
		if !p.peek(')') {
			return nil, fmt.Errorf("expected comma or closing parenthesis at position %d", p.pos+1)
		}
		p.pos++
		break
	}
	if len(args) < bounds[0] || len(args) > bounds[1] {
		return nil, fmt.Errorf("function %q expects %d..%d arguments", name, bounds[0], bounds[1])
	}
	return expressionCall{name: name, args: args}, nil
}

func (p *expressionParser) skipSpace() {
	for p.pos < len(p.input) && unicode.IsSpace(rune(p.input[p.pos])) {
		p.pos++
	}
}

func (p *expressionParser) peek(ch byte) bool {
	return p.pos < len(p.input) && p.input[p.pos] == ch
}

func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }

func isIdentifierStart(ch rune) bool {
	return ch == '_' || unicode.IsLetter(ch)
}

func isIdentifierPart(ch rune) bool {
	return isIdentifierStart(ch) || unicode.IsDigit(ch)
}
