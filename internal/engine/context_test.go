package engine

import "testing"

func TestContext_Lookup(t *testing.T) {
	c := NewContext()
	c.Set("present", "value")
	c.Set("empty", "")

	if v, ok := c.Lookup("present"); !ok || v != "value" {
		t.Fatalf("Lookup(present) = %q,%v; want value,true", v, ok)
	}
	if v, ok := c.Lookup("empty"); !ok || v != "" {
		t.Fatalf("Lookup(empty) = %q,%v; want \"\",true", v, ok)
	}
	if v, ok := c.Lookup("missing"); ok || v != "" {
		t.Fatalf("Lookup(missing) = %q,%v; want \"\",false", v, ok)
	}
}
