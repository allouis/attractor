package config

import "testing"

func TestParseProviderHeaderDottedName(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"[providers.foo]", "foo"},
		{"[providers.foo.bar]", "foo.bar"},
		{"[providers.a.b.c]", "a.b.c"},
		{"[server]", ""},
		{"[providers]", ""},
	}
	for _, tc := range cases {
		got, err := parseProviderHeader(tc.line)
		if err != nil {
			t.Fatalf("parseProviderHeader(%q) error: %v", tc.line, err)
		}
		if got != tc.want {
			t.Errorf("parseProviderHeader(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// TestParseDottedProviderName exercises the whole Parse path for a dotted
// provider name.
func TestParseDottedProviderName(t *testing.T) {
	cfg, err := Parse([]byte("[providers.foo.bar]\nbackend = \"acp\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := cfg.Providers["foo.bar"]; !ok {
		t.Errorf("provider %q missing; got providers %v", "foo.bar", cfg.Providers)
	}
}
