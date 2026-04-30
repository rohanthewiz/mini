// Package main is the entry point for the application.
package main

import (
	"fmt"

	"mini/shutdown"
	"mini/web"
)

func main() {
	done := make(chan struct{})
	shutdown.InitShutdownService(done)

	go web.StartWebServer()

	<-done
	fmt.Println("App exited")
}