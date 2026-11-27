//go:build !embedweb

package main

import "io/fs"

func embeddedWeb() fs.FS {
	return nil
}
