//go:build !embed_ui

// Package webui exposes the built web client for embedding into the binary.
// Without the embed_ui build tag the client is not bundled and the server
// serves a placeholder (or proxies to a dev server).
package webui

import "io/fs"

// Assets returns nil; the client is not embedded in this build.
func Assets() fs.FS { return nil }
