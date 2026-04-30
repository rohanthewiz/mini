// Package shutdown provides functionality for handling application shutdown gracefully.
// It maintains a global shutdown flag that can be checked by various parts of an
// application to determine if a shutdown is in progress. When shutdown is initiated,
// it also sets an environment variable "SHUTDOWN" for compatibility with external
// processes that may need to check this status.
package shutdown

import (
	"os"
	"sync"
)

var (
	isShutdown bool
	mu         sync.RWMutex
)

// CheckShutdown reports whether a shutdown is in progress.
func CheckShutdown() bool {
	mu.RLock()
	defer mu.RUnlock()
	return isShutdown
}

func setShutdown() {
	mu.Lock()
	isShutdown = true
	mu.Unlock()
	_ = os.Setenv("SHUTDOWN", "true")
}