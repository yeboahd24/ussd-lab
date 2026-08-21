// Package web holds the static assets served to the phone.
//
// The assets live at the repository root (MVP design §25) rather than inside
// internal/simulator, so this package exists purely to give go:embed a
// directory it can reach -- embed cannot traverse upwards from a package.
//
// Everything is embedded into the binary. There is no build step, no bundler
// and no node_modules: a tool whose premise is removing setup friction should
// not require a JavaScript toolchain to release.
package web

import (
	"embed"
	"io/fs"
)

//go:embed simulator
var simulatorAssets embed.FS

// SimulatorFS returns the simulator UI rooted at its own directory, so files
// are served as "/index.html" rather than "/simulator/index.html".
func SimulatorFS() (fs.FS, error) {
	return fs.Sub(simulatorAssets, "simulator")
}
