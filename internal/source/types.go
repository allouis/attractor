// Package source pulls Items — live projections of external work — from
// systems like GitHub and Linear (items-spec §8). A Source fetches on
// demand; Attractor never stores Items. Identity is an engine.ItemRef
// `(source, type, external-id)`; the Vars map carries the fields a
// dispatched workflow consumes (repo, pr_number, url, …).
package source

import (
	"context"

	"github.com/fabro/attractor/internal/engine"
)

// Item is a live projection of a single piece of external work. It is
// never persisted; the durable link back to it is Ref, stamped on any
// Run it spawns.
type Item struct {
	Ref   engine.ItemRef    `json:"ref"`
	Title string            `json:"title"`
	Body  string            `json:"body,omitempty"`
	URL   string            `json:"url,omitempty"`
	Vars  map[string]string `json:"vars,omitempty"`
}

// Filter narrows a List. MVP supports the single "assigned to me" facet
// (items-spec §11); richer filters come later.
type Filter struct {
	Assigned bool
}

// Source fetches Items from one external system. Get(item_ref) — needed
// to resolve an item to workflow vars at dispatch time — arrives with
// its consumer in I4; List backs GET /items today.
type Source interface {
	List(ctx context.Context, filter Filter) ([]Item, error)
}
