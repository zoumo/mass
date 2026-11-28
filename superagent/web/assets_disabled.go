//go:build !superagentweb

package web

import "io/fs"

// Assets is empty in Go-only builds, including the offline OpenAPI exporter.
func Assets() fs.FS { return nil }
