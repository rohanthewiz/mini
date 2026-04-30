package web

import (
	"fmt"
	"os"

	"github.com/rohanthewiz/logger"
	"github.com/rohanthewiz/rweb"
)

const defaultPort = "8000"

// StartWebServer starts the HTTP server. It blocks until the server exits.
func StartWebServer() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	s := newServer(rweb.ServerOptions{
		Address: fmt.Sprintf(":%s", port),
		Verbose: true,
	})

	if err := s.Run(); err != nil {
		logger.LogErr(err, "where", "at server exit")
	}
}

// newServer builds a configured rweb server with all routes registered.
func newServer(opts rweb.ServerOptions) *rweb.Server {
	s := rweb.NewServer(opts)

	s.Use(rweb.RequestInfo)

	// Liveness/readiness probe
	s.Get("/health", func(ctx rweb.Context) error {
		return ctx.WriteString("ok")
	})

	s.Get("/", rootHandler)

	return s
}