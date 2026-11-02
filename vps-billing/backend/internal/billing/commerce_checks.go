package billing

import (
	"encoding/json"
	"strings"
)

// Commerce constraints use signed amounts and arithmetic. Parse only their
// small expression grammar so MySQL's operand parentheses do not hide a changed
// bound, operator, identifier or literal. This is not a general SQL parser.
type commerceCheckParser struct {
	tokens []string
	pos    int
	ok     bool
}

func (p *commerceCheckParser) take() string {
	if p.pos >= len(p.tokens) {
		p.ok = false
		return ""
	}
	s := p.tokens[p.pos]
	p.pos++
	return s
}
func (p *commerceCheckParser) peek() string {
	if p.pos >= len(p.tokens) {
		return ""
	}
	return p.tokens[p.pos]
}
func (p *commerceCheckParser) expression(minimum int) any {
	token := p.take()
	var left any
	switch token {
	case "(":
		left = p.expression(1)
		if p.take() != ")" {
			p.ok = false
		}
	case "-":
		value := p.expression(6)
		left = []any{"negative", value}
	default:
		if token == "" || token == ")" || token == "," || token == "and" || token == "or" || token == "in" || token == "is" {
			p.ok = false
		}
		left = token
	}
	for p.ok {
		op := p.peek()
		priority := 0
		switch op {
		case "or":
			priority = 1
		case "and":
			priority = 2
		case "=", "<", ">", "<=", ">=", "in", "is":
			priority = 3
		case "+", "-":
			priority = 4
		}
		if priority < minimum || priority == 0 {
			break
		}
		p.take()
		if op == "is" {
			if p.take() != "null" {
				p.ok = false
				break
			}
			left = []any{"is null", left}
			continue
		}
		if op == "in" {
			if p.take() != "(" {
				p.ok = false
				break
			}
			values := []any{}
			for p.ok {
				values = append(values, p.expression(5))
				separator := p.take()
				if separator == ")" {
					break
				}
				if separator != "," {
					p.ok = false
				}
			}
			left = []any{op, left, values}
			continue
		}
		right := p.expression(priority + 1)
		left = []any{op, left, right}
	}
	return left
}
func commerceCheckCanonical(expression string) (string, bool) {
	normalized, ok := normalizeMigrationCheck(expression)
	if !ok {
		return "", false
	}
	parser := commerceCheckParser{tokens: strings.Fields(normalized), ok: true}
	tree := parser.expression(1)
	if !parser.ok || parser.pos != len(parser.tokens) {
		return "", false
	}
	raw, err := json.Marshal(tree)
	return string(raw), err == nil
}
func commerceCheckMatches(check currencyMigrationCheck, expected string) bool {
	if check.kind != "CHECK" || check.enforced != "YES" {
		return false
	}
	actual, ok := commerceCheckCanonical(check.clause)
	want, valid := commerceCheckCanonical(expected)
	return ok && valid && actual == want
}
