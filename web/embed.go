//go:build embed_ui

// Package webui exposes the built web client for embedding into the binary.
// Build with -tags embed_ui after running the frontend build (npm run build).
package webui

import (
	"io/fs"

	"embed"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built client rooted at its dist directory.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}
