package client

import (
	"encoding/json"
	"testing"
)

func TestRunSummaryUnmarshalsItemRef(t *testing.T) {
	blob := `{"id":"r1","status":"running","item_ref":{"source":"github","type":"pr","external_id":"42"}}`
	var got RunSummary
	if err := json.Unmarshal([]byte(blob), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := ItemRef{Source: "github", Type: "pr", ExternalID: "42"}
	if got.ItemRef == nil || *got.ItemRef != want {
		t.Errorf("ItemRef = %+v, want %+v", got.ItemRef, want)
	}
}

func TestSubmitRequestMarshalsItemRef(t *testing.T) {
	req := SubmitRequest{Dot: "digraph{}", ItemRef: &ItemRef{Source: "github", Type: "pr", ExternalID: "42"}}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back struct {
		ItemRef *ItemRef `json:"item_ref"`
	}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.ItemRef == nil || back.ItemRef.ExternalID != "42" {
		t.Errorf("item_ref not marshalled: %s", data)
	}

	// Omitted when nil.
	bare, _ := json.Marshal(SubmitRequest{Dot: "digraph{}"})
	var m map[string]json.RawMessage
	_ = json.Unmarshal(bare, &m)
	if _, ok := m["item_ref"]; ok {
		t.Errorf("item_ref should be omitted when nil: %s", bare)
	}
}
