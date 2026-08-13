// Package webui holds the embedded browser pages shared by the
// single-run server and the hub. Both serve the same waterfall page —
// it drives entirely off the /pipelines/{id} API shape, which run and
// hub expose identically (live-proxied or archive-backed) — so one
// page works everywhere. The hub adds an index page listing runs.
package webui

import _ "embed"

// Waterfall is the per-run waterfall page. It resolves its run id from
// the /ui/{id} URL path, falling back to the first run in /pipelines
// (the single-run server's case).
//
//go:embed waterfall.html
var Waterfall []byte

// HubIndex is the hub's run-list page: live + archived runs with
// status and reachability, linking each to /ui/{id}.
//
//go:embed hub.html
var HubIndex []byte
