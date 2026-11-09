// Package web holds the built frontend that tess serve embeds.
//
// The SvelteKit app builds into build/app (scripts/build-web.sh). Only build/.keep is
// committed, so tess compiles without Node; such a build serves a page saying how to
// build the app.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var build embed.FS

// Build is the frontend, rooted at index.html, or an empty tree when it is not built.
func Build() fs.FS {
	sub, err := fs.Sub(build, "build/app")
	if err != nil {
		panic(err) // a constant, valid path
	}
	return sub
}
