package setup

import (
	"reflect"
	"testing"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
)

func declaredVarsOf(t *testing.T, src string) []string {
	t.Helper()
	file, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return DeclaredVars(g)
}

func TestDeclaredVars(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"none", `digraph d { a [shape=Mdiamond] }`, nil},
		{"single", `digraph d { vars="x" a [shape=Mdiamond] }`, []string{"x"}},
		{"trimmed", `digraph d { vars=" x , y ,, z " a [shape=Mdiamond] }`, []string{"x", "y", "z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := declaredVarsOf(t, tc.src)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DeclaredVars = %v, want %v", got, tc.want)
			}
		})
	}
}
