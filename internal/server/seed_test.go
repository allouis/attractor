package server

import (
	"reflect"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// TestSeedContextVarsWithoutItem proves plain submits (automation / cron /
// non-item POST /pipelines) still seed their vars into the run's initial
// context, so `$context.k` resolves even with no Item (C3).
func TestSeedContextVarsWithoutItem(t *testing.T) {
	got := seedContext(map[string]string{"k": "v"}, nil)
	want := map[string]string{"k": "v"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("seedContext(vars, nil) = %v, want %v", got, want)
	}
}

// TestSeedContextNoVarsNoItem yields nil so a bare run starts unseeded.
func TestSeedContextNoVarsNoItem(t *testing.T) {
	if got := seedContext(nil, nil); got != nil {
		t.Fatalf("seedContext(nil, nil) = %v, want nil", got)
	}
}

// TestSeedContextItemAddsMetadata keeps the Item path: vars plus the
// Ref-derived item.* keys.
func TestSeedContextItemAddsMetadata(t *testing.T) {
	got := seedContext(map[string]string{"repo": "allouis/attractor"}, &engine.ItemRef{
		Source: "github", Type: "pr", ExternalID: "allouis/attractor#42",
	})
	want := map[string]string{
		"repo":        "allouis/attractor",
		"item.type":   "pr",
		"item.source": "github",
		"item.id":     "allouis/attractor#42",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("seedContext item = %v, want %v", got, want)
	}
}
