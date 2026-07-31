package web

import (
	"fmt"
	"os"

	"github.com/rohanthewiz/logger"
	"github.com/rohanthewiz/rweb"

	"mini/store"
)

const defaultPort = "8000"

// StartWebServer starts the HTTP server against the given store. It blocks
// until the server exits.
func StartWebServer(st *store.Store) {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	s := newServer(rweb.ServerOptions{
		Address: fmt.Sprintf(":%s", port),
		Verbose: true,
	}, st)

	if err := s.Run(); err != nil {
		logger.LogErr(err, "where", "at server exit")
	}
}

// newServer builds a configured rweb server with all routes registered.
// The store is injected (rather than reached for globally) so tests can run
// the full route table against a temp-dir database.
func newServer(opts rweb.ServerOptions, st *store.Store) *rweb.Server {
	s := rweb.NewServer(opts)

	// Request logging rides on the caller's Verbose flag rather than being
	// unconditional: rweb.RequestInfo writes a line to stdout per request with
	// no switch of its own, which under `go test -bench` interleaves with the
	// benchmark's own stdout and buries the ns/op results. Benchmarks leave
	// Verbose off (see startTestServer); the real server sets it.
	if opts.Verbose {
		s.Use(rweb.RequestInfo)
	}

	// Asset URLs are fixed for the life of the process, so they are resolved
	// here rather than on every render. A failure is logged and degrades to
	// the unversioned paths (see assetURLs) instead of taking the server
	// down — main() has already failed fast on a genuine compile error.
	pa, err := assetURLs()
	if err != nil {
		logger.LogErr(err, "resolving fingerprinted asset URLs; falling back to unversioned paths")
	}

	h := handlers{
		st:     st,
		status: newStatusCache(statusCacheTTL),
		assets: pa,
	}

	// Liveness/readiness probe
	s.Get("/health", func(ctx rweb.Context) error {
		return ctx.WriteString("ok")
	})

	s.Get("/", h.rootHandler)
	s.Get("/api/status", h.statusHandler)

	// Compiled front-end assets: Stylus -> CSS via go-styl, TypeScript ->
	// minified JS via esbuild, both in-process (see the assets package).
	//
	// Each is reachable two ways, hitting the same handler. The fingerprinted
	// path is what the rendered HTML references and what earns the immutable
	// year-long cache; the bare path stays for anything referencing assets
	// directly and is served under revalidation. Segment counts differ, so
	// the two patterns cannot collide.
	s.Get("/assets/app.css", cssHandler)
	s.Get("/assets/app.js", jsHandler)
	s.Get("/assets/:"+assetVersionParam+"/app.css", cssHandler)
	s.Get("/assets/:"+assetVersionParam+"/app.js", jsHandler)

	return s
}
