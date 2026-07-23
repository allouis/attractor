package server

import (
	"embed"
	"net/http"
)

// uiFS holds the embedded single-page UI (service-spec §4): one static
// HTML file plus vanilla JS, no build step.
//
//go:embed ui/index.html
var uiFS embed.FS

// serveUI returns the embedded UI page. It reads the file per request so
// the handler stays trivial; the page is small and cached by the client.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	page, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}
