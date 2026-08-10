// Package source pulls Items — live projections of external work — from
// systems like GitHub and Linear (items-spec §8). A Source fetches on
// demand; Attractor never stores Items. Identity is an items.ItemRef
// `(source, type, external-id)`; the Vars map carries the fields a
// dispatched workflow consumes (repo, pr_number, url, …).
package source

import (
	"context"

	"github.com/allouis/attractor/internal/items"
)

// Item is a live projection of a single piece of external work. It is
// never persisted; the durable link back to it is Ref, stamped on any
// Run it spawns.
type Item struct {
	Ref   items.ItemRef     `json:"ref"`
	Title string            `json:"title"`
	Body  string            `json:"body,omitempty"`
	URL   string            `json:"url,omitempty"`
	Vars  map[string]string `json:"vars,omitempty"`
}

// Filter narrows a List. MVP supports the single "mine" facet
// (items-spec §11): items authored by OR assigned to me. richer filters
// come later.
type Filter struct {
	// Assigned selects the user's own work — authored by OR assigned to
	// me (not merely assigned). The field name is retained; its meaning
	// broadened when GitHub items grew to PRs + issues.
	Assigned bool
}

// Source fetches Items from one external system. List backs GET /items;
// Get resolves a single item_ref to its workflow vars at dispatch time
// (items-spec I4: POST /items/run).
type Source interface {
	List(ctx context.Context, filter Filter) ([]Item, error)
	Get(ctx context.Context, ref items.ItemRef) (Item, error)
}
