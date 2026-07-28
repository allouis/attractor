package items

import (
	"encoding/json"
	"testing"
)

func TestItemRefString(t *testing.T) {
	ref := ItemRef{Source: "github", Type: "pr", ExternalID: "42"}
	if got := ref.String(); got != "github:pr:42" {
		t.Errorf("String() = %q, want %q", got, "github:pr:42")
	}
}

func TestParseItemRef(t *testing.T) {
	ref, err := ParseItemRef("github:pr:42")
	if err != nil {
		t.Fatalf("ParseItemRef: %v", err)
	}
	want := ItemRef{Source: "github", Type: "pr", ExternalID: "42"}
	if ref != want {
		t.Errorf("ParseItemRef = %+v, want %+v", ref, want)
	}
	if ref.String() != "github:pr:42" {
		t.Errorf("round-trip = %q", ref.String())
	}
}

func TestParseItemRefRejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "github", "github:pr", "github:pr:42:extra", "github::42", ":pr:42"} {
		if _, err := ParseItemRef(in); err == nil {
			t.Errorf("ParseItemRef(%q) = nil error, want error", in)
		}
	}
}

func TestItemRefJSON(t *testing.T) {
	ref := ItemRef{Source: "github", Type: "pr", ExternalID: "42"}
	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(data); got != `{"source":"github","type":"pr","external_id":"42"}` {
		t.Errorf("Marshal = %s", got)
	}
	var back ItemRef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back != ref {
		t.Errorf("round-trip = %+v, want %+v", back, ref)
	}
}

func TestItemRefIsZero(t *testing.T) {
	if !(ItemRef{}).IsZero() {
		t.Error("zero ItemRef should report IsZero")
	}
	if (ItemRef{Source: "github"}).IsZero() {
		t.Error("non-zero ItemRef should not report IsZero")
	}
}
