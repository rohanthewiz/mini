// Package main is the entry point for the application.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/rohanthewiz/logger"

	"mini/assets"
	"mini/shutdown"
	"mini/store"
	"mini/web"
)

func main() {
	done := make(chan struct{})
	shutdown.InitShutdownService(done)

	// Pre-compile the embedded front-end sources. They cannot change at
	// runtime, so a compile error is a build defect — surface it here at
	// startup rather than as a 500 on the first request.
	if _, err := assets.CSS(); err != nil {
		logger.LogErr(err, "failed to compile styles.styl")
		os.Exit(1)
	}
	if _, err := assets.JS(); err != nil {
		logger.LogErr(err, "failed to compile app.ts")
		os.Exit(1)
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "mini.db"
	}

	st, err := store.Open(dbPath)
	if err != nil {
		logger.LogErr(err, "failed to open store", "path", dbPath)
		os.Exit(1)
	}

	// Close through the shutdown service so in-flight writes settle before
	// the process exits.
	shutdown.RegisterHook("bytdb store", func(_ time.Duration) error {
		return st.Close()
	})

	go web.StartWebServer(st)

	<-done
	fmt.Println("App exited")
}
