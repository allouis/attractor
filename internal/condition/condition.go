// Package condition implements Attractor's edge condition expression
// language (spec §10). The grammar is intentionally tiny:
//
//	ConditionExpr ::= Clause ( '&&' Clause )*
//	Clause        ::= Key Operator Literal | Key
//	Operator      ::= '=' | '!='
//
// Clauses are AND-combined, evaluated left to right.
package condition

import (
	"fmt"
	"strings"
)

// Op is the operator used to compare a key against a literal.
type Op int

const (
	OpTruthy   Op = iota // bare key — truthy lookup
	OpEqual              // =
	OpNotEqual           // !=
)

// Clause is a single key-operator-literal triple from a parsed condition.
type Clause struct {
	Key     string
	Op      Op
	Literal string
}

// Expression is a parsed condition: a sequence of clauses joined by `&&`.
type Expression struct {
	Clauses []Clause
}

// Parse compiles a condition expression. An empty expression is valid
// and yields a zero-clause Expression that always evaluates to true.
func Parse(src string) (*Expression, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return &Expression{}, nil
	}
	parts := strings.Split(src, "&&")
	expr := &Expression{}
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		clause, err := parseClause(raw)
		if err != nil {
			return nil, err
		}
		expr.Clauses = append(expr.Clauses, clause)
	}
	return expr, nil
}

func parseClause(raw string) (Clause, error) {
	// `!=` first so we don't pick up the leading `=` from `==`.
	if i := strings.Index(raw, "!="); i >= 0 {
		key := strings.TrimSpace(raw[:i])
		val := strings.TrimSpace(raw[i+2:])
		if key == "" {
			return Clause{}, fmt.Errorf("condition: empty key in clause %q", raw)
		}
		return Clause{Key: key, Op: OpNotEqual, Literal: parseLiteral(val)}, nil
	}
	if i := strings.Index(raw, "="); i >= 0 {
		key := strings.TrimSpace(raw[:i])
		val := strings.TrimSpace(raw[i+1:])
		// Accept `==` as equality: the leading `=` of the value would
		// otherwise silently become part of the literal (`outcome == fail`
		// parsing as literal "= fail"), producing an edge that never
		// matches with no error anywhere.
		val = strings.TrimSpace(strings.TrimPrefix(val, "="))
		if key == "" {
			return Clause{}, fmt.Errorf("condition: empty key in clause %q", raw)
		}
		return Clause{Key: key, Op: OpEqual, Literal: parseLiteral(val)}, nil
	}
	// Bare key: truthy lookup.
	return Clause{Key: raw, Op: OpTruthy}, nil
}

func parseLiteral(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

// Resolver looks up a condition key against an outcome+context. See spec
// §10.4 for the key resolution rules.
type Resolver interface {
	Resolve(key string) string
}

// Evaluate applies the parsed expression against the supplied resolver.
// Missing keys compare as empty strings; empty expressions return true.
func (e *Expression) Evaluate(r Resolver) bool {
	for _, c := range e.Clauses {
		v := r.Resolve(c.Key)
		switch c.Op {
		case OpTruthy:
			if v == "" || v == "false" || v == "0" {
				return false
			}
		case OpEqual:
			if v != c.Literal {
				return false
			}
		case OpNotEqual:
			if v == c.Literal {
				return false
			}
		}
	}
	return true
}

// MapResolver is a minimal Resolver backed by a string map. The engine's
// runtime resolver is richer (covering `outcome`, `preferred_label`, and
// the `context.` prefix); MapResolver is the kind tests reach for.
type MapResolver map[string]string

// Resolve returns the value for key or empty string if missing.
func (m MapResolver) Resolve(key string) string { return m[key] }
