// Package webui holds the embedded browser pages shared by the single-run
// server and hub. Assets are inlined into the existing HTML responses; no
// frontend build or extra HTTP endpoints are required.
package webui

import (
	_ "embed"
	"strings"
)

//go:embed dashboard.css
var stylesheet string

//go:embed ui-core.js
var core string

//go:embed hub.js
var hubScript string

//go:embed run.js
var runScript string

//go:embed hub.html
var hubPage string

//go:embed waterfall.html
var runPage string

// Waterfall is the per-run page, shared by /ui and the hub's /ui/{id}.
var Waterfall = bundle(runPage, runScript)

// HubIndex is the hub's directory of current work and archived runs.
var HubIndex = bundle(hubPage, hubScript)

func bundle(page, script string) []byte {
	return []byte(strings.NewReplacer("/* UI_CSS */", stylesheet, "/* UI_CORE */", core, "/* UI_APP */", script).Replace(page))
}
