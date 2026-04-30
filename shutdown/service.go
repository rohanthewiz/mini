package shutdown

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const gracePeriod = 15 * time.Second

type HookFunc func(duration time.Duration) error

type Hook struct {
	Name string
	Fn   HookFunc
}

type shutdownHooks struct {
	Hooks []Hook
	lock  sync.Mutex
}

var hooks shutdownHooks

// RegisterHook registers a function to run during graceful shutdown.
func RegisterHook(name string, fn HookFunc) {
	hooks.lock.Lock()
	defer hooks.lock.Unlock()
	hooks.Hooks = append(hooks.Hooks, Hook{name, fn})
	fmt.Printf("Registered shutdown hook: #%d - %s\n", len(hooks.Hooks), name)
}

// InitShutdownService listens for SIGINT/SIGTERM and runs registered hooks.
// It closes the provided done channel after all hooks complete (or grace period elapses).
func InitShutdownService(done chan struct{}) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		defer close(done)
		wg := sync.WaitGroup{}

		sig := <-sigChan
		log.Printf("Received shutdown signal: %v", sig)
		setShutdown()

		log.Printf("Shutting down %d hooks (grace period is: %s)", len(hooks.Hooks), gracePeriod)

		for i, hook := range hooks.Hooks {
			wg.Add(1)
			go func(it int, h Hook) {
				defer wg.Done()
				_ = h.Fn(gracePeriod)
				log.Printf("Shutdown hook (%d) %s completed", it, h.Name)
			}(i, hook)
		}

		holdForWaitGroup := make(chan struct{})
		go func() {
			wg.Wait()
			close(holdForWaitGroup)
		}()

		select {
		case <-holdForWaitGroup:
		case <-time.After(gracePeriod):
			log.Printf("Shutdown hooks timed out after %v", gracePeriod)
		}
		log.Println("Shutdown service done")
	}()
}