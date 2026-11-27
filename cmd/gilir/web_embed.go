//go:build embedweb

package main

import (
	"io/fs"

	"github.com/armedi/gilir/web"
)

func embeddedWeb() fs.FS {
	// The embed directive requires dist at build time.
	dist, err := fs.Sub(web.Bundle, "dist")
	if err != nil {
		panic(err)
	}
	return dist
}
