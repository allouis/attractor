// Package dot parses a constrained subset of the Graphviz DOT language
// used by Attractor pipeline definitions. See Attractor spec §2.
package dot

// File is a parsed DOT source file. The DSL admits exactly one digraph
// per file; multi-graph and undirected inputs are rejected at parse time.
type File struct {
	Name       string
	Statements []Statement
}

// Statement is the sum type for parsed top-level statements in declaration
// order. Ordering matters: node/edge defaults apply only to declarations
// that follow them.
type Statement interface{ stmt() }

// GraphAttrStmt is a `graph [k=v, ...]` block applying attributes to the
// enclosing graph.
type GraphAttrStmt struct{ Attrs map[string]string }

// NodeDefaults is a `node [k=v, ...]` block setting defaults for
// subsequent node statements in the same scope.
type NodeDefaults struct{ Attrs map[string]string }

// EdgeDefaults is an `edge [k=v, ...]` block setting defaults for
// subsequent edge statements in the same scope.
type EdgeDefaults struct{ Attrs map[string]string }

// SubgraphStmt is a `subgraph [name] { ... }` block. Per spec §2.3 the
// engine flattens subgraphs at the graph-build phase, but the parser
// preserves their structure so transforms can inspect membership.
type SubgraphStmt struct {
	Name       string
	Statements []Statement
}

// NodeStmt is a single node declaration.
type NodeStmt struct {
	ID    string
	Attrs map[string]string
}

// EdgeStmt is a single directed edge. Chained declarations
// (`A -> B -> C [...]`) are expanded into one EdgeStmt per pair at parse
// time, each carrying a copy of the attribute block.
type EdgeStmt struct {
	From  string
	To    string
	Attrs map[string]string
}

// DeclStmt is a top-level `key = value` declaration, equivalent to the
// corresponding key in a `graph [...]` block.
type DeclStmt struct {
	Key   string
	Value string
}

func (GraphAttrStmt) stmt() {}
func (NodeDefaults) stmt()  {}
func (EdgeDefaults) stmt()  {}
func (SubgraphStmt) stmt()  {}
func (NodeStmt) stmt()      {}
func (EdgeStmt) stmt()      {}
func (DeclStmt) stmt()      {}
