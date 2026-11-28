//go:build superagentweb

package web

import (
	"embed"
	"io/fs"
)

//go:embed dist/index.html dist/assets
var dist embed.FS

// Assets returns the files built by Vite, under the dist directory.
func Assets() fs.FS { return dist }
