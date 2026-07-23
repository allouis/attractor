package attractor_test

import (
	"testing"

	"github.com/fabro/attractor/internal/toml"
)

func TestTOML_StripComment(t *testing.T) {
	cases := map[string]string{
		`key = "value"`:            `key = "value"`,
		`key = "value" # trailing`: `key = "value"`,
		`  # whole line`:           ``,
		`key = "a # b"`:            `key = "a # b"`, // # inside quotes survives
		``:                         ``,
	}
	for in, want := range cases {
		if got := toml.StripComment(in); got != want {
			t.Errorf("StripComment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTOML_ParseKeyValue(t *testing.T) {
	k, v, err := toml.ParseKeyValue(`  backend = "acp"  `)
	must(t, err)
	if k != "backend" || v != "acp" {
		t.Fatalf("got key=%q val=%q", k, v)
	}
	if _, _, err := toml.ParseKeyValue(`no-equals-here`); err == nil {
		t.Fatal("expected error for line without '='")
	}
}

func TestTOML_TableHeader(t *testing.T) {
	path, err := toml.TableHeader(`[providers.anthropic]`)
	must(t, err)
	if len(path) != 2 || path[0] != "providers" || path[1] != "anthropic" {
		t.Fatalf("got %v", path)
	}
	single, err := toml.TableHeader(`[vars]`)
	must(t, err)
	if len(single) != 1 || single[0] != "vars" {
		t.Fatalf("got %v", single)
	}
	if _, err := toml.TableHeader(`[unterminated`); err == nil {
		t.Fatal("expected error for malformed header")
	}
}
