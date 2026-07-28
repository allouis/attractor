package items

import (
	"fmt"
	"strings"
)

// ItemRef is the durable identity linking a Run back to the external
// Item that spawned it (items-spec: `item_ref = (source, type,
// external-id)`, e.g. `(github, pr, 42)`). Attractor never stores Items
// themselves; the ref is the only Item data that outlives a fetch.
// Canonical string form is `source:type:external_id`.
type ItemRef struct {
	Source     string `json:"source"`
	Type       string `json:"type"`
	ExternalID string `json:"external_id"`
}

// String returns the canonical `source:type:external_id` form.
func (r ItemRef) String() string {
	return r.Source + ":" + r.Type + ":" + r.ExternalID
}

// IsZero reports whether the ref carries no identity.
func (r ItemRef) IsZero() bool {
	return r == ItemRef{}
}

// ParseItemRef parses the canonical `source:type:external_id` form. All
// three segments must be non-empty.
func ParseItemRef(s string) (ItemRef, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return ItemRef{}, fmt.Errorf("invalid item_ref %q: want source:type:external_id", s)
	}
	return ItemRef{Source: parts[0], Type: parts[1], ExternalID: parts[2]}, nil
}
