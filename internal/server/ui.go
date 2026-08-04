package server

import (
	"bytes"
	"embed"
	"net/http"
)

// uiFS holds the embedded single-page UI (service-spec §4): the static HTML
// page plus the compiled Tailwind stylesheet (generated from ui/index.html by
// the flake's tailwindcss build step; see ui/tailwind.config.js). serveUI
// inlines the stylesheet into the page so it stays a single self-contained
// response — no extra request, no external asset.
//
//go:embed ui/index.html ui/tailwind.css
var uiFS embed.FS

// twPlaceholder is the marker in index.html that serveUI swaps for the
// compiled Tailwind <style>.
var twPlaceholder = []byte("<!-- tailwind:inject -->")

// serveUI returns the embedded UI page. Cache-Control: no-cache makes the
// browser revalidate every load, so a redeployed binary's updated page is
// picked up immediately rather than served stale from disk cache.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	page, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if css, cssErr := uiFS.ReadFile("ui/tailwind.css"); cssErr == nil {
		style := append([]byte("<style>"), append(css, []byte("</style>")...)...)
		page = bytes.Replace(page, twPlaceholder, style, 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(page)
}
