package cli

import (
	"io"
	"runtime/debug"
	"strings"
)

// version is the release version stamped into a binary by the linker:
// -X github.com/armedi/gilir/internal/cli.version=0.3.2. The release build
// (npm/build.mjs) sets it from package.json. It stays empty for plain `go
// build`, `go install` and `go run` builds, where the module build information
// supplies a version instead.
var version string

// devVersion labels a build that carries neither a stamped version nor usable
// module build information, such as `go run ./cmd/gilir`.
const devVersion = "dev"

// writeVersion prints "gilir <version>" for a human, or the version alone as
// JSON. It needs no service, so `gilir version` works when nothing is running.
func writeVersion(w io.Writer, jsonMode bool) error {
	if jsonMode {
		return writeJSON(w, versionOutput{Version: currentVersion()})
	}
	writeText(w, "gilir "+currentVersion())
	return nil
}

// currentVersion reports the version this binary was built as.
func currentVersion() string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(version, info, ok)
}

// resolveVersion prefers the linker-stamped version, then the module version
// the toolchain recorded for a `go install` build, then devVersion. A leading
// "v" is removed so the output matches package.json and install.sh's
// --version. Pseudo-versions and the toolchain's "+dirty" suffix stay intact,
// because they tell a local build apart from its release.
func resolveVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "" {
		return strings.TrimPrefix(stamped, "v")
	}
	if ok && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return devVersion
}
