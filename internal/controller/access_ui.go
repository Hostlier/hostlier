package controller

import (
	"embed"
	"net/http"
)

// accessPages contains only static assets; no credential or controller state is embedded.
//
//go:embed accessui/*
var accessPages embed.FS

// serveAccessPage exposes the same-origin owner interface without management API authority.
func (c *Controller) serveAccessPage(w http.ResponseWriter, r *http.Request) bool {
	files := map[string]string{"/": "index.html", "/connect": "index.html", "/security": "index.html", "/approve": "index.html", "/access.js": "access.js", "/access.css": "access.css"}
	file, ok := files[r.URL.Path]
	// API and unknown paths remain subject to the ordinary dispatcher.
	if !ok {
		return false
	}
	// Static pages accept no state-changing methods.
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	data, err := accessPages.ReadFile("accessui/" + file)
	// An incomplete embedded distribution must fail closed.
	if err != nil {
		http.Error(w, "Unavailable", 500)
		return true
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", map[string]string{"index.html": "text/html; charset=utf-8", "access.js": "text/javascript; charset=utf-8", "access.css": "text/css; charset=utf-8"}[file])
	// HEAD returns the same headers without a response body.
	if r.Method == "GET" {
		w.Write(data)
	}
	return true
}
