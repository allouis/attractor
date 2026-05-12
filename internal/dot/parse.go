package dot

import (
	"fmt"
)

// Parse reads DOT source and returns the parsed File. It enforces the
// Attractor subset: exactly one `digraph` per file, directed edges only,
// no `strict` modifier, no HTML labels.
func Parse(src string) (*File, error) {
	p := &parser{lex: newLexer(src)}
	if err := p.advance(); err != nil {
		return nil, err
	}
	return p.parseFile()
}

type parser struct {
	lex *lexer
	cur token
}

func (p *parser) advance() error {
	tk, err := p.lex.next()
	if err != nil {
		return err
	}
	p.cur = tk
	return nil
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("line %d col %d: %s", p.cur.line, p.cur.col, fmt.Sprintf(format, args...))
}

func (p *parser) parseFile() (*File, error) {
	if p.cur.kind == tkWord && p.cur.value == "strict" {
		return nil, p.errorf("`strict` graphs are not supported")
	}
	if p.cur.kind == tkWord && p.cur.value == "graph" {
		return nil, p.errorf("undirected graphs are not supported; use `digraph`")
	}
	if !(p.cur.kind == tkWord && p.cur.value == "digraph") {
		return nil, p.errorf("expected `digraph`, got %q", p.cur.value)
	}
	if err := p.advance(); err != nil {
		return nil, err
	}

	file := &File{}
	// Optional graph name.
	if p.cur.kind == tkWord || p.cur.kind == tkBare {
		file.Name = p.cur.value
		if err := p.advance(); err != nil {
			return nil, err
		}
	} else if p.cur.kind == tkString {
		file.Name = p.cur.value
		if err := p.advance(); err != nil {
			return nil, err
		}
	}

	if p.cur.kind != tkLBrace {
		return nil, p.errorf("expected `{`, got %q", p.cur.value)
	}
	if err := p.advance(); err != nil {
		return nil, err
	}

	stmts, err := p.parseStatements()
	if err != nil {
		return nil, err
	}
	file.Statements = stmts

	if p.cur.kind != tkRBrace {
		return nil, p.errorf("expected `}` at end of digraph")
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if p.cur.kind != tkEOF {
		return nil, p.errorf("unexpected content after closing `}` (multi-graph files are rejected)")
	}
	return file, nil
}

func (p *parser) parseStatements() ([]Statement, error) {
	var stmts []Statement
	for p.cur.kind != tkRBrace && p.cur.kind != tkEOF {
		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		if stmt != nil {
			stmts = append(stmts, stmt)
		}
		// optional semicolon
		if p.cur.kind == tkSemi {
			if err := p.advance(); err != nil {
				return nil, err
			}
		}
	}
	return stmts, nil
}

func (p *parser) parseStatement() (Statement, error) {
	switch p.cur.kind {
	case tkWord:
		switch p.cur.value {
		case "graph":
			return p.parseAttrDefaults("graph")
		case "node":
			return p.parseAttrDefaults("node")
		case "edge":
			return p.parseAttrDefaults("edge")
		case "subgraph":
			return p.parseSubgraph()
		}
		return p.parseIdentStmt()
	case tkBare:
		return p.parseIdentStmt()
	case tkLBrace:
		// Anonymous subgraph block (rare but supported by DOT). Treat like
		// a subgraph with empty name.
		if err := p.advance(); err != nil {
			return nil, err
		}
		inner, err := p.parseStatements()
		if err != nil {
			return nil, err
		}
		if p.cur.kind != tkRBrace {
			return nil, p.errorf("expected `}` to close subgraph")
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
		return SubgraphStmt{Statements: inner}, nil
	}
	return nil, p.errorf("unexpected token %q in statement position", p.cur.value)
}

func (p *parser) parseAttrDefaults(target string) (Statement, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}
	// A bare `graph key = value;` at top level (without `[]`) is treated as
	// a graph-level attribute declaration when target is "graph". We only
	// reach this branch when an attribute block follows.
	if p.cur.kind != tkLBracket {
		// `graph [ ... ]` is required for defaults blocks; if no bracket
		// follows after `node`/`edge`, that's an error.
		if target == "graph" && p.cur.kind == tkEquals {
			return nil, p.errorf("malformed graph attribute (use `graph [k=v]` or `k = v`)")
		}
		return nil, p.errorf("expected `[` after `%s`", target)
	}
	attrs, err := p.parseAttrBlock()
	if err != nil {
		return nil, err
	}
	switch target {
	case "graph":
		return GraphAttrStmt{Attrs: attrs}, nil
	case "node":
		return NodeDefaults{Attrs: attrs}, nil
	case "edge":
		return EdgeDefaults{Attrs: attrs}, nil
	}
	return nil, p.errorf("unknown default target %q", target)
}

func (p *parser) parseSubgraph() (Statement, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}
	name := ""
	if p.cur.kind == tkWord || p.cur.kind == tkBare {
		name = p.cur.value
		if err := p.advance(); err != nil {
			return nil, err
		}
	}
	if p.cur.kind != tkLBrace {
		return nil, p.errorf("expected `{` after subgraph name")
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	inner, err := p.parseStatements()
	if err != nil {
		return nil, err
	}
	if p.cur.kind != tkRBrace {
		return nil, p.errorf("expected `}` to close subgraph")
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	return SubgraphStmt{Name: name, Statements: inner}, nil
}

// parseIdentStmt is the entry point after a leading identifier. The
// identifier is one of:
//   - a top-level `key = value` graph attribute
//   - the start of a node statement (with optional attr block)
//   - the start of an edge statement (`A -> B [...]`)
func (p *parser) parseIdentStmt() (Statement, error) {
	first := p.cur.value
	if err := p.advance(); err != nil {
		return nil, err
	}
	if p.cur.kind == tkEquals {
		if err := p.advance(); err != nil {
			return nil, err
		}
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return DeclStmt{Key: first, Value: val}, nil
	}
	if p.cur.kind == tkArrow {
		return p.parseEdgeStmt(first)
	}
	// Node statement; optional attribute block.
	attrs := map[string]string{}
	if p.cur.kind == tkLBracket {
		var err error
		attrs, err = p.parseAttrBlock()
		if err != nil {
			return nil, err
		}
	}
	return NodeStmt{ID: first, Attrs: attrs}, nil
}

// parseEdgeStmt expands chained `A -> B -> C [...]` into one EdgeStmt per
// adjacent pair, each carrying a copy of the optional attribute block.
// The result is returned as a synthetic StatementGroup-equivalent: the
// parser pushes additional edges by mutating the parent statement list
// indirectly via a wrapper. Here we use a small slice wrapper.
func (p *parser) parseEdgeStmt(from string) (Statement, error) {
	chain := []string{from}
	for p.cur.kind == tkArrow {
		if err := p.advance(); err != nil {
			return nil, err
		}
		if p.cur.kind != tkWord && p.cur.kind != tkBare {
			return nil, p.errorf("expected identifier after `->`, got %q", p.cur.value)
		}
		chain = append(chain, p.cur.value)
		if err := p.advance(); err != nil {
			return nil, err
		}
	}
	attrs := map[string]string{}
	if p.cur.kind == tkLBracket {
		var err error
		attrs, err = p.parseAttrBlock()
		if err != nil {
			return nil, err
		}
	}
	if len(chain) == 2 {
		return EdgeStmt{From: chain[0], To: chain[1], Attrs: attrs}, nil
	}
	// Expand the chain: emit a group statement that carries multiple edges.
	group := edgeGroup{}
	for i := 0; i < len(chain)-1; i++ {
		copyAttrs := make(map[string]string, len(attrs))
		for k, v := range attrs {
			copyAttrs[k] = v
		}
		group.edges = append(group.edges, EdgeStmt{From: chain[i], To: chain[i+1], Attrs: copyAttrs})
	}
	return group, nil
}

// edgeGroup is an internal Statement wrapping multiple EdgeStmts produced
// by chain expansion. The graph builder unfolds these as it walks the AST.
type edgeGroup struct {
	edges []EdgeStmt
}

func (edgeGroup) stmt() {}

// EdgesOf returns the underlying edges for an edgeGroup statement, or a
// single-element slice if the statement is a plain EdgeStmt. It is the
// helper the graph builder uses to flatten chained edges without
// inspecting the unexported edgeGroup type directly.
func EdgesOf(stmt Statement) []EdgeStmt {
	switch s := stmt.(type) {
	case EdgeStmt:
		return []EdgeStmt{s}
	case edgeGroup:
		return s.edges
	}
	return nil
}

// IsEdge reports whether a statement is an edge or an expanded chain of edges.
func IsEdge(stmt Statement) bool {
	switch stmt.(type) {
	case EdgeStmt, edgeGroup:
		return true
	}
	return false
}

func (p *parser) parseAttrBlock() (map[string]string, error) {
	if err := p.advance(); err != nil { // consume [
		return nil, err
	}
	attrs := map[string]string{}
	for p.cur.kind != tkRBracket {
		if p.cur.kind != tkWord && p.cur.kind != tkBare {
			return nil, p.errorf("expected attribute key, got %q", p.cur.value)
		}
		key := p.cur.value
		if err := p.advance(); err != nil {
			return nil, err
		}
		if p.cur.kind != tkEquals {
			return nil, p.errorf("expected `=` after attribute key %q", key)
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		attrs[key] = val
		if p.cur.kind == tkComma {
			if err := p.advance(); err != nil {
				return nil, err
			}
			continue
		}
		if p.cur.kind == tkRBracket {
			break
		}
		return nil, p.errorf("expected `,` or `]` between attributes, got %q", p.cur.value)
	}
	if err := p.advance(); err != nil { // consume ]
		return nil, err
	}
	return attrs, nil
}

// parseValue accepts any of: string, number, identifier word, or bare
// value. The returned form is canonical: strings have escapes resolved,
// other forms are returned as raw lexemes for downstream coercion.
func (p *parser) parseValue() (string, error) {
	switch p.cur.kind {
	case tkString, tkWord, tkBare, tkNumber:
		v := p.cur.value
		if err := p.advance(); err != nil {
			return "", err
		}
		return v, nil
	}
	return "", p.errorf("expected value, got %q", p.cur.value)
}
