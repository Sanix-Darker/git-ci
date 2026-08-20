package executionsemantics

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type ConditionContract struct {
	Expression string `json:"expression,omitempty"`
	Evaluable  bool   `json:"evaluable"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

type ConditionContext struct {
	Values          map[string]interface{}
	Success         bool
	Failure         bool
	Cancelled       bool
	CaseInsensitive bool
}

func CompileCondition(expression string) ConditionContract {
	expression = normalizeCondition(expression)
	contract := ConditionContract{Expression: expression, Evaluable: true}
	if expression == "" {
		return contract
	}
	if _, err := parseCondition(expression); err != nil {
		contract.Evaluable = false
		contract.Diagnostic = err.Error()
	}
	return contract
}

func EvaluateCondition(expression string, context ConditionContext) (bool, error) {
	expression = normalizeCondition(expression)
	if expression == "" {
		return true, nil
	}
	node, err := parseCondition(expression)
	if err != nil {
		return false, err
	}
	value, err := node.evaluate(context)
	if err != nil {
		return false, err
	}
	return truthy(value), nil
}

func normalizeCondition(expression string) string {
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "${{") && strings.HasSuffix(expression, "}}") {
		expression = strings.TrimSpace(expression[3 : len(expression)-2])
	}
	return expression
}

type conditionTokenKind int

const (
	tokenEOF conditionTokenKind = iota
	tokenIdentifier
	tokenString
	tokenNumber
	tokenTrue
	tokenFalse
	tokenNull
	tokenLeftParen
	tokenRightParen
	tokenComma
	tokenNot
	tokenAnd
	tokenOr
	tokenEqual
	tokenNotEqual
	tokenLess
	tokenLessEqual
	tokenGreater
	tokenGreaterEqual
)

type conditionToken struct {
	kind    conditionTokenKind
	literal string
}

type conditionLexer struct {
	input []rune
	index int
}

func (lexer *conditionLexer) scan() ([]conditionToken, error) {
	var tokens []conditionToken
	for lexer.index < len(lexer.input) {
		character := lexer.input[lexer.index]
		if unicode.IsSpace(character) {
			lexer.index++
			continue
		}
		switch character {
		case '(':
			tokens = append(tokens, conditionToken{kind: tokenLeftParen, literal: "("})
			lexer.index++
		case ')':
			tokens = append(tokens, conditionToken{kind: tokenRightParen, literal: ")"})
			lexer.index++
		case ',':
			tokens = append(tokens, conditionToken{kind: tokenComma, literal: ","})
			lexer.index++
		case '&':
			if !lexer.consume("&&") {
				return nil, lexer.error("expected &&")
			}
			tokens = append(tokens, conditionToken{kind: tokenAnd, literal: "&&"})
		case '|':
			if !lexer.consume("||") {
				return nil, lexer.error("expected ||")
			}
			tokens = append(tokens, conditionToken{kind: tokenOr, literal: "||"})
		case '!':
			if lexer.consume("!=") {
				tokens = append(tokens, conditionToken{kind: tokenNotEqual, literal: "!="})
			} else {
				lexer.index++
				tokens = append(tokens, conditionToken{kind: tokenNot, literal: "!"})
			}
		case '=':
			if !lexer.consume("==") {
				return nil, lexer.error("expected ==; assignment and regex operators are unsupported")
			}
			tokens = append(tokens, conditionToken{kind: tokenEqual, literal: "=="})
		case '<':
			if lexer.consume("<=") {
				tokens = append(tokens, conditionToken{kind: tokenLessEqual, literal: "<="})
			} else {
				lexer.index++
				tokens = append(tokens, conditionToken{kind: tokenLess, literal: "<"})
			}
		case '>':
			if lexer.consume(">=") {
				tokens = append(tokens, conditionToken{kind: tokenGreaterEqual, literal: ">="})
			} else {
				lexer.index++
				tokens = append(tokens, conditionToken{kind: tokenGreater, literal: ">"})
			}
		case '\'', '"':
			value, err := lexer.string(character)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, conditionToken{kind: tokenString, literal: value})
		default:
			if unicode.IsDigit(character) || character == '-' && lexer.index+1 < len(lexer.input) && unicode.IsDigit(lexer.input[lexer.index+1]) {
				tokens = append(tokens, conditionToken{kind: tokenNumber, literal: lexer.number()})
				continue
			}
			if isIdentifierStart(character) || character == '$' {
				identifier := lexer.identifier()
				kind := tokenIdentifier
				switch strings.ToLower(identifier) {
				case "true":
					kind = tokenTrue
				case "false":
					kind = tokenFalse
				case "null":
					kind = tokenNull
				}
				tokens = append(tokens, conditionToken{kind: kind, literal: identifier})
				continue
			}
			return nil, lexer.error(fmt.Sprintf("unsupported character %q", character))
		}
	}
	return append(tokens, conditionToken{kind: tokenEOF}), nil
}

func (lexer *conditionLexer) consume(expected string) bool {
	runes := []rune(expected)
	if lexer.index+len(runes) > len(lexer.input) {
		return false
	}
	for offset, character := range runes {
		if lexer.input[lexer.index+offset] != character {
			return false
		}
	}
	lexer.index += len(runes)
	return true
}

func (lexer *conditionLexer) string(quote rune) (string, error) {
	lexer.index++
	var output strings.Builder
	for lexer.index < len(lexer.input) {
		character := lexer.input[lexer.index]
		lexer.index++
		if character == quote {
			if quote == '\'' && lexer.index < len(lexer.input) && lexer.input[lexer.index] == quote {
				output.WriteRune(quote)
				lexer.index++
				continue
			}
			return output.String(), nil
		}
		if character == '\\' && quote == '"' && lexer.index < len(lexer.input) {
			escaped := lexer.input[lexer.index]
			lexer.index++
			switch escaped {
			case 'n':
				output.WriteByte('\n')
			case 't':
				output.WriteByte('\t')
			case '\\', '"':
				output.WriteRune(escaped)
			default:
				return "", lexer.error("unsupported string escape")
			}
			continue
		}
		output.WriteRune(character)
	}
	return "", lexer.error("unterminated string")
}

func (lexer *conditionLexer) number() string {
	start := lexer.index
	if lexer.input[lexer.index] == '-' {
		lexer.index++
	}
	for lexer.index < len(lexer.input) && (unicode.IsDigit(lexer.input[lexer.index]) || strings.ContainsRune(".eE+-", lexer.input[lexer.index])) {
		lexer.index++
	}
	return string(lexer.input[start:lexer.index])
}

func (lexer *conditionLexer) identifier() string {
	start := lexer.index
	if lexer.input[lexer.index] == '$' {
		lexer.index++
	}
	for lexer.index < len(lexer.input) {
		character := lexer.input[lexer.index]
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' && character != '.' && character != '-' {
			break
		}
		lexer.index++
	}
	return string(lexer.input[start:lexer.index])
}

func (lexer *conditionLexer) error(message string) error {
	return fmt.Errorf("condition: %s at character %d", message, lexer.index+1)
}

func isIdentifierStart(character rune) bool {
	return unicode.IsLetter(character) || character == '_'
}

type conditionNode interface {
	evaluate(ConditionContext) (interface{}, error)
}

type literalNode struct{ value interface{} }

func (node literalNode) evaluate(ConditionContext) (interface{}, error) { return node.value, nil }

type variableNode struct{ name string }

func (node variableNode) evaluate(context ConditionContext) (interface{}, error) {
	name := strings.TrimPrefix(node.name, "$")
	if value, exists := context.Values[node.name]; exists {
		return value, nil
	}
	if value, exists := context.Values[name]; exists {
		return value, nil
	}
	for key, value := range context.Values {
		if strings.EqualFold(key, node.name) || strings.EqualFold(key, name) {
			return value, nil
		}
	}
	return nil, fmt.Errorf("condition: context %q is unavailable", node.name)
}

type unaryNode struct {
	operator conditionTokenKind
	operand  conditionNode
}

func (node unaryNode) evaluate(context ConditionContext) (interface{}, error) {
	value, err := node.operand.evaluate(context)
	if err != nil {
		return nil, err
	}
	return !truthy(value), nil
}

type binaryNode struct {
	operator    conditionTokenKind
	left, right conditionNode
}

func (node binaryNode) evaluate(context ConditionContext) (interface{}, error) {
	left, err := node.left.evaluate(context)
	if err != nil {
		return nil, err
	}
	if node.operator == tokenAnd && !truthy(left) {
		return false, nil
	}
	if node.operator == tokenOr && truthy(left) {
		return true, nil
	}
	right, err := node.right.evaluate(context)
	if err != nil {
		return nil, err
	}
	switch node.operator {
	case tokenAnd:
		return truthy(left) && truthy(right), nil
	case tokenOr:
		return truthy(left) || truthy(right), nil
	case tokenEqual, tokenNotEqual:
		equal := equalValues(left, right, context.CaseInsensitive)
		if node.operator == tokenNotEqual {
			equal = !equal
		}
		return equal, nil
	case tokenLess, tokenLessEqual, tokenGreater, tokenGreaterEqual:
		comparison, err := compareValues(left, right, context.CaseInsensitive)
		if err != nil {
			return nil, err
		}
		switch node.operator {
		case tokenLess:
			return comparison < 0, nil
		case tokenLessEqual:
			return comparison <= 0, nil
		case tokenGreater:
			return comparison > 0, nil
		default:
			return comparison >= 0, nil
		}
	}
	return nil, fmt.Errorf("condition: unsupported binary operator")
}

type functionNode struct {
	name      string
	arguments []conditionNode
}

func (node functionNode) evaluate(context ConditionContext) (interface{}, error) {
	name := strings.ToLower(node.name)
	switch name {
	case "success":
		return context.Success, nil
	case "failure":
		return context.Failure, nil
	case "cancelled":
		return context.Cancelled, nil
	case "always":
		return true, nil
	}
	arguments := make([]interface{}, 0, len(node.arguments))
	for _, argument := range node.arguments {
		value, err := argument.evaluate(context)
		if err != nil {
			return nil, err
		}
		arguments = append(arguments, value)
	}
	left := fmt.Sprint(arguments[0])
	right := fmt.Sprint(arguments[1])
	if context.CaseInsensitive {
		left, right = strings.ToLower(left), strings.ToLower(right)
	}
	switch name {
	case "contains":
		return strings.Contains(left, right), nil
	case "startswith":
		return strings.HasPrefix(left, right), nil
	case "endswith":
		return strings.HasSuffix(left, right), nil
	}
	return nil, fmt.Errorf("condition: unsupported function %q", node.name)
}

type conditionParser struct {
	tokens []conditionToken
	index  int
}

func parseCondition(expression string) (conditionNode, error) {
	lexer := conditionLexer{input: []rune(expression)}
	tokens, err := lexer.scan()
	if err != nil {
		return nil, err
	}
	parser := conditionParser{tokens: tokens}
	node, err := parser.parseOr()
	if err != nil {
		return nil, err
	}
	if parser.peek().kind != tokenEOF {
		return nil, fmt.Errorf("condition: unexpected token %q", parser.peek().literal)
	}
	return node, nil
}

func (parser *conditionParser) parseOr() (conditionNode, error) {
	left, err := parser.parseAnd()
	if err != nil {
		return nil, err
	}
	for parser.match(tokenOr) {
		right, err := parser.parseAnd()
		if err != nil {
			return nil, err
		}
		left = binaryNode{operator: tokenOr, left: left, right: right}
	}
	return left, nil
}

func (parser *conditionParser) parseAnd() (conditionNode, error) {
	left, err := parser.parseComparison()
	if err != nil {
		return nil, err
	}
	for parser.match(tokenAnd) {
		right, err := parser.parseComparison()
		if err != nil {
			return nil, err
		}
		left = binaryNode{operator: tokenAnd, left: left, right: right}
	}
	return left, nil
}

func (parser *conditionParser) parseComparison() (conditionNode, error) {
	left, err := parser.parseUnary()
	if err != nil {
		return nil, err
	}
	operator := parser.peek().kind
	switch operator {
	case tokenEqual, tokenNotEqual, tokenLess, tokenLessEqual, tokenGreater, tokenGreaterEqual:
		parser.index++
		right, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		return binaryNode{operator: operator, left: left, right: right}, nil
	default:
		return left, nil
	}
}

func (parser *conditionParser) parseUnary() (conditionNode, error) {
	if parser.match(tokenNot) {
		operand, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		return unaryNode{operator: tokenNot, operand: operand}, nil
	}
	return parser.parsePrimary()
}

func (parser *conditionParser) parsePrimary() (conditionNode, error) {
	token := parser.peek()
	parser.index++
	switch token.kind {
	case tokenString:
		return literalNode{value: token.literal}, nil
	case tokenNumber:
		value, err := strconv.ParseFloat(token.literal, 64)
		if err != nil {
			return nil, fmt.Errorf("condition: invalid number %q", token.literal)
		}
		return literalNode{value: value}, nil
	case tokenTrue:
		return literalNode{value: true}, nil
	case tokenFalse:
		return literalNode{value: false}, nil
	case tokenNull:
		return literalNode{value: nil}, nil
	case tokenIdentifier:
		if !parser.match(tokenLeftParen) {
			return variableNode{name: token.literal}, nil
		}
		arguments := []conditionNode{}
		if parser.peek().kind != tokenRightParen {
			for {
				argument, err := parser.parseOr()
				if err != nil {
					return nil, err
				}
				arguments = append(arguments, argument)
				if !parser.match(tokenComma) {
					break
				}
			}
		}
		if !parser.match(tokenRightParen) {
			return nil, fmt.Errorf("condition: expected ) after function arguments")
		}
		if err := validateFunction(token.literal, len(arguments)); err != nil {
			return nil, err
		}
		return functionNode{name: token.literal, arguments: arguments}, nil
	case tokenLeftParen:
		node, err := parser.parseOr()
		if err != nil {
			return nil, err
		}
		if !parser.match(tokenRightParen) {
			return nil, fmt.Errorf("condition: expected )")
		}
		return node, nil
	default:
		return nil, fmt.Errorf("condition: expected value, found %q", token.literal)
	}
}

func (parser *conditionParser) peek() conditionToken {
	return parser.tokens[parser.index]
}

func (parser *conditionParser) match(kind conditionTokenKind) bool {
	if parser.peek().kind != kind {
		return false
	}
	parser.index++
	return true
}

func validateFunction(name string, arguments int) error {
	switch strings.ToLower(name) {
	case "success", "failure", "cancelled", "always":
		if arguments != 0 {
			return fmt.Errorf("condition: function %s expects no arguments", name)
		}
	case "contains", "startswith", "endswith":
		if arguments != 2 {
			return fmt.Errorf("condition: function %s expects two arguments", name)
		}
	default:
		return fmt.Errorf("condition: unsupported function %q", name)
	}
	return nil
}

func truthy(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return true
	}
}

func equalValues(left, right interface{}, caseInsensitive bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if leftNumber, leftOK := numericValue(left); leftOK {
		if rightNumber, rightOK := numericValue(right); rightOK {
			return leftNumber == rightNumber
		}
	}
	leftString, rightString := fmt.Sprint(left), fmt.Sprint(right)
	if caseInsensitive {
		return strings.EqualFold(leftString, rightString)
	}
	return leftString == rightString
}

func compareValues(left, right interface{}, caseInsensitive bool) (int, error) {
	if leftNumber, leftOK := numericValue(left); leftOK {
		if rightNumber, rightOK := numericValue(right); rightOK {
			switch {
			case leftNumber < rightNumber:
				return -1, nil
			case leftNumber > rightNumber:
				return 1, nil
			default:
				return 0, nil
			}
		}
	}
	leftString, rightString := fmt.Sprint(left), fmt.Sprint(right)
	if caseInsensitive {
		leftString, rightString = strings.ToLower(leftString), strings.ToLower(rightString)
	}
	return strings.Compare(leftString, rightString), nil
}

func numericValue(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	default:
		return 0, false
	}
}
