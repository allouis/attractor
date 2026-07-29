package config

import (
	"os"
	"path/filepath"
	"testing"
)

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

// TestParseLinearAPIKey captures the [linear] api_key table key
// (items-spec I2: Linear source authenticates via a config API key).
func TestParseLinearAPIKey(t *testing.T) {
	cfg, err := Parse([]byte("[linear]\napi_key = \"lin_abc123\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.LinearAPIKey != "lin_abc123" {
		t.Errorf("LinearAPIKey = %q, want %q", cfg.LinearAPIKey, "lin_abc123")
	}
}

// TestLinearAPIKeyOverlay checks a later layer (cwd) wins over home.
func TestLinearAPIKeyOverlay(t *testing.T) {
	cfg := Config{Providers: map[string]Provider{}}
	cfg.overlay(Config{LinearAPIKey: "home"})
	cfg.overlay(Config{LinearAPIKey: "cwd"})
	if cfg.LinearAPIKey != "cwd" {
		t.Errorf("LinearAPIKey = %q, want %q", cfg.LinearAPIKey, "cwd")
	}
}

// TestLoadLinearAPIKeyFromEnv verifies LINEAR_API_KEY is picked up from the
// process environment when no config file sets it, and that a config file
// still wins when both are present.
func TestLoadLinearAPIKeyFromEnv(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "lin_from_env")

	// No config file anywhere → env fills the key.
	cfg, err := Load(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LinearAPIKey != "lin_from_env" {
		t.Fatalf("env fallback not applied: %q", cfg.LinearAPIKey)
	}

	// A config file setting the key wins over the env.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".attractor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".attractor", "config.toml"),
		[]byte("[linear]\napi_key = \"lin_from_file\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LinearAPIKey != "lin_from_file" {
		t.Fatalf("config file should win over env: %q", cfg.LinearAPIKey)
	}
}
