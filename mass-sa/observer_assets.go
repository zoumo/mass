//go:build observerweb

package sa

import (
	"embed"
	"io/fs"
)

// Production builds require the real Vite output. A missing dist directory is
// a compile error with this tag, never a placeholder frontend.
//
// Vite can emit shared chunks whose names start with an underscore.
// Include those files so every dynamic route dependency is served.
//
//go:embed all:web/dist
var observerWeb embed.FS

func observerAssets() (fs.FS, error) {
	return fs.Sub(observerWeb, "web/dist")
}
