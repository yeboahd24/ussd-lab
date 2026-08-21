package simulator

import (
	"fmt"
	"io/fs"
	"net/http"

	"github.com/yeboahd24/ussd-lab/web"
)

// mountUI serves the embedded phone UI.
//
// The asset server is scoped to an embedded filesystem, never to a directory on
// disk. There is therefore no path that can reach the developer's filesystem,
// however the URL is constructed (MVP design §22).
func mountUI(mux *http.ServeMux) error {
	assets, err := web.SimulatorFS()
	if err != nil {
		return fmt.Errorf("simulator: load embedded assets: %w", err)
	}

	fileServer := http.FileServerFS(assets)

	// The catch-all is registered without a method. A method-qualified "GET /"
	// would conflict with the "/api/" subtree -- ServeMux rejects a pattern
	// that is more general in path but narrower in method. http.FileServerFS
	// answers non-GET requests with 405 on its own.
	mux.Handle("/", noCache(fileServer))
	return nil
}

// noCache keeps a phone from holding a stale UI across restarts.
//
// The developer will iterate on this tool; a cached app.js that silently
// disagrees with the server is a confusing failure, and the assets are a few
// kilobytes served over the LAN.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// assetNames lists the embedded files, for diagnostics and tests.
func assetNames() ([]string, error) {
	assets, err := web.SimulatorFS()
	if err != nil {
		return nil, err
	}

	var names []string
	err = fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, path)
		}
		return nil
	})
	return names, err
}
