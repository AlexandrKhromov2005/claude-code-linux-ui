package main

import (
	"io/fs"

	webui "github.com/AlexandrKhromov2005/claude-code-linux-ui/web"
)

// webAssets returns the embedded web client, or nil when the binary was built
// without the embed_ui tag (a placeholder page is served instead).
func webAssets() fs.FS { return webui.Assets() }
