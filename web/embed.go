//go:build embedweb

package web

import "embed"

// Bundle contains the dashboard produced by pnpm build.
//
//go:embed dist/index.html dist/assets
var Bundle embed.FS
