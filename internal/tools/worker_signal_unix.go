//go:build unix

package tools

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// workerSignalContext turns parent TERM and terminal interrupt signals into
// cancellation for the worker's child shell. The runner then applies TERM/KILL
// to that shell's separate process group.
func workerSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
