package main

import "io/fs"

// webAssets returns the embedded web client. It is nil until the Svelte client
// is built and embedded (a placeholder page is served meanwhile).
func webAssets() fs.FS { return nil }
